package antigravity

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
)

// --- OpenAI Types ---

type OpenAIRequest struct {
	Model           string          `json:"model"`
	Messages        []OpenAIMessage `json:"messages"`
	Tools           []OpenAITool    `json:"tools,omitempty"`
	Stream          bool            `json:"stream,omitempty"`
	MaxTokens       int             `json:"max_tokens,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	TopP            *float64        `json:"top_p,omitempty"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
}

type OpenAIMessage struct {
	Role             string           `json:"role"`
	Content          json.RawMessage  `json:"content"`
	Name             string           `json:"name,omitempty"`
	ReasoningContent *string          `json:"reasoning_content,omitempty"`
	ToolCalls        []OpenAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
}

type OpenAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function OpenAIFunctionCall `json:"function"`
}

type OpenAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type OpenAITool struct {
	Type     string         `json:"type"`
	Function OpenAIFunction `json:"function"`
}

type OpenAIFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
}

type OpenAIResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []OpenAIChoice `json:"choices"`
	Usage   *OpenAIUsage   `json:"usage,omitempty"`
}

type OpenAIChoice struct {
	Index        int        `json:"index"`
	Message      *OpenAIMsg `json:"message,omitempty"`
	Delta        *OpenAIMsg `json:"delta,omitempty"`
	FinishReason *string    `json:"finish_reason"`
}

type OpenAIMsg struct {
	Role             string           `json:"role,omitempty"`
	Content          *string          `json:"content,omitempty"`
	ToolCalls        []OpenAIToolCall `json:"tool_calls,omitempty"`
	ReasoningContent *string          `json:"reasoning_content,omitempty"`
}

type OpenAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// --- Streaming chunk ---

type OpenAIStreamChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []OpenAIChoice `json:"choices"`
	Usage   *OpenAIUsage   `json:"usage,omitempty"`
}

// --- Gemini Types ---

type CloudCodeRequest struct {
	Project     string        `json:"project"`
	Model       string        `json:"model"`
	UserAgent   string        `json:"userAgent"`
	RequestType string        `json:"requestType"`
	RequestID   string        `json:"requestId"`
	Request     GeminiRequest `json:"request"`
}

type GeminiRequest struct {
	Contents          []GeminiContent          `json:"contents"`
	SystemInstruction *GeminiSystemInstruction `json:"systemInstruction,omitempty"`
	GenerationConfig  GeminiGenerationConfig   `json:"generationConfig"`
	Tools             []GeminiToolGroup        `json:"tools,omitempty"`
	ToolConfig        *GeminiToolConfig        `json:"toolConfig,omitempty"`
	SessionID         string                   `json:"sessionId"`
}

type GeminiContent struct {
	Role  string       `json:"role"`
	Parts []GeminiPart `json:"parts"`
}

type GeminiPart struct {
	Text             *string                 `json:"text,omitempty"`
	Thought          *bool                   `json:"thought,omitempty"`
	ThoughtSignature *string                 `json:"thoughtSignature,omitempty"`
	FunctionCall     *GeminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *GeminiFunctionResponse `json:"functionResponse,omitempty"`
	InlineData       *GeminiInlineData       `json:"inlineData,omitempty"`
}

type GeminiFunctionCall struct {
	ID   string                 `json:"id,omitempty"`
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args"`
}

type GeminiFunctionResponse struct {
	ID       string                 `json:"id,omitempty"`
	Name     string                 `json:"name"`
	Response map[string]interface{} `json:"response"`
}

type GeminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type GeminiSystemInstruction struct {
	Role  string       `json:"role"`
	Parts []GeminiPart `json:"parts"`
}

type GeminiGenerationConfig struct {
	MaxOutputTokens int                   `json:"maxOutputTokens,omitempty"`
	Temperature     *float64              `json:"temperature,omitempty"`
	TopP            *float64              `json:"topP,omitempty"`
	ThinkingConfig  *GeminiThinkingConfig `json:"thinkingConfig,omitempty"`
}

type GeminiThinkingConfig struct {
	ThinkingBudget  int    `json:"thinkingBudget,omitempty"`
	ThinkingLevel   string `json:"thinkingLevel,omitempty"`
	IncludeThoughts bool   `json:"includeThoughts,omitempty"`
}

type GeminiToolGroup struct {
	FunctionDeclarations []GeminiFunctionDeclaration `json:"functionDeclarations"`
}

type GeminiFunctionDeclaration struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
}

type GeminiToolConfig struct {
	FunctionCallingConfig GeminiFunctionCallingConfig `json:"functionCallingConfig"`
}

type GeminiFunctionCallingConfig struct {
	Mode string `json:"mode"`
}

// --- Gemini Response Types ---

type GeminiResponse struct {
	Response *GeminiResponseBody `json:"response,omitempty"`
	// Top-level for non-wrapped responses
	Candidates    []GeminiCandidate `json:"candidates,omitempty"`
	UsageMetadata *GeminiUsage      `json:"usageMetadata,omitempty"`
	ModelVersion  string            `json:"modelVersion,omitempty"`
}

type GeminiResponseBody struct {
	Candidates    []GeminiCandidate `json:"candidates,omitempty"`
	UsageMetadata *GeminiUsage      `json:"usageMetadata,omitempty"`
	ModelVersion  string            `json:"modelVersion,omitempty"`
	ResponseID    string            `json:"responseId,omitempty"`
}

type GeminiCandidate struct {
	Content      GeminiContent `json:"content"`
	FinishReason string        `json:"finishReason,omitempty"`
}

type GeminiUsage struct {
	PromptTokenCount        int `json:"promptTokenCount,omitempty"`
	CandidatesTokenCount    int `json:"candidatesTokenCount,omitempty"`
	TotalTokenCount         int `json:"totalTokenCount,omitempty"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount,omitempty"`
	CachedContentTokenCount int `json:"cachedContentTokenCount,omitempty"`
}

// --- Translation Functions ---

// translateSSEChunk converts a single Gemini SSE data line to OpenAI SSE format
func translateSSEChunk(data string) string {
	if data == "" || data == "[DONE]" {
		return "[DONE]"
	}

	var gemini struct {
		Response *GeminiResponseBody `json:"response,omitempty"`
		// Direct candidates (some responses)
		Candidates    []GeminiCandidate `json:"candidates,omitempty"`
		UsageMetadata *GeminiUsage      `json:"usageMetadata,omitempty"`
	}

	if err := json.Unmarshal([]byte(data), &gemini); err != nil {
		return "" // Skip unparseable chunks
	}

	// Get candidates from either wrapper or direct
	candidates := gemini.Candidates
	usage := gemini.UsageMetadata
	if gemini.Response != nil {
		candidates = gemini.Response.Candidates
		usage = gemini.Response.UsageMetadata
	}

	if len(candidates) == 0 {
		return ""
	}

	candidate := candidates[0]
	chunk := OpenAIStreamChunk{
		ID:      "chatcmpl-" + uuid.New().String()[:8],
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   "ag-model",
		Choices: []OpenAIChoice{},
	}

	choice := OpenAIChoice{Index: 0, Delta: &OpenAIMsg{}}

	// Process parts
	for _, part := range candidate.Content.Parts {
		// Thinking/reasoning
		if part.Thought != nil && *part.Thought && part.Text != nil {
			choice.Delta.ReasoningContent = part.Text
			continue
		}

		// Text content
		if part.Text != nil {
			choice.Delta.Content = part.Text
		}

		// Function call
		if part.FunctionCall != nil {
			args, _ := json.Marshal(part.FunctionCall.Args)
			choice.Delta.ToolCalls = append(choice.Delta.ToolCalls, OpenAIToolCall{
				ID:   "call_" + uuid.New().String()[:8],
				Type: "function",
				Function: OpenAIFunctionCall{
					Name:      part.FunctionCall.Name,
					Arguments: string(args),
				},
			})
		}
	}

	// Finish reason
	if candidate.FinishReason != "" {
		reason := mapFinishReason(candidate.FinishReason)
		choice.FinishReason = &reason
	}

	// Set role on first chunk
	if choice.Delta.Content != nil || choice.Delta.ToolCalls != nil || choice.Delta.ReasoningContent != nil {
		role := "assistant"
		choice.Delta.Role = role
	}

	chunk.Choices = append(chunk.Choices, choice)

	// Usage
	if usage != nil {
		chunk.Usage = &OpenAIUsage{
			PromptTokens:     usage.PromptTokenCount,
			CompletionTokens: usage.CandidatesTokenCount,
			TotalTokens:      usage.TotalTokenCount,
		}
	}

	result, _ := json.Marshal(chunk)
	return string(result)
}

// translateGeminiToOpenAI converts full Gemini response to OpenAI format
func translateGeminiToOpenAI(gemini *GeminiResponse) *OpenAIResponse {
	candidates := gemini.Candidates
	usage := gemini.UsageMetadata
	if gemini.Response != nil {
		candidates = gemini.Response.Candidates
		usage = gemini.Response.UsageMetadata
	}

	resp := &OpenAIResponse{
		ID:      "chatcmpl-" + uuid.New().String()[:8],
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   "ag-model",
		Choices: []OpenAIChoice{},
	}

	if len(candidates) > 0 {
		candidate := candidates[0]
		msg := &OpenAIMsg{Role: "assistant"}

		var textParts []string
		for _, part := range candidate.Content.Parts {
			if part.Text != nil {
				if part.Thought != nil && *part.Thought {
					msg.ReasoningContent = part.Text
				} else {
					textParts = append(textParts, *part.Text)
				}
			}
			if part.FunctionCall != nil {
				args, _ := json.Marshal(part.FunctionCall.Args)
				msg.ToolCalls = append(msg.ToolCalls, OpenAIToolCall{
					ID:   "call_" + uuid.New().String()[:8],
					Type: "function",
					Function: OpenAIFunctionCall{
						Name:      part.FunctionCall.Name,
						Arguments: string(args),
					},
				})
			}
		}

		if len(textParts) > 0 {
			joined := strings.Join(textParts, "")
			msg.Content = &joined
		}

		reason := mapFinishReason(candidate.FinishReason)
		resp.Choices = append(resp.Choices, OpenAIChoice{
			Index:        0,
			Message:      msg,
			FinishReason: &reason,
		})
	}

	if usage != nil {
		resp.Usage = &OpenAIUsage{
			PromptTokens:     usage.PromptTokenCount,
			CompletionTokens: usage.CandidatesTokenCount,
			TotalTokens:      usage.TotalTokenCount,
		}
	}

	return resp
}

// --- Helpers ---

func mapFinishReason(geminiReason string) string {
	switch geminiReason {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY":
		return "content_filter"
	default:
		return "stop"
	}
}

func extractTextContent(content json.RawMessage) string {
	// Try string first
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return s
	}

	// Try array of content parts
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &parts); err == nil {
		var texts []string
		for _, p := range parts {
			if p.Type == "text" && p.Text != "" {
				texts = append(texts, p.Text)
			}
		}
		return strings.Join(texts, "\n")
	}

	return ""
}

func convertContentToParts(content json.RawMessage) []GeminiPart {
	parts := []GeminiPart{}

	// Try string
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		if s != "" {
			parts = append(parts, GeminiPart{Text: &s})
		}
		return parts
	}

	// Try array of content parts
	var contentParts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL *struct {
			URL string `json:"url"`
		} `json:"image_url,omitempty"`
	}
	if err := json.Unmarshal(content, &contentParts); err == nil {
		for _, p := range contentParts {
			switch p.Type {
			case "text":
				if p.Text != "" {
					text := p.Text
					parts = append(parts, GeminiPart{Text: &text})
				}
			case "image_url":
				if p.ImageURL != nil && strings.HasPrefix(p.ImageURL.URL, "data:") {
					// Parse data URI: data:image/png;base64,xxxxx
					dataURI := p.ImageURL.URL
					commaIdx := strings.Index(dataURI, ",")
					if commaIdx > 0 {
						mimeType := strings.TrimPrefix(dataURI[5:commaIdx], "")
						mimeType = strings.TrimSuffix(mimeType, ";base64")
						data := dataURI[commaIdx+1:]
						parts = append(parts, GeminiPart{
							InlineData: &GeminiInlineData{
								MimeType: mimeType,
								Data:     data,
							},
						})
					}
				}
			}
		}
	}

	return parts
}
