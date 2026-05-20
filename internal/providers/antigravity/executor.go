package antigravity

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/liam-auto/liam/internal/config"
	"github.com/liam-auto/liam/internal/db"
	upstream "github.com/liam-auto/liam/internal/httputil"
)

const (
	baseURL         = "https://daily-cloudcode-pa.googleapis.com"
	apiPath         = "/v1internal"
	maxOutputTokens = 65536 // No artificial cap - let model use full capacity
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
// Same account always gets same device profile (prevents fingerprint drift)
func getStableUserAgent(accountID string) string {
	// Deterministic selection based on account ID hash
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

// NewExecutor creates a new Antigravity executor
func NewExecutor(cfg *config.Config) *Executor {
	return &Executor{
		cfg:    cfg,
		client: upstream.NewUTLSClient(120 * time.Second),
	}
}

// Execute sends a request to Antigravity API
func (e *Executor) Execute(account *db.Account, model string, body []byte, stream bool) (*http.Response, error) {
	return e.ExecuteWithSession(account, model, body, stream, "")
}

// ExecuteWithSession sends a request with a stable session ID (for anti-ban + prompt caching)
func (e *Executor) ExecuteWithSession(account *db.Account, model string, body []byte, stream bool, sessionID string) (*http.Response, error) {
	// Parse credentials
	var creds db.AGCredentials
	if err := json.Unmarshal(account.Credentials, &creds); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}

	if creds.AccessToken == "" {
		return nil, fmt.Errorf("no access token for account %s", account.Email)
	}

	// Strip "ag/" prefix from model name
	upstreamModel := strings.TrimPrefix(model, "ag/")

	// Translate OpenAI request to Gemini Cloud Code format
	geminiBody, err := e.translateRequest(upstreamModel, body, stream, &creds)
	if err != nil {
		return nil, fmt.Errorf("translate request: %w", err)
	}

	// Build URL
	action := "generateContent"
	if stream {
		action = "streamGenerateContent?alt=sse"
	}
	url := fmt.Sprintf("%s%s:%s", baseURL, apiPath, action)

	// Build request
	req, err := http.NewRequest("POST", url, bytes.NewReader(geminiBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	// Headers — use per-account stable User-Agent (device profile stabilization)
	if sessionID == "" {
		sessionID = generateSessionID(account.Email)
	}
	userAgent := getStableUserAgent(account.ID)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+creds.AccessToken)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("X-Machine-Session-Id", sessionID)
	req.Header.Set("x-request-source", "local")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}

	// Strip proxy/fingerprint leak headers, then add native Antigravity headers.
	// X-Goog-Api-Client matches the Node.js Gemini SDK the native extension sends.
	// Accept-Encoding matches Node.js default (gzip/deflate/br, no zstd).
	upstream.ScrubUpstreamHeaders(req)
	req.Header.Set("X-Goog-Api-Client", "google-genai-sdk/1.41.0 gl-node/v22.19.0")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")

	// Execute
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}

	// If streaming, translate response on-the-fly
	if stream && resp.StatusCode == 200 {
		translatedResp := e.translateStreamingResponse(resp)
		return translatedResp, nil
	}

	// Non-streaming: translate response
	if !stream && resp.StatusCode == 200 {
		translatedResp, err := e.translateResponse(resp)
		if err != nil {
			return resp, nil // Return raw on translation error
		}
		return translatedResp, nil
	}

	return resp, nil
}

// translateRequest converts OpenAI chat completion request to Gemini Cloud Code format
func (e *Executor) translateRequest(model string, body []byte, stream bool, creds *db.AGCredentials) ([]byte, error) {
	var openaiReq OpenAIRequest
	if err := json.Unmarshal(body, &openaiReq); err != nil {
		return nil, fmt.Errorf("parse openai request: %w", err)
	}

	// Build Gemini contents from OpenAI messages
	contents := []GeminiContent{}
	var systemParts []GeminiPart

	for _, msg := range openaiReq.Messages {
		switch msg.Role {
		case "system":
			text := extractTextContent(msg.Content)
			if text != "" {
				systemParts = append(systemParts, GeminiPart{Text: &text})
			}

		case "user":
			parts := convertContentToParts(msg.Content)
			if len(parts) > 0 {
				contents = append(contents, GeminiContent{Role: "user", Parts: parts})
			}

		case "assistant":
			parts := []GeminiPart{}
			text := extractTextContent(msg.Content)
			if text != "" {
				parts = append(parts, GeminiPart{Text: &text})
			}
			// Handle tool calls
			if len(msg.ToolCalls) > 0 {
				for _, tc := range msg.ToolCalls {
					args := map[string]interface{}{}
					json.Unmarshal([]byte(tc.Function.Arguments), &args)
					parts = append(parts, GeminiPart{
						FunctionCall: &GeminiFunctionCall{
							Name: sanitizeFunctionName(tc.Function.Name),
							Args: args,
						},
					})
				}
			}
			if len(parts) > 0 {
				contents = append(contents, GeminiContent{Role: "model", Parts: parts})
			}

		case "tool":
			// Tool response → functionResponse
			var result interface{}
			json.Unmarshal([]byte(extractTextContent(msg.Content)), &result)
			if result == nil {
				result = extractTextContent(msg.Content)
			}
			name := "tool"
			if msg.Name != "" {
				name = sanitizeFunctionName(msg.Name)
			}
			parts := []GeminiPart{{
				FunctionResponse: &GeminiFunctionResponse{
					Name:     name,
					Response: map[string]interface{}{"result": result},
				},
			}}
			contents = append(contents, GeminiContent{Role: "user", Parts: parts})
		}
	}

	// Build tools (functionDeclarations)
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

	// Build generation config
	genConfig := GeminiGenerationConfig{}
	if openaiReq.MaxTokens > 0 {
		genConfig.MaxOutputTokens = openaiReq.MaxTokens
	} else {
		genConfig.MaxOutputTokens = maxOutputTokens
	}
	if openaiReq.Temperature != nil {
		genConfig.Temperature = openaiReq.Temperature
	}
	if openaiReq.TopP != nil {
		genConfig.TopP = openaiReq.TopP
	}

	// Map reasoning_effort to thinkingConfig.thinkingBudget
	if openaiReq.ReasoningEffort != "" {
		budget := 0
		switch openaiReq.ReasoningEffort {
		case "low":
			budget = 2048
		case "medium":
			budget = 8192
		case "high":
			budget = 32768
		case "max":
			budget = 65536
		case "none":
			// Explicitly disable thinking — don't set config
		case "auto":
			// Let model decide — set budget to -1 (auto)
			budget = -1
		default:
			// Try parse as numeric (direct budget from DSL)
			fmt.Sscanf(openaiReq.ReasoningEffort, "%d", &budget)
		}
		if budget > 0 {
			genConfig.ThinkingConfig = &GeminiThinkingConfig{
				ThinkingBudget:  budget,
				IncludeThoughts: true,
			}
		} else if budget == -1 {
			// Auto mode — include thoughts but let model decide budget
			genConfig.ThinkingConfig = &GeminiThinkingConfig{
				IncludeThoughts: true,
			}
		}
	}

	// Build Cloud Code envelope
	projectID := creds.ProjectID
	if projectID == "" {
		projectID = generateProjectID()
	}

	envelope := CloudCodeRequest{
		Project:     projectID,
		Model:       model,
		UserAgent:   "antigravity",
		RequestType: "agent",
		RequestID:   "agent-" + uuid.New().String(),
		Request: GeminiRequest{
			Contents:         contents,
			GenerationConfig: genConfig,
			SessionID:        generateSessionID(creds.ProjectID),
		},
	}

	// System instruction (user's only, no injection)
	if len(systemParts) > 0 {
		envelope.Request.SystemInstruction = &GeminiSystemInstruction{
			Role:  "user",
			Parts: systemParts,
		}
	}

	// Tools
	if len(tools) > 0 {
		envelope.Request.Tools = tools
		envelope.Request.ToolConfig = &GeminiToolConfig{
			FunctionCallingConfig: GeminiFunctionCallingConfig{Mode: "AUTO"},
		}
	}

	return json.Marshal(envelope)
}

// translateStreamingResponse wraps the upstream SSE response and translates Gemini SSE → OpenAI SSE
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

				// Process complete SSE lines
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
				// Send [DONE]
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

// translateResponse translates non-streaming Gemini response to OpenAI format
func (e *Executor) translateResponse(resp *http.Response) (*http.Response, error) {
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var geminiResp GeminiResponse
	if err := json.Unmarshal(body, &geminiResp); err != nil {
		// Return raw if can't parse
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

// --- Helpers ---

func generateSessionID(seed string) string {
	return uuid.New().String() + fmt.Sprintf("%d", time.Now().UnixMilli())
}

func generateProjectID() string {
	adjs := []string{"useful", "bright", "swift", "calm", "bold"}
	nouns := []string{"fuze", "wave", "spark", "flow", "core"}
	b := make([]byte, 3)
	rand.Read(b)
	adj := adjs[int(b[0])%len(adjs)]
	noun := nouns[int(b[1])%len(nouns)]
	return fmt.Sprintf("%s-%s-%s", adj, noun, uuid.New().String()[:5])
}

func sanitizeFunctionName(name string) string {
	if name == "" {
		return "_unknown"
	}
	// Gemini requires: [a-zA-Z_][a-zA-Z0-9_.:−]{0,63}
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
