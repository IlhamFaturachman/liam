package rtk

import (
	"fmt"
	"strings"
)

// Stats records what RTK did during a single Compress call. Caller
// emits this as a log line; we keep both the raw byte counts and the
// per-filter hit list so a "saved 4kb via grep+find hits=3" message
// summarises a request at a glance.
type Stats struct {
	BytesBefore int
	BytesAfter  int
	Hits        []HitEntry
}

// HitEntry tracks one successful filter application.
type HitEntry struct {
	Shape  string // which envelope shape carried the content
	Filter string // which filter compressed it
	Saved  int    // bytes saved on this particular content
}

// Compress walks `body` (parsed OpenAI request body) and compresses every
// tool_result content it finds. Mutates `body` in place. Returns nil if
// disabled, body shape unrecognised, or compression failed for a non-fatal
// reason — caller should treat nil as "no-op" and continue.
//
// Supported shapes (mirrors 9router compressMessages):
//
//  1. {"role":"tool", "content":"text"}                                — OpenAI chat
//  2. {"role":"tool", "content":[{"type":"text","text":"..."}]}        — OpenAI array
//  3. {"role":"user", "content":[{"type":"tool_result", "content":"..."}]}             — Anthropic-style
//  4. {"role":"user", "content":[{"type":"tool_result", "content":[{"type":"text"}]}]} — Anthropic array
//
// Tool-call ARGUMENTS (from assistant messages) are intentionally
// untouched. Compressing those would corrupt the JSON the model sent.
func Compress(body map[string]any, enabled bool) *Stats {
	if !enabled || body == nil {
		return nil
	}
	messages := extractMessageArray(body)
	if messages == nil {
		return nil
	}

	stats := &Stats{}
	defer func() {
		// Filters run inside SafeApply, but the walk itself can panic on
		// pathological body shapes. Rather than letting that escape and
		// fail the whole request, recover and treat as no-op.
		if r := recover(); r != nil {
			// Reset stats so the caller doesn't think compression
			// succeeded when it actually crashed mid-walk.
			stats = nil
		}
	}()

	for i := range messages {
		m, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)

		// Shape 1: {role:"tool", content:"text"}
		if role == "tool" {
			if s, ok := m["content"].(string); ok {
				m["content"] = compressText(s, stats, "openai-tool")
				continue
			}
			if arr, ok := m["content"].([]any); ok {
				// Shape 2: {role:"tool", content:[{type:"text", text:"..."}]}
				for k := range arr {
					part, ok := arr[k].(map[string]any)
					if !ok {
						continue
					}
					if pt, _ := part["type"].(string); pt != "text" {
						continue
					}
					if t, ok := part["text"].(string); ok {
						part["text"] = compressText(t, stats, "openai-tool-array")
					}
				}
				continue
			}
		}

		// Shapes 3/4: tool_result blocks inside user content array.
		arr, ok := m["content"].([]any)
		if !ok {
			continue
		}
		for j := range arr {
			block, ok := arr[j].(map[string]any)
			if !ok {
				continue
			}
			if bt, _ := block["type"].(string); bt != "tool_result" {
				continue
			}
			// Preserve error traces verbatim — the model needs the
			// exact stack/output to recover.
			if isErr, _ := block["is_error"].(bool); isErr {
				continue
			}
			if s, ok := block["content"].(string); ok {
				block["content"] = compressText(s, stats, "claude-string")
				continue
			}
			if inner, ok := block["content"].([]any); ok {
				for k := range inner {
					part, ok := inner[k].(map[string]any)
					if !ok {
						continue
					}
					if pt, _ := part["type"].(string); pt != "text" {
						continue
					}
					if t, ok := part["text"].(string); ok {
						part["text"] = compressText(t, stats, "claude-array")
					}
				}
			}
		}
	}

	return stats
}

// extractMessageArray finds the OpenAI/Claude messages array in body.
// LIAM speaks OpenAI to clients exclusively, so we only look at
// body["messages"]. The Responses API path (`input` array) is handled
// directly by Claude/Antigravity translators and doesn't reach RTK.
func extractMessageArray(body map[string]any) []any {
	if msgs, ok := body["messages"].([]any); ok {
		return msgs
	}
	return nil
}

func compressText(text string, stats *Stats, shape string) string {
	bytesIn := len(text)
	stats.BytesBefore += bytesIn

	if bytesIn < MinCompressSize || bytesIn > RawCap {
		stats.BytesAfter += bytesIn
		return text
	}

	ff := AutoDetect(text)
	if ff.Fn == nil {
		stats.BytesAfter += bytesIn
		return text
	}

	out := SafeApply(ff, text)
	// Safety: never return empty, never grow the input.
	if out == "" || len(out) >= bytesIn {
		stats.BytesAfter += bytesIn
		return text
	}

	stats.BytesAfter += len(out)
	stats.Hits = append(stats.Hits, HitEntry{Shape: shape, Filter: ff.Name, Saved: bytesIn - len(out)})
	return out
}

// FormatLog renders Stats into a single log line for operator visibility.
// Returns "" when there's nothing meaningful to log (no hits).
func FormatLog(s *Stats) string {
	if s == nil || len(s.Hits) == 0 {
		return ""
	}
	saved := s.BytesBefore - s.BytesAfter
	pct := 0.0
	if s.BytesBefore > 0 {
		pct = float64(saved) / float64(s.BytesBefore) * 100
	}
	seen := map[string]struct{}{}
	var filterList []string
	for _, h := range s.Hits {
		if _, ok := seen[h.Filter]; ok {
			continue
		}
		seen[h.Filter] = struct{}{}
		filterList = append(filterList, h.Filter)
	}
	return fmt.Sprintf("[RTK] saved %dB / %dB (%.1f%%) via [%s] hits=%d",
		saved, s.BytesBefore, pct, strings.Join(filterList, ","), len(s.Hits))
}
