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
	"net/http"
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

func (e *Executor) Execute(account *db.Account, model string, body []byte, stream bool) (*http.Response, error) {
	return e.ExecuteWithSession(account, model, body, stream, "")
}

func (e *Executor) ExecuteWithSession(account *db.Account, model string, body []byte, stream bool, sessionID string) (*http.Response, error) {
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
	// ...
	geminiBody, err := e.translateRequest(upstreamModel, body, stream, &creds, sessionID)
	if err != nil {
		return nil, fmt.Errorf("translate request: %w", err)
	}

	// DEBUG LOG
	// fmt.Printf("[AG DEBUG] Payload for %s: %s\n", upstreamModel, string(geminiBody))

	action := "generateContent"
	if stream {
		action = "streamGenerateContent?alt=sse"
	}

	var lastErr error
	var resp *http.Response

	userAgent := getStableUserAgent(account.ID)

	for _, baseURL := range baseURLs {
		url := fmt.Sprintf("%s%s:%s", baseURL, apiPath, action)

		req, err := http.NewRequest("POST", url, bytes.NewReader(geminiBody))
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}

		req.Close = true // Anti-fingerprint: Connection: close
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+creds.AccessToken)
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("x-request-source", "local")
		if stream {
			req.Header.Set("Accept", "text/event-stream")
		}

		resp, err = e.client.Do(req)
		if err != nil {
			lastErr = err
			continue // try fallback
		}

		if resp.StatusCode == 429 && len(baseURLs) > 1 {
			// If rate limited, try the next URL if available
			lastErr = fmt.Errorf("rate limited")
			// Read and close body to reuse connection
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			continue
		}

		// Success or final error status
		break
	}

	if resp == nil {
		return nil, fmt.Errorf("execute request failed on all endpoints: %v", lastErr)
	}

	if stream && resp.StatusCode == 200 {
		return e.translateStreamingResponse(resp), nil
	}

	if !stream && resp.StatusCode == 200 {
		translatedResp, err := e.translateResponse(resp)
		if err != nil {
			return resp, nil
		}
		return translatedResp, nil
	}

	return resp, nil
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
		if isClaude {
			envelope.Request.ToolConfig = &GeminiToolConfig{
				FunctionCallingConfig: GeminiFunctionCallingConfig{Mode: "VALIDATED"},
			}
		} else {
			envelope.Request.ToolConfig = &GeminiToolConfig{
				FunctionCallingConfig: GeminiFunctionCallingConfig{Mode: "AUTO"},
			}
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
