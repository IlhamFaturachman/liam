package kiro

import (
	"encoding/json"
	"strings"

	"github.com/google/uuid"
)

// translateRequest converts OpenAI chat completion to Kiro AWS CodeWhisperer format
func translateRequest(model string, body []byte, profileARN string) ([]byte, error) {
	var openaiReq OpenAIRequest
	if err := json.Unmarshal(body, &openaiReq); err != nil {
		return nil, err
	}

	// Strip "kr/" or "kiro/" prefix
	upstreamModel := strings.TrimPrefix(model, "kr/")
	upstreamModel = strings.TrimPrefix(upstreamModel, "kiro/")

	// Build conversation state
	convState := ConversationState{
		ChatTriggerType: "MANUAL",
		ConversationID:  uuid.New().String(),
	}

	// Convert messages → history + currentMessage
	// Last message becomes currentMessage, rest becomes history
	if len(openaiReq.Messages) == 0 {
		return nil, nil
	}

	// Build history from all messages except the last
	systemContent := ""
	historyMsgs := []OpenAIMessage{}

	for _, msg := range openaiReq.Messages {
		if msg.Role == "system" {
			text := extractText(msg.Content)
			if systemContent != "" {
				systemContent += "\n\n"
			}
			systemContent += text
			continue
		}
		historyMsgs = append(historyMsgs, msg)
	}

	// Build history (alternating user/assistant)
	history := []ChatMessage{}
	for i := 0; i < len(historyMsgs)-1; i++ {
		msg := historyMsgs[i]
		if msg.Role == "user" || msg.Role == "tool" {
			content := extractText(msg.Content)
			if msg.Role == "tool" {
				// Convert tool message to user with tool_result formatting
				content = "Tool result for " + msg.ToolCallID + ": " + content
			}
			history = append(history, ChatMessage{
				UserInputMessage: &UserInputMessage{
					Content: content,
					ModelID: upstreamModel,
					Origin:  "AI_EDITOR",
				},
			})
		} else if msg.Role == "assistant" {
			content := extractText(msg.Content)
			toolUses := []KiroToolUse{}
			for _, tc := range msg.ToolCalls {
				args := map[string]interface{}{}
				json.Unmarshal([]byte(tc.Function.Arguments), &args)
				toolUses = append(toolUses, KiroToolUse{
					ToolUseID: tc.ID,
					Name:      tc.Function.Name,
					Input:     args,
				})
			}
			history = append(history, ChatMessage{
				AssistantResponseMessage: &AssistantResponseMessage{
					Content:  content,
					ToolUses: toolUses,
				},
			})
		}
	}

	// Build currentMessage from last message
	lastMsg := historyMsgs[len(historyMsgs)-1]
	currentContent := extractText(lastMsg.Content)

	// Prepend system content + timestamp to current message
	prefix := ""
	if systemContent != "" {
		prefix = systemContent + "\n\n"
	}
	prefix += "[Current time: " + currentTimestamp() + "]\n\n"
	currentContent = prefix + currentContent

	// Build user input message context (tools)
	var userCtx *UserInputMessageContext
	if len(openaiReq.Tools) > 0 {
		toolSpecs := []KiroToolSpec{}
		for _, t := range openaiReq.Tools {
			if t.Type != "function" {
				continue
			}
			toolSpecs = append(toolSpecs, KiroToolSpec{
				ToolSpecification: ToolSpecification{
					Name:        t.Function.Name,
					Description: t.Function.Description,
					InputSchema: &ToolInputSchema{JSON: t.Function.Parameters},
				},
			})
		}
		if len(toolSpecs) > 0 {
			userCtx = &UserInputMessageContext{
				Tools: toolSpecs,
				EditorState: &EditorState{CursorState: map[string]interface{}{}},
			}
		}
	}

	// Handle tool results in last message
	if lastMsg.Role == "tool" {
		// Tool result message
		if userCtx == nil {
			userCtx = &UserInputMessageContext{
				EditorState: &EditorState{CursorState: map[string]interface{}{}},
			}
		}
		userCtx.ToolResults = []KiroToolResult{
			{
				ToolUseID: lastMsg.ToolCallID,
				Status:    "success",
				Content: []KiroToolResultContent{
					{Text: extractText(lastMsg.Content)},
				},
			},
		}
		currentContent = ""
	}

	convState.CurrentMessage = ChatMessage{
		UserInputMessage: &UserInputMessage{
			Content:                 currentContent,
			ModelID:                 upstreamModel,
			Origin:                  "AI_EDITOR",
			UserInputMessageContext: userCtx,
		},
	}
	convState.History = history

	// Inference config
	infCfg := &InferenceConfig{}
	if openaiReq.MaxTokens > 0 {
		infCfg.MaxTokens = openaiReq.MaxTokens
	} else {
		infCfg.MaxTokens = 32000
	}
	if openaiReq.Temperature != nil {
		infCfg.Temperature = openaiReq.Temperature
	}
	if openaiReq.TopP != nil {
		infCfg.TopP = openaiReq.TopP
	}

	kiroReq := KiroRequest{
		ConversationState: convState,
		ProfileArn:        profileARN,
		InferenceConfig:   infCfg,
	}

	return json.Marshal(kiroReq)
}

func extractText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	// Try string
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
