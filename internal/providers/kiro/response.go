package kiro

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strings"
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
			model:     model,
			id:        "chatcmpl-" + uuid.New().String()[:8],
			created:   time.Now().Unix(),
			toolCalls: map[string]*toolCallAccum{},
		}

		// Send initial role chunk
		role := "assistant"
		emitChunk(pw, state, &OpenAIMsg{Role: role}, nil)

		for {
			frame, err := readEventStreamFrame(body)
			if err != nil {
				// Determine the right finish reason. If we end the
				// stream while a tool call is still mid-flight (we
				// never saw its terminating Stop=true frame, or its
				// accumulated arguments don't parse as valid JSON),
				// surface "length" so consumers know the payload is
				// truncated rather than complete. This prevents
				// callers like OpenCode from feeding a half-formed
				// `{"filePath": "..."` into JSON.parse and exploding
				// with the dreaded "Expected '}'" error.
				stopReason := "stop"
				if state.activeToolID != "" {
					stopReason = "length"
				} else {
					for _, acc := range state.toolCalls {
						if !isCompleteJSON(acc.args) {
							stopReason = "length"
							break
						}
					}
				}
				if len(state.toolCalls) > 0 && stopReason == "stop" {
					stopReason = "tool_calls"
				}
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

	// activeToolID is the toolUseId of the most recent toolUseEvent
	// that arrived with one. Kiro's upstream emits the start chunk
	// with the full {toolUseId, name, input_chunk_1} envelope, then
	// streams the rest of the JSON arguments as bare {input: "..."}
	// chunks (no toolUseId). Without this fallback we drop every tail
	// chunk and OpenCode receives a half-formed JSON like
	//   {"filePath": "/path/to/file.md"
	// which then fails its own JSON.parse with "Expected '}'".
	activeToolID string

	// Inline thinking-block stripper. When `<thinking_mode>enabled</thinking_mode>`
	// is injected into the user content, Opus 4.6 / Sonnet 4.5 / Sonnet
	// 4.6 produce their reasoning trace as `<thinking>…</thinking>`
	// blocks INSIDE the regular `assistantResponseEvent` content stream
	// — they don't emit a separate `reasoningContentEvent` like Opus 4.7
	// or DeepSeek do. Without this we'd leak the raw `<thinking>` tags
	// to the end user. The state machine below routes everything inside
	// a `<thinking>` block to `reasoning_content` and the rest to
	// regular `content`, with carry-over for partial tag matches at
	// chunk boundaries.
	insideThinking bool
	thinkingCarry  string // partial "<thinking" or "</thinking" match
	contentCarry   string // partial "<thinking" outside a block

	// outputCharCount tracks the total characters of assistant content
	// we forwarded downstream, so we can synthesize a completion-tokens
	// estimate when Kiro's metricsEvent reports outputTokens=0 (a known
	// upstream quirk for short replies and certain models like Opus 4.7).
	// We use a 4 chars/token rule of thumb that matches OpenAI's tokenizer
	// closely enough for usage tracking purposes.
	outputCharCount int
}

type toolCallAccum struct {
	id   string
	name string
	args string
}

func handleFrame(w io.Writer, state *streamState, frame *eventFrame) {
	switch frame.EventType {
	case "assistantResponseEvent":
		// Payload may be flat ({content: "..."}) or nested
		// ({assistantResponseEvent: {content: "..."}}) depending on the
		// upstream model. Try both shapes.
		var ev struct {
			Content                string `json:"content"`
			AssistantResponseEvent *struct {
				Content string `json:"content"`
			} `json:"assistantResponseEvent"`
		}
		if err := json.Unmarshal(frame.Payload, &ev); err != nil {
			return
		}
		content := ev.Content
		if content == "" && ev.AssistantResponseEvent != nil {
			content = ev.AssistantResponseEvent.Content
		}
		if content != "" {
			emitAssistantText(w, state, content)
		}

	case "reasoningContentEvent":
		// Thinking/reasoning content emitted by Opus 4.7, DeepSeek,
		// GLM-5, etc. before the main answer. Forward as
		// reasoning_content (matches OpenAI thinking conventions) and
		// also surface it as visible content so the test probe sees
		// at least one delta — without this Opus 4.7 looked "failed"
		// because nothing was streamed before metering events.
		var ev struct {
			Content               string `json:"content"`
			ReasoningContentEvent *struct {
				Content string `json:"content"`
			} `json:"reasoningContentEvent"`
		}
		if err := json.Unmarshal(frame.Payload, &ev); err != nil {
			return
		}
		content := ev.Content
		if content == "" && ev.ReasoningContentEvent != nil {
			content = ev.ReasoningContentEvent.Content
		}
		if content != "" {
			emitChunk(w, state, &OpenAIMsg{ReasoningContent: &content}, nil)
		}

	case "codeEvent":
		var ev struct {
			Content   string `json:"content"`
			CodeEvent *struct {
				Content string `json:"content"`
			} `json:"codeEvent"`
		}
		if err := json.Unmarshal(frame.Payload, &ev); err != nil {
			return
		}
		content := ev.Content
		if content == "" && ev.CodeEvent != nil {
			content = ev.CodeEvent.Content
		}
		if content != "" {
			state.outputCharCount += len(content)
			emitChunk(w, state, &OpenAIMsg{Content: &content}, nil)
		}

	case "toolUseEvent":
		var ev struct {
			ToolUseID    string `json:"toolUseId"`
			Name         string `json:"name"`
			Input        string `json:"input"`
			Stop         bool   `json:"stop"`
			ToolUseEvent *struct {
				ToolUseID string `json:"toolUseId"`
				Name      string `json:"name"`
				Input     string `json:"input"`
				Stop      bool   `json:"stop"`
			} `json:"toolUseEvent"`
		}
		if err := json.Unmarshal(frame.Payload, &ev); err != nil {
			return
		}
		// Unwrap nested payload shape, used by some Kiro upstream models.
		if ev.ToolUseEvent != nil && ev.ToolUseID == "" {
			ev.ToolUseID = ev.ToolUseEvent.ToolUseID
			ev.Name = ev.ToolUseEvent.Name
			ev.Input = ev.ToolUseEvent.Input
			ev.Stop = ev.ToolUseEvent.Stop
		}
		// Continuation chunks arrive with only `input` populated — no
		// toolUseId, no name. Without a fallback they'd be dropped and
		// OpenCode would parse an unterminated JSON like
		//   {"filePath": "/path/to/file.md"
		// triggering "JSON Parse error: Expected '}'". Resolve to the
		// most recently started toolUse so the args stream keeps flowing.
		if ev.ToolUseID == "" {
			ev.ToolUseID = state.activeToolID
		}
		if ev.ToolUseID == "" {
			return
		}
		state.activeToolID = ev.ToolUseID
		acc, ok := state.toolCalls[ev.ToolUseID]
		if !ok {
			acc = &toolCallAccum{id: ev.ToolUseID, name: ev.Name}
			state.toolCalls[ev.ToolUseID] = acc
			// Emit tool_call start
			emitChunk(w, state, &OpenAIMsg{
				ToolCalls: []OpenAIToolCall{{
					ID:       ev.ToolUseID,
					Type:     "function",
					Function: OpenAIFunctionCall{Name: ev.Name},
				}},
			}, nil)
		}
		if ev.Input != "" {
			acc.args += ev.Input
			emitChunk(w, state, &OpenAIMsg{
				ToolCalls: []OpenAIToolCall{{
					ID:       ev.ToolUseID,
					Type:     "function",
					Function: OpenAIFunctionCall{Arguments: ev.Input},
				}},
			}, nil)
		}
		// Stop=true marks the end of THIS tool call's argument stream.
		// Subsequent toolUseEvent frames belong to a different call, so
		// clear the active id to force the next one to bring its own.
		if ev.Stop {
			state.activeToolID = ""
		}

	case "messageStopEvent":
		// Done
		stopReason := "stop"
		if len(state.toolCalls) > 0 {
			stopReason = "tool_calls"
		}
		emitChunk(w, state, nil, &stopReason)

	case "metricsEvent":
		// Token usage from upstream. Payload may be flat or nested.
		var ev struct {
			InputTokens  int `json:"inputTokens"`
			OutputTokens int `json:"outputTokens"`
			MetricsEvent *struct {
				InputTokens  int `json:"inputTokens"`
				OutputTokens int `json:"outputTokens"`
			} `json:"metricsEvent"`
		}
		if err := json.Unmarshal(frame.Payload, &ev); err == nil {
			in := ev.InputTokens
			out := ev.OutputTokens
			if in == 0 && out == 0 && ev.MetricsEvent != nil {
				in = ev.MetricsEvent.InputTokens
				out = ev.MetricsEvent.OutputTokens
			}
			if in > 0 || out > 0 {
				state.usage = &OpenAIUsage{
					PromptTokens:     in,
					CompletionTokens: out,
					TotalTokens:      in + out,
				}
			}
		}

	case "contextUsageEvent":
		var ev struct {
			ContextUsagePercentage float64 `json:"contextUsagePercentage"`
		}
		if err := json.Unmarshal(frame.Payload, &ev); err == nil {
			// Estimate tokens from context usage % (200k window assumption)
			used := int(ev.ContextUsagePercentage / 100 * 200000)
			if state.usage == nil {
				state.usage = &OpenAIUsage{
					PromptTokens: used,
					TotalTokens:  used,
				}
			}
		}

	case "messageMetadataEvent", "supplementaryWebLinksEvent", "meteringEvent":
		// Known events we don't surface; ignore silently.

	default:
		// Unknown event types are intentionally ignored to keep the
		// stream resilient to upstream additions. Errors surface via
		// the HTTP status code path before this function ever runs.
	}
}

// emitAssistantText splits an incoming assistant text fragment into the
// portion inside `<thinking>…</thinking>` blocks (routed to
// reasoning_content) and the portion outside (routed to content). It
// keeps state on the streamState so partial tag matches at chunk
// boundaries — `<thi` arriving in one chunk and `nking>` in the next —
// don't leak through.
//
// Sonnet 4.5 / Sonnet 4.6 / Opus 4.6 emit their thinking trace inline
// like `<thinking>…</thinking>actual answer here`. Opus 4.7 and
// DeepSeek emit a separate `reasoningContentEvent` instead so they
// never hit this path. Either way the OpenAI client sees a clean
// reasoning_content stream + a clean content stream.
func emitAssistantText(w io.Writer, state *streamState, fragment string) {
	if fragment == "" {
		return
	}
	const openTag = "<thinking>"
	const closeTag = "</thinking>"

	for fragment != "" {
		if state.insideThinking {
			// Look for closing tag, taking thinkingCarry into account
			combined := state.thinkingCarry + fragment
			if idx := strings.Index(combined, closeTag); idx >= 0 {
				// Emit everything up to the close tag as reasoning,
				// then return to outside mode.
				if idx > 0 {
					reasoning := combined[:idx]
					emitChunk(w, state, &OpenAIMsg{ReasoningContent: &reasoning}, nil)
				}
				consumed := idx + len(closeTag)
				if consumed >= len(state.thinkingCarry) {
					fragment = combined[consumed:]
				} else {
					fragment = combined[consumed:]
				}
				state.thinkingCarry = ""
				state.insideThinking = false
				continue
			}
			// No close tag yet. Hold the tail that could still match
			// "</thinking" so we don't split it across chunks.
			holdback := tagHoldback(combined, closeTag)
			if holdback > 0 {
				flush := combined[:len(combined)-holdback]
				if flush != "" {
					emitChunk(w, state, &OpenAIMsg{ReasoningContent: &flush}, nil)
				}
				state.thinkingCarry = combined[len(combined)-holdback:]
			} else {
				if combined != "" {
					emitChunk(w, state, &OpenAIMsg{ReasoningContent: &combined}, nil)
				}
				state.thinkingCarry = ""
			}
			return
		}

		// Outside a thinking block. Look for opening tag.
		combined := state.contentCarry + fragment
		if idx := strings.Index(combined, openTag); idx >= 0 {
			before := combined[:idx]
			if before != "" {
				state.outputCharCount += len(before)
				emitChunk(w, state, &OpenAIMsg{Content: &before}, nil)
			}
			fragment = combined[idx+len(openTag):]
			state.contentCarry = ""
			state.insideThinking = true
			continue
		}
		// No open tag yet. Hold the tail that could still match
		// "<thinking" so we don't accidentally emit a "<thi" prefix
		// as content right before the rest of the tag arrives.
		holdback := tagHoldback(combined, openTag)
		if holdback > 0 {
			flush := combined[:len(combined)-holdback]
			if flush != "" {
				state.outputCharCount += len(flush)
				emitChunk(w, state, &OpenAIMsg{Content: &flush}, nil)
			}
			state.contentCarry = combined[len(combined)-holdback:]
		} else {
			if combined != "" {
				state.outputCharCount += len(combined)
				emitChunk(w, state, &OpenAIMsg{Content: &combined}, nil)
			}
			state.contentCarry = ""
		}
		return
	}
}

// tagHoldback returns how many trailing chars of `s` could still be the
// start of `tag`. Used to defer flushing chunks that might contain a
// partial tag at the boundary, so a `<thi` arriving alone doesn't get
// flushed as user-visible content.
func tagHoldback(s, tag string) int {
	max := len(tag) - 1
	if max > len(s) {
		max = len(s)
	}
	for n := max; n > 0; n-- {
		if strings.HasPrefix(tag, s[len(s)-n:]) {
			return n
		}
	}
	return 0
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
	if finishReason != nil {
		// Synthesize usage on the final chunk. Kiro's metricsEvent often
		// reports outputTokens=0 even when content was clearly streamed
		// (Opus 4.7 in particular). Fall back to a conservative 4 chars
		// per token estimate so the dashboard shows a sane number.
		usage := state.usage
		if usage == nil && state.outputCharCount > 0 {
			usage = &OpenAIUsage{}
		}
		if usage != nil {
			if usage.CompletionTokens == 0 && state.outputCharCount > 0 {
				usage.CompletionTokens = (state.outputCharCount + 3) / 4
				usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
			}
			chunk.Usage = usage
		}
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
//
//	[1 byte: name length]
//	[N bytes: name]
//	[1 byte: type] (7 = string)
//	[2 bytes: value length]
//	[N bytes: value]
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

// isCompleteJSON reports whether the given string is a valid, fully-closed
// JSON document. We use this to detect tool-call argument streams that got
// truncated mid-flight (e.g. upstream connection reset), so we can flag the
// completion with finish_reason="length" instead of "stop"/"tool_calls" and
// give downstream callers a chance to retry instead of feeding broken JSON
// straight into their parser.
//
// An empty string counts as "complete" because some tool calls legitimately
// take zero arguments, and we don't want to false-flag those.
func isCompleteJSON(s string) bool {
	if s == "" {
		return true
	}
	var v any
	return json.Unmarshal([]byte(s), &v) == nil
}
