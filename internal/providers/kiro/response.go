package kiro

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
)

// EventStream frame parser + OpenAI SSE translator
//
// AWS EventStream binary frame format:
//   [4 bytes: total length (big endian)]
//   [4 bytes: headers length (big endian)]
//   [4 bytes: prelude CRC32]
//   [N bytes: typed headers]
//   [N bytes: payload (JSON)]
//   [4 bytes: message CRC32]
//
// We don't validate CRC (best-effort parsing).
//
// Headers contain :event-type which tells us what kind of event this is:
//   - assistantResponseEvent → text content delta
//   - codeEvent → code content delta
//   - toolUseEvent → tool call delta
//   - messageStopEvent → finish
//   - contextUsageEvent → token usage info
//   - meteringEvent → ignore
//   - metricsEvent → ignore

type eventFrame struct {
	EventType string
	Payload   []byte
}

// translateStreamingResponse takes Kiro binary EventStream response and pipes OpenAI SSE chunks
func translateStreamingResponse(body io.ReadCloser, model string) io.ReadCloser {
	pr, pw := io.Pipe()

	go func() {
		defer pw.Close()
		defer body.Close()

		state := &streamState{
			model:    model,
			id:       "chatcmpl-" + uuid.New().String()[:8],
			created:  time.Now().Unix(),
			toolCalls: map[string]*toolCallAccum{},
		}

		// Send initial role chunk
		role := "assistant"
		emitChunk(pw, state, &OpenAIMsg{Role: role}, nil)

		for {
			frame, err := readEventStreamFrame(body)
			if err != nil {
				if err != io.EOF {
					// Best effort — flush what we have
				}
				// Send finish + DONE
				stopReason := "stop"
				emitChunk(pw, state, nil, &stopReason)
				fmt.Fprintf(pw, "data: [DONE]\n\n")
				return
			}

			handleFrame(pw, state, frame)
		}
	}()

	return pr
}

type streamState struct {
	model     string
	id        string
	created   int64
	toolCalls map[string]*toolCallAccum
	usage     *OpenAIUsage
}

type toolCallAccum struct {
	id   string
	name string
	args string
}

func handleFrame(w io.Writer, state *streamState, frame *eventFrame) {
	switch frame.EventType {
	case "assistantResponseEvent":
		var ev struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(frame.Payload, &ev); err == nil && ev.Content != "" {
			emitChunk(w, state, &OpenAIMsg{Content: &ev.Content}, nil)
		}

	case "codeEvent":
		var ev struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(frame.Payload, &ev); err == nil && ev.Content != "" {
			emitChunk(w, state, &OpenAIMsg{Content: &ev.Content}, nil)
		}

	case "toolUseEvent":
		var ev struct {
			ToolUseID string `json:"toolUseId"`
			Name      string `json:"name"`
			Input     string `json:"input"`
			Stop      bool   `json:"stop"`
		}
		if err := json.Unmarshal(frame.Payload, &ev); err != nil {
			return
		}
		acc, ok := state.toolCalls[ev.ToolUseID]
		if !ok {
			acc = &toolCallAccum{id: ev.ToolUseID, name: ev.Name}
			state.toolCalls[ev.ToolUseID] = acc
			// Emit tool_call start
			emitChunk(w, state, &OpenAIMsg{
				ToolCalls: []OpenAIToolCall{{
					ID:   ev.ToolUseID,
					Type: "function",
					Function: OpenAIFunctionCall{Name: ev.Name},
				}},
			}, nil)
		}
		if ev.Input != "" {
			acc.args += ev.Input
			emitChunk(w, state, &OpenAIMsg{
				ToolCalls: []OpenAIToolCall{{
					ID:   ev.ToolUseID,
					Type: "function",
					Function: OpenAIFunctionCall{Arguments: ev.Input},
				}},
			}, nil)
		}

	case "messageStopEvent":
		// Done
		stopReason := "stop"
		if len(state.toolCalls) > 0 {
			stopReason = "tool_calls"
		}
		emitChunk(w, state, nil, &stopReason)

	case "contextUsageEvent":
		var ev struct {
			ContextUsagePercentage float64 `json:"contextUsagePercentage"`
		}
		if err := json.Unmarshal(frame.Payload, &ev); err == nil {
			// Estimate tokens from context usage % (200k window assumption)
			used := int(ev.ContextUsagePercentage / 100 * 200000)
			state.usage = &OpenAIUsage{
				PromptTokens: used,
				TotalTokens:  used,
			}
		}
	}
}

func emitChunk(w io.Writer, state *streamState, delta *OpenAIMsg, finishReason *string) {
	chunk := OpenAIStreamChunk{
		ID:      state.id,
		Object:  "chat.completion.chunk",
		Created: state.created,
		Model:   state.model,
		Choices: []OpenAIChoice{{
			Index:        0,
			Delta:        delta,
			FinishReason: finishReason,
		}},
	}
	if finishReason != nil && state.usage != nil {
		chunk.Usage = state.usage
	}

	data, _ := json.Marshal(chunk)
	fmt.Fprintf(w, "data: %s\n\n", data)
	if f, ok := w.(interface{ Flush() }); ok {
		f.Flush()
	}
}

// readEventStreamFrame reads one AWS EventStream binary frame
func readEventStreamFrame(r io.Reader) (*eventFrame, error) {
	// Read prelude: 4 + 4 + 4 = 12 bytes
	var prelude [12]byte
	if _, err := io.ReadFull(r, prelude[:]); err != nil {
		return nil, err
	}

	totalLen := binary.BigEndian.Uint32(prelude[0:4])
	headersLen := binary.BigEndian.Uint32(prelude[4:8])
	// preludeCRC := binary.BigEndian.Uint32(prelude[8:12]) // skip validation

	if totalLen < 16 || totalLen > 16*1024*1024 {
		return nil, fmt.Errorf("invalid frame length: %d", totalLen)
	}

	// Read headers + payload + message CRC
	remaining := totalLen - 12 // already read prelude
	rest := make([]byte, remaining)
	if _, err := io.ReadFull(r, rest); err != nil {
		return nil, err
	}

	headers := rest[0:headersLen]
	payloadLen := remaining - headersLen - 4 // -4 for message CRC at end
	payload := rest[headersLen : headersLen+payloadLen]

	// Parse headers to find :event-type
	eventType := parseEventType(headers)

	return &eventFrame{
		EventType: eventType,
		Payload:   payload,
	}, nil
}

// parseEventType extracts :event-type from headers (simplified parser)
// AWS EventStream header format:
//   [1 byte: name length]
//   [N bytes: name]
//   [1 byte: type] (7 = string)
//   [2 bytes: value length]
//   [N bytes: value]
func parseEventType(headers []byte) string {
	i := 0
	for i < len(headers) {
		if i+1 > len(headers) {
			break
		}
		nameLen := int(headers[i])
		i++
		if i+nameLen > len(headers) {
			break
		}
		name := string(headers[i : i+nameLen])
		i += nameLen

		if i+1 > len(headers) {
			break
		}
		valType := headers[i]
		i++

		// Type 7 = string
		if valType == 7 {
			if i+2 > len(headers) {
				break
			}
			valLen := int(binary.BigEndian.Uint16(headers[i : i+2]))
			i += 2
			if i+valLen > len(headers) {
				break
			}
			value := string(headers[i : i+valLen])
			i += valLen
			if name == ":event-type" {
				return value
			}
		} else {
			// Skip other types (best effort — not exhaustive)
			break
		}
	}
	return ""
}

// translateNonStreamingResponse for completeness (Kiro is always streaming, but just in case)
func translateNonStreamingResponse(body io.ReadCloser, model string) ([]byte, error) {
	defer body.Close()
	// For now, just return empty
	return json.Marshal(map[string]interface{}{
		"id":      "chatcmpl-" + uuid.New().String()[:8],
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []interface{}{},
	})
}
