package antigravity

import (
	"bytes"
	crypto_rand "crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/liam-auto/liam/internal/config"
	"github.com/liam-auto/liam/internal/db"
)

var (
	baseURLs = []string{
		"https://daily-cloudcode-pa.googleapis.com",
		"https://cloudcode-pa.googleapis.com",
	}
	apiPath         = "/v1internal"
	maxOutputTokens = 65536
)

// AG-specific retry constants — ported from CLIProxyAPI/9router.
// These govern the INTERNAL retry loop inside the executor, not the
// server.go outer loop. AG's 429/503 are fundamentally different from
// other providers: most are transient server-side capacity issues that
// resolve within seconds if you just retry the same account.
const (
	// Max retry attempts per request (same account, before giving up
	// and letting server.go try a different account or fail).
	agMaxRetryAttempts = 6

	// 503 "no capacity" — retry same account, fast.
	agNoCapacityDelayBase = 2 * time.Second
	agNoCapacityDelayMax  = 2 * time.Second

	// Soft 429 (RATE_LIMIT_EXCEEDED with no/unknown details) — retry
	// same account with short backoff.
	agSoft429DelayBase = 500 * time.Millisecond
	agSoft429DelayMax  = 3 * time.Second

	// Transient 429 RESOURCE_EXHAUSTED (unknown classification) —
	// very fast retry.
	agTransient429DelayBase = 100 * time.Millisecond
	agTransient429DelayMax  = 500 * time.Millisecond

	// RATE_LIMIT_EXCEEDED with Retry-After < 3s → instant retry same account.
	agInstantRetryThreshold = 3 * time.Second
	// RATE_LIMIT_EXCEEDED with Retry-After 3s-5m → short cooldown, let
	// server.go switch to a different account.
	agShortCooldownThreshold = 5 * time.Minute

	// Max Retry-After we'll actually wait inline (10s). Beyond that,
	// let server.go handle account switching.
	agMaxInlineRetryAfter = 10 * time.Second
)

// ag429Kind classifies a 429 response for retry decisions.
type ag429Kind int

const (
	ag429Soft           ag429Kind = iota // Unknown / no details → retry same account
	ag429InstantRetry                    // RATE_LIMIT_EXCEEDED + retryAfter < 3s → instant retry same account
	ag429ShortCooldown                   // RATE_LIMIT_EXCEEDED + retryAfter 3s-5m → switch account
	ag429QuotaExhausted                  // QUOTA_EXHAUSTED → switch account, mark cooldown long
)

type ag429Decision struct {
	kind       ag429Kind
	retryAfter time.Duration
	reason     string
}

// retryAfterRegex parses "Resets in 151h52m27s" or "reset after 2h7m23s"
// from error messages.
var retryAfterRegex = regexp.MustCompile(`(?:reset[s]?\s+(?:in|after)\s+)(\d+h)?(\d+m)?(\d+(?:\.\d+)?s)?`)

// classify429 parses an AG 429 error body and classifies it.
// Port of CLIProxyAPI's decideAntigravity429.
func classify429(body []byte) ag429Decision {
	decision := ag429Decision{kind: ag429Soft}
	if len(body) == 0 {
		return decision
	}

	// Parse retry-after from error message body
	decision.retryAfter = parseRetryFromBody(body)

	// Parse error.status
	var errResp struct {
		Error struct {
			Status  string `json:"status"`
			Message string `json:"message"`
			Details []struct {
				Type   string `json:"@type"`
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &errResp); err != nil {
		return decision
	}

	if !strings.EqualFold(errResp.Error.Status, "RESOURCE_EXHAUSTED") {
		return decision
	}

	for _, detail := range errResp.Error.Details {
		if detail.Type != "type.googleapis.com/google.rpc.ErrorInfo" {
			continue
		}
		reason := strings.TrimSpace(detail.Reason)
		decision.reason = reason

		switch {
		case strings.EqualFold(reason, "QUOTA_EXHAUSTED"):
			decision.kind = ag429QuotaExhausted
			return decision
		case strings.EqualFold(reason, "RATE_LIMIT_EXCEEDED"):
			if decision.retryAfter <= 0 {
				decision.kind = ag429Soft
				return decision
			}
			switch {
			case decision.retryAfter < agInstantRetryThreshold:
				decision.kind = ag429InstantRetry
			case decision.retryAfter < agShortCooldownThreshold:
				decision.kind = ag429ShortCooldown
			default:
				decision.kind = ag429QuotaExhausted
			}
			return decision
		}
	}

	// Fallback keyword scan
	lowerBody := strings.ToLower(string(body))
	for _, kw := range []string{"quota_exhausted", "quota exhausted", "individual quota reached"} {
		if strings.Contains(lowerBody, kw) {
			decision.kind = ag429QuotaExhausted
			decision.reason = "quota_exhausted"
			return decision
		}
	}

	return decision
}

// parseRetryFromBody extracts retry delay from error body (headers or message).
func parseRetryFromBody(body []byte) time.Duration {
	// Try parsing retryDelay from error.details[*].retryDelay
	var errResp struct {
		Error struct {
			Details []struct {
				Type       string `json:"@type"`
				RetryDelay string `json:"retryDelay"`
			} `json:"details"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &errResp); err == nil {
		for _, detail := range errResp.Error.Details {
			if detail.Type == "type.googleapis.com/google.rpc.RetryInfo" && detail.RetryDelay != "" {
				if d, err := parseGoogleDuration(detail.RetryDelay); err == nil && d > 0 {
					return d
				}
			}
		}
		// Try parsing from error message text ("Resets in 151h52m27s")
		if d := parseRetryFromMessage(errResp.Error.Message); d > 0 {
			return d
		}
	}
	return 0
}

// parseGoogleDuration parses Google's duration format like "546747.772701164s"
func parseGoogleDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	if strings.HasSuffix(s, "s") {
		numStr := strings.TrimSuffix(s, "s")
		f, err := strconv.ParseFloat(numStr, 64)
		if err != nil {
			return 0, err
		}
		return time.Duration(f * float64(time.Second)), nil
	}
	return time.ParseDuration(s)
}

// parseRetryFromMessage extracts retry time from human-readable error
// messages like "reset after 2h7m23s".
func parseRetryFromMessage(msg string) time.Duration {
	matches := retryAfterRegex.FindStringSubmatch(strings.ToLower(msg))
	if matches == nil {
		return 0
	}
	var total time.Duration
	if matches[1] != "" {
		h, _ := strconv.Atoi(strings.TrimSuffix(matches[1], "h"))
		total += time.Duration(h) * time.Hour
	}
	if matches[2] != "" {
		m, _ := strconv.Atoi(strings.TrimSuffix(matches[2], "m"))
		total += time.Duration(m) * time.Minute
	}
	if matches[3] != "" {
		f, _ := strconv.ParseFloat(strings.TrimSuffix(matches[3], "s"), 64)
		total += time.Duration(f * float64(time.Second))
	}
	return total
}

// isNoCapacity returns true for 503 "no capacity available" errors.
func isNoCapacity(status int, body []byte) bool {
	if status != 503 {
		return false
	}
	return strings.Contains(strings.ToLower(string(body)), "no capacity available")
}

// Device profiles for stabilization (pinned per account)
var deviceProfiles = []string{
	"darwin/arm64",
	"darwin/amd64",
	"linux/amd64",
	"linux/arm64",
	"win32/x64",
}

// getStableUserAgent returns a per-account stable User-Agent
func getStableUserAgent(accountID string) string {
	hash := 0
	for _, c := range accountID {
		hash = hash*31 + int(c)
	}
	if hash < 0 {
		hash = -hash
	}
	profile := deviceProfiles[hash%len(deviceProfiles)]
	return "antigravity/1.107.0 " + profile
}

// Executor handles Antigravity (Gemini Code Assist) requests
type Executor struct {
	cfg    *config.Config
	client *http.Client
}

// NewExecutor creates a new Antigravity executor.
// We force HTTP/1.1 to match NodeJS behavior (anti-fingerprint).
func NewExecutor(cfg *config.Config) *Executor {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     false, // Anti-fingerprint: force HTTP/1.1 like CLIProxyAPI
		MaxIdleConns:          50,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSNextProto:          make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
		TLSClientConfig: &tls.Config{
			NextProtos: []string{"http/1.1"},
		},
	}
	return &Executor{
		cfg:    cfg,
		client: &http.Client{Transport: transport},
	}
}

// ExecuteResult wraps the HTTP response with metadata about whether
// the executor already handled retries internally and what kind of
// error it is, so server.go can make informed decisions about whether
// to try a different account or give up.
type ExecuteResult struct {
	Response *http.Response
	// ShouldSwitchAccount is true when the executor exhausted its
	// internal retries and determined this account should be rotated
	// (e.g. real quota exhaustion). When false, server.go should
	// retry the same account (e.g. transient 503).
	ShouldSwitchAccount bool
	// CooldownMs suggests how long to cool down this account (only
	// set when ShouldSwitchAccount is true).
	CooldownMs int
	// Reason is a human-readable string for logging.
	Reason string
}

func (e *Executor) Execute(account *db.Account, model string, body []byte, stream bool) (*http.Response, error) {
	return e.ExecuteWithSession(account, model, body, stream, "")
}

// ExecuteWithResult is the rich-return version that lets server.go
// distinguish "retry same account" from "switch account".
func (e *Executor) ExecuteWithResult(account *db.Account, model string, body []byte, stream bool, sessionID string) (*ExecuteResult, error) {
	var creds db.AGCredentials
	if err := json.Unmarshal(account.Credentials, &creds); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}

	if creds.AccessToken == "" {
		return nil, fmt.Errorf("no access token for account %s", account.Email)
	}

	upstreamModel := strings.TrimPrefix(model, "ag/")
	if upstreamModel == "gemini-3.1-pro-high" {
		upstreamModel = "gemini-pro-agent"
	}
	geminiBody, err := e.translateRequest(upstreamModel, body, stream, &creds, sessionID)
	if err != nil {
		return nil, fmt.Errorf("translate request: %w", err)
	}

	action := "generateContent"
	if stream {
		action = "streamGenerateContent?alt=sse"
	}

	userAgent := getStableUserAgent(account.ID)

	// =========================================================
	// INTERNAL RETRY LOOP — handles 429/503 within the executor.
	// Only surfaces to server.go when:
	//   1. Retries exhausted (let server.go try different account)
	//   2. Real quota exhaustion (QUOTA_EXHAUSTED → must switch)
	//   3. Success (200)
	//   4. Non-retryable error (400/401/403/404/422)
	// =========================================================
	var lastErr error
	var lastBody []byte
	var lastStatus int

	for attempt := 0; attempt < agMaxRetryAttempts; attempt++ {
		// Try each base URL (daily → prod fallback)
		for urlIdx, baseURL := range baseURLs {
			reqURL := fmt.Sprintf("%s%s:%s", baseURL, apiPath, action)

			req, reqErr := http.NewRequest("POST", reqURL, bytes.NewReader(geminiBody))
			if reqErr != nil {
				return nil, fmt.Errorf("create request: %w", reqErr)
			}

			req.Close = true // Anti-fingerprint: Connection: close
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+creds.AccessToken)
			req.Header.Set("User-Agent", userAgent)
			req.Header.Set("x-request-source", "local")
			if stream {
				req.Header.Set("Accept", "text/event-stream")
			}

			resp, doErr := e.client.Do(req)
			if doErr != nil {
				lastErr = doErr
				if urlIdx+1 < len(baseURLs) {
					log.Printf("[AG-EXEC] attempt %d network error on %s, trying fallback", attempt+1, baseURL)
					continue // try next URL
				}
				break // all URLs failed, go to next attempt
			}

			// ---- SUCCESS ----
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				if stream {
					return &ExecuteResult{Response: e.translateStreamingResponse(resp)}, nil
				}
				translated, tErr := e.translateResponse(resp)
				if tErr != nil {
					return &ExecuteResult{Response: resp}, nil
				}
				return &ExecuteResult{Response: translated}, nil
			}

			// Read body for classification
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastStatus = resp.StatusCode
			lastBody = respBody
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateAG(string(respBody), 200))

			// ---- NON-RETRYABLE (return immediately, don't waste retries) ----
			if resp.StatusCode == 400 || resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 404 || resp.StatusCode == 422 {
				errorResp := &http.Response{
					StatusCode: resp.StatusCode,
					Header:     resp.Header.Clone(),
					Body:       io.NopCloser(bytes.NewReader(respBody)),
				}
				result := &ExecuteResult{Response: errorResp}
				// 401/403 = auth issue → switch account
				if resp.StatusCode == 401 || resp.StatusCode == 403 {
					result.ShouldSwitchAccount = true
					result.CooldownMs = 120000 // 2 min
					result.Reason = fmt.Sprintf("auth error %d", resp.StatusCode)
				}
				return result, nil
			}

			// ---- 429 CLASSIFICATION ----
			if resp.StatusCode == 429 {
				decision := classify429(respBody)

				switch decision.kind {
				case ag429InstantRetry:
					// RATE_LIMIT_EXCEEDED + retryAfter < 3s → wait and retry same account
					wait := decision.retryAfter + 800*time.Millisecond
					if wait > agMaxInlineRetryAfter {
						wait = agMaxInlineRetryAfter
					}
					log.Printf("[AG-EXEC] 429 instant retry (retryAfter=%v) for %s, waiting %v (attempt %d/%d)",
						decision.retryAfter, model, wait, attempt+1, agMaxRetryAttempts)
					time.Sleep(wait)
					break // break URL loop, continue attempt loop

				case ag429ShortCooldown:
					// RATE_LIMIT_EXCEEDED + retryAfter 3s-5m → surface to server.go to switch account
					log.Printf("[AG-EXEC] 429 short cooldown (retryAfter=%v) for %s, surfacing to switch account",
						decision.retryAfter, model)
					errorResp := &http.Response{
						StatusCode: 429,
						Header:     resp.Header.Clone(),
						Body:       io.NopCloser(bytes.NewReader(respBody)),
					}
					cooldownMs := int(decision.retryAfter.Milliseconds())
					if cooldownMs < 5000 {
						cooldownMs = 5000
					}
					return &ExecuteResult{
						Response:            errorResp,
						ShouldSwitchAccount: true,
						CooldownMs:          cooldownMs,
						Reason:              "rate_limit_short_cooldown",
					}, nil

				case ag429QuotaExhausted:
					// Real quota exhaustion → surface immediately, long cooldown
					log.Printf("[AG-EXEC] 429 QUOTA_EXHAUSTED for %s (reason=%s, retryAfter=%v), surfacing to switch account",
						model, decision.reason, decision.retryAfter)
					errorResp := &http.Response{
						StatusCode: 429,
						Header:     resp.Header.Clone(),
						Body:       io.NopCloser(bytes.NewReader(respBody)),
					}
					cooldownMs := 300000 // 5 min default
					if decision.retryAfter > 5*time.Minute {
						cooldownMs = int(decision.retryAfter.Milliseconds())
						// Cap at 30 min
						if cooldownMs > 1800000 {
							cooldownMs = 1800000
						}
					}
					return &ExecuteResult{
						Response:            errorResp,
						ShouldSwitchAccount: true,
						CooldownMs:          cooldownMs,
						Reason:              "quota_exhausted",
					}, nil

				case ag429Soft:
					// Unknown 429 / no details → retry same account with backoff
					delay := time.Duration(attempt+1) * agSoft429DelayBase
					if delay > agSoft429DelayMax {
						delay = agSoft429DelayMax
					}
					log.Printf("[AG-EXEC] 429 soft retry for %s, waiting %v (attempt %d/%d)",
						model, delay, attempt+1, agMaxRetryAttempts)
					time.Sleep(delay)

					// Also try fallback URL first
					if urlIdx+1 < len(baseURLs) {
						log.Printf("[AG-EXEC] trying fallback URL for 429")
						continue // try next URL
					}
					break // continue to next attempt
				}
				break // break URL loop for instant retry (continue attempt loop)
			}

			// ---- 503 — server capacity issue, retry same account ----
			if resp.StatusCode == 503 {
				delay := agNoCapacityDelayBase
				log.Printf("[AG-EXEC] 503 for %s, retry same account in %v (attempt %d/%d)",
					model, delay, attempt+1, agMaxRetryAttempts)

				// Try fallback URL first
				if urlIdx+1 < len(baseURLs) {
					log.Printf("[AG-EXEC] trying fallback URL for 503")
					continue // try next URL
				}

				time.Sleep(delay)
				break // continue to next attempt
			}

			// ---- 502/504 — gateway error, retry with delay ----
			if resp.StatusCode == 502 || resp.StatusCode == 504 {
				delay := agNoCapacityDelayBase
				if urlIdx+1 < len(baseURLs) {
					continue // try fallback URL
				}
				log.Printf("[AG-EXEC] %d for %s, retry in %v (attempt %d/%d)",
					resp.StatusCode, model, delay, attempt+1, agMaxRetryAttempts)
				time.Sleep(delay)
				break // continue to next attempt
			}

			// ---- Other errors — surface to server.go ----
			errorResp := &http.Response{
				StatusCode: resp.StatusCode,
				Header:     resp.Header.Clone(),
				Body:       io.NopCloser(bytes.NewReader(respBody)),
			}
			return &ExecuteResult{
				Response:            errorResp,
				ShouldSwitchAccount: true,
				CooldownMs:          30000,
				Reason:              fmt.Sprintf("unknown_error_%d", resp.StatusCode),
			}, nil
		}
	}

	// All retries exhausted
	if lastBody != nil {
		errorResp := &http.Response{
			StatusCode: lastStatus,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(lastBody)),
		}
		return &ExecuteResult{
			Response:            errorResp,
			ShouldSwitchAccount: false, // retries exhausted but might work next time
			Reason:              "retries_exhausted",
		}, nil
	}

	return nil, fmt.Errorf("execute request failed: %v", lastErr)
}

func (e *Executor) ExecuteWithSession(account *db.Account, model string, body []byte, stream bool, sessionID string) (*http.Response, error) {
	result, err := e.ExecuteWithResult(account, model, body, stream, sessionID)
	if err != nil {
		return nil, err
	}
	return result.Response, nil
}

func (e *Executor) translateRequest(model string, body []byte, stream bool, creds *db.AGCredentials, forceSessionID string) ([]byte, error) {
	var openaiReq OpenAIRequest
	if err := json.Unmarshal(body, &openaiReq); err != nil {
		return nil, fmt.Errorf("parse openai request: %w", err)
	}

	isClaude := strings.Contains(strings.ToLower(model), "claude")

	// Pass 1: Build tool call ID to name map
	tcID2Name := make(map[string]string)
	for _, msg := range openaiReq.Messages {
		if msg.Role == "assistant" {
			for _, tc := range msg.ToolCalls {
				if tc.Type == "function" && tc.ID != "" && tc.Function.Name != "" {
					tcID2Name[tc.ID] = tc.Function.Name
				}
			}
		}
	}

	// Pass 2: Build tool responses cache
	toolResponses := make(map[string]string)
	for _, msg := range openaiReq.Messages {
		if msg.Role == "tool" && msg.ToolCallID != "" {
			toolResponses[msg.ToolCallID] = string(msg.Content)
		}
	}

	contents := []GeminiContent{}
	var systemParts []GeminiPart
	firstUserText := ""

	// We'll process tool responses in batches after the assistant message
	var pendingToolCallIDs []string

	flushPendingTools := func() {
		if len(pendingToolCallIDs) > 0 {
			toolParts := []GeminiPart{}
			for _, fid := range pendingToolCallIDs {
				if resp, ok := toolResponses[fid]; ok {
					name := "tool"
					if n, exists := tcID2Name[fid]; exists {
						name = n
					}

					var parsedResp interface{}
					json.Unmarshal([]byte(extractTextContent(json.RawMessage(resp))), &parsedResp)
					if parsedResp == nil {
						parsedResp = extractTextContent(json.RawMessage(resp))
					}

					toolParts = append(toolParts, GeminiPart{
						FunctionResponse: &GeminiFunctionResponse{
							ID:       fid,
							Name:     sanitizeFunctionName(name),
							Response: map[string]interface{}{"result": parsedResp},
						},
					})
				}
			}
			if len(toolParts) > 0 {
				contents = append(contents, GeminiContent{Role: "user", Parts: toolParts})
			}
			pendingToolCallIDs = nil
		}
	}

	for _, msg := range openaiReq.Messages {
		if msg.Role == "tool" {
			continue // handled via batching
		}
		flushPendingTools()

		switch msg.Role {
		case "system", "developer":
			text := extractTextContent(msg.Content)
			if text != "" {
				systemParts = append(systemParts, GeminiPart{Text: &text})
			}

		case "user":
			parts := convertContentToParts(msg.Content)
			if len(parts) > 0 {
				if firstUserText == "" && parts[0].Text != nil {
					firstUserText = *parts[0].Text
				}
				contents = append(contents, GeminiContent{Role: "user", Parts: parts})
			}

		case "assistant":
			parts := []GeminiPart{}
			text := extractTextContent(msg.Content)

			if msg.ReasoningContent != nil && *msg.ReasoningContent != "" {
				rc := *msg.ReasoningContent
				t := true
				parts = append(parts, GeminiPart{
					Thought: &t,
					Text:    &rc,
				})
			}

			if text != "" {
				parts = append(parts, GeminiPart{Text: &text})
			}

			if len(msg.ToolCalls) > 0 {
				skipSig := "skip_thought_signature_validator"
				for _, tc := range msg.ToolCalls {
					args := map[string]interface{}{}
					json.Unmarshal([]byte(tc.Function.Arguments), &args)
					part := GeminiPart{
						FunctionCall: &GeminiFunctionCall{
							ID:   tc.ID,
							Name: sanitizeFunctionName(tc.Function.Name),
							Args: args,
						},
					}
					if !isClaude {
						part.ThoughtSignature = &skipSig
					}
					parts = append(parts, part)
					pendingToolCallIDs = append(pendingToolCallIDs, tc.ID)
				}
			}
			if len(parts) > 0 {
				contents = append(contents, GeminiContent{Role: "model", Parts: parts})
			}
		}
	}
	flushPendingTools()

	var tools []GeminiToolGroup
	if len(openaiReq.Tools) > 0 {
		declarations := []GeminiFunctionDeclaration{}
		for _, tool := range openaiReq.Tools {
			if tool.Type == "function" {
				params := cleanSchema(tool.Function.Parameters)
				declarations = append(declarations, GeminiFunctionDeclaration{
					Name:        sanitizeFunctionName(tool.Function.Name),
					Description: tool.Function.Description,
					Parameters:  params,
				})
			}
		}
		if len(declarations) > 0 {
			tools = []GeminiToolGroup{{FunctionDeclarations: declarations}}
		}
	}

	genConfig := GeminiGenerationConfig{}

	if openaiReq.Temperature != nil {
		genConfig.Temperature = openaiReq.Temperature
	}
	if openaiReq.TopP != nil {
		genConfig.TopP = openaiReq.TopP
	}

	// Max tokens (Claude vs Gemini)
	if isClaude {
		if openaiReq.MaxTokens > 0 {
			genConfig.MaxOutputTokens = openaiReq.MaxTokens
		} else {
			genConfig.MaxOutputTokens = maxOutputTokens
		}
		// Cap at 16384 for safety (per 9router)
		if genConfig.MaxOutputTokens > 16384 {
			genConfig.MaxOutputTokens = 16384
		}
	} else {
		// Gemini API handles maxOutputTokens on server. Must be omitted.
		genConfig.MaxOutputTokens = 0
	}

	if openaiReq.ReasoningEffort != "" {
		effort := strings.ToLower(openaiReq.ReasoningEffort)
		if effort == "auto" {
			genConfig.ThinkingConfig = &GeminiThinkingConfig{
				ThinkingBudget:  -1,
				IncludeThoughts: true,
			}
		} else if effort != "none" {
			genConfig.ThinkingConfig = &GeminiThinkingConfig{
				ThinkingLevel:   effort,
				IncludeThoughts: true,
			}
		}
	}

	projectID := creds.ProjectID
	if projectID == "" {
		projectID = generateProjectID()
	}

	sessionID := forceSessionID
	if sessionID == "" {
		if firstUserText != "" {
			h := sha256.Sum256([]byte(firstUserText))
			n := int64(binary.BigEndian.Uint64(h[:8])) & 0x7FFFFFFFFFFFFFFF
			sessionID = "-" + strconv.FormatInt(n, 10)
		} else {
			sessionID = "-" + fmt.Sprintf("%d", time.Now().UnixNano())
		}
	}

	requestType := "agent"
	requestID := "agent-" + uuid.New().String()
	if strings.Contains(strings.ToLower(model), "image") {
		requestType = "image_gen"
		requestID = "gemini_" + uuid.New().String()
	}

	envelope := CloudCodeRequest{
		Project:     projectID,
		Model:       model,
		UserAgent:   "antigravity",
		RequestType: requestType,
		RequestID:   requestID,
		Request: GeminiRequest{
			Contents:         contents,
			GenerationConfig: genConfig,
			SessionID:        sessionID,
		},
	}

	if len(systemParts) > 0 {
		envelope.Request.SystemInstruction = &GeminiSystemInstruction{
			Role:  "user",
			Parts: systemParts,
		}
	}

	if len(tools) > 0 {
		envelope.Request.Tools = tools
		envelope.Request.ToolConfig = &GeminiToolConfig{
			FunctionCallingConfig: GeminiFunctionCallingConfig{Mode: "VALIDATED"},
		}
	}

	return json.Marshal(envelope)
}

func (e *Executor) translateStreamingResponse(resp *http.Response) *http.Response {
	pr, pw := io.Pipe()

	go func() {
		defer pw.Close()
		defer resp.Body.Close()

		buf := make([]byte, 4096)
		var accumulated string

		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				accumulated += string(buf[:n])

				for {
					idx := strings.Index(accumulated, "\n")
					if idx == -1 {
						break
					}
					line := accumulated[:idx]
					accumulated = accumulated[idx+1:]

					if strings.HasPrefix(line, "data: ") {
						data := strings.TrimPrefix(line, "data: ")
						translated := translateSSEChunk(data)
						if translated != "" {
							fmt.Fprintf(pw, "data: %s\n\n", translated)
						}
					}
				}
			}
			if err != nil {
				fmt.Fprintf(pw, "data: [DONE]\n\n")
				break
			}
		}
	}()

	return &http.Response{
		StatusCode: 200,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
		},
		Body: pr,
	}
}

func (e *Executor) translateResponse(resp *http.Response) (*http.Response, error) {
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var geminiResp GeminiResponse
	if err := json.Unmarshal(body, &geminiResp); err != nil {
		return &http.Response{
			StatusCode: resp.StatusCode,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(body)),
		}, nil
	}

	openaiResp := translateGeminiToOpenAI(&geminiResp)
	translated, _ := json.Marshal(openaiResp)

	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(translated)),
	}, nil
}

func generateProjectID() string {
	adjs := []string{"useful", "bright", "swift", "calm", "bold"}
	nouns := []string{"fuze", "wave", "spark", "flow", "core"}
	b := make([]byte, 3)
	crypto_rand.Read(b)
	adj := adjs[int(b[0])%len(adjs)]
	noun := nouns[int(b[1])%len(nouns)]
	return fmt.Sprintf("%s-%s-%s", adj, noun, uuid.New().String()[:5])
}

func sanitizeFunctionName(name string) string {
	if name == "" {
		return "_unknown"
	}
	result := strings.Builder{}
	for i, c := range name {
		if i == 0 {
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' {
				result.WriteRune(c)
			} else {
				result.WriteRune('_')
				result.WriteRune(c)
			}
		} else {
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '.' || c == ':' || c == '-' {
				result.WriteRune(c)
			} else {
				result.WriteRune('_')
			}
		}
	}
	s := result.String()
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}
