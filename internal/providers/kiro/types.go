package kiro

import (
	"encoding/json"
	"time"
)

// KiroCredentials stored for each Kiro account
type KiroCredentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    string `json:"expires_at"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	Region       string `json:"region"`
	ProfileARN   string `json:"profile_arn,omitempty"`
}

// --- OpenAI Types (request) ---

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
	Role       string           `json:"role"`
	Content    json.RawMessage  `json:"content"`
	Name       string           `json:"name,omitempty"`
	ToolCalls  []OpenAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
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

// --- OpenAI SSE chunk ---

type OpenAIStreamChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []OpenAIChoice `json:"choices"`
	Usage   *OpenAIUsage   `json:"usage,omitempty"`
}

type OpenAIChoice struct {
	Index        int        `json:"index"`
	Delta        *OpenAIMsg `json:"delta,omitempty"`
	Message      *OpenAIMsg `json:"message,omitempty"`
	FinishReason *string    `json:"finish_reason"`
}

type OpenAIMsg struct {
	Role             string           `json:"role,omitempty"`
	Content          *string          `json:"content,omitempty"`
	ReasoningContent *string          `json:"reasoning_content,omitempty"`
	ToolCalls        []OpenAIToolCall `json:"tool_calls,omitempty"`
}

type OpenAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// --- Kiro (AWS CodeWhisperer) Request Types ---

type KiroRequest struct {
	ConversationState ConversationState `json:"conversationState"`
	ProfileArn        string            `json:"profileArn,omitempty"`
	InferenceConfig   *InferenceConfig  `json:"inferenceConfig,omitempty"`
}

type ConversationState struct {
	ChatTriggerType string        `json:"chatTriggerType"`
	ConversationID  string        `json:"conversationId"`
	CurrentMessage  ChatMessage   `json:"currentMessage"`
	History         []ChatMessage `json:"history,omitempty"`
}

type ChatMessage struct {
	UserInputMessage         *UserInputMessage         `json:"userInputMessage,omitempty"`
	AssistantResponseMessage *AssistantResponseMessage `json:"assistantResponseMessage,omitempty"`
}

type UserInputMessage struct {
	Content                 string                   `json:"content"`
	ModelID                 string                   `json:"modelId"`
	Origin                  string                   `json:"origin"`
	UserInputMessageContext *UserInputMessageContext `json:"userInputMessageContext,omitempty"`
	UserIntent              string                   `json:"userIntent,omitempty"`
	Images                  []KiroImage              `json:"images,omitempty"`
}

// KiroImage matches the AWS CodeWhisperer image attachment shape:
//
//	{ "format": "png", "source": { "bytes": "<base64>" } }
//
// "format" is the lowercase media subtype (png, jpeg, gif, webp). The bytes
// MUST be raw base64 — no `data:...;base64,` prefix. We also enforce a 5 MB
// post-base64 ceiling per image because the upstream rejects payloads
// larger than that with an opaque "InvalidRequest" error.
type KiroImage struct {
	Format string          `json:"format"`
	Source KiroImageSource `json:"source"`
}

type KiroImageSource struct {
	Bytes string `json:"bytes"`
}

type UserInputMessageContext struct {
	Tools       []KiroToolSpec   `json:"tools,omitempty"`
	ToolResults []KiroToolResult `json:"toolResults,omitempty"`
	EditorState *EditorState     `json:"editorState,omitempty"`
}

type EditorState struct {
	CursorState map[string]interface{} `json:"cursorState"`
}

type KiroToolSpec struct {
	ToolSpecification ToolSpecification `json:"toolSpecification"`
}

type ToolSpecification struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	InputSchema *ToolInputSchema `json:"inputSchema,omitempty"`
}

type ToolInputSchema struct {
	JSON map[string]interface{} `json:"json"`
}

type KiroToolResult struct {
	ToolUseID string                  `json:"toolUseId"`
	Status    string                  `json:"status"`
	Content   []KiroToolResultContent `json:"content"`
}

type KiroToolResultContent struct {
	Text string                 `json:"text,omitempty"`
	JSON map[string]interface{} `json:"json,omitempty"`
}

type AssistantResponseMessage struct {
	Content   string        `json:"content"`
	MessageID string        `json:"messageId,omitempty"`
	ToolUses  []KiroToolUse `json:"toolUses,omitempty"`
}

type KiroToolUse struct {
	ToolUseID string                 `json:"toolUseId"`
	Name      string                 `json:"name"`
	Input     map[string]interface{} `json:"input"`
}

type InferenceConfig struct {
	MaxTokens   int      `json:"maxTokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"topP,omitempty"`
}

// --- Helpers ---

func currentTimestamp() string {
	return time.Now().UTC().Format(time.RFC3339)
}
