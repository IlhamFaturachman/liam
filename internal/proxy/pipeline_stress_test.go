// Stress test: end-to-end validation that RTK + Caveman do not break
// any of the existing LIAM Kiro pipeline guarantees.
//
// Specifically checks (each concern → its own test):
//
//   - Tool call ARGUMENTS untouched after RTK + caveman + Kiro translate
//     (Session N+2 streaming-truncation regression sentinel)
//   - LIAM overlay still prepends to first user message after caveman
//     injects into the system message
//   - Caveman prompt actually arrives at the upstream model via the
//     "Developer instructions:" channel (not lost in translation)
//   - Thinking DSL parsing happens BEFORE token savers (sufficient for
//     model name to be clean when RTK/caveman see it)
//   - Multimodal (image) parts survive RTK without mutation
//
// Test strategy: round-trip a realistic OpenAI request body through:
//
//  1. Manual thinking-DSL strip (mirrors server.go handleChatCompletions)
//  2. rtk.Compress
//  3. caveman.Inject
//  4. kiro.translateRequest  (the actual production translator)
//
// Then assert structural invariants on the resulting Kiro payload.
package proxy_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/liam-auto/liam/internal/caveman"
	"github.com/liam-auto/liam/internal/providers/kiro"
	"github.com/liam-auto/liam/internal/rtk"
)

// largeGitDiff is big enough (>500B = MinCompressSize) AND has hunks
// exceeding GitDiffHunkMaxLines (100 lines/hunk) so the compact-diff
// filter actually trims content. Without going past the hunk cap,
// gitDiff would keep every +/- line verbatim and "compression" would
// only save bytes on file-header restatements — which is a real-world
// scenario for small diffs but useless for stress-testing.
//
// Each hunk has 150 added + 150 removed lines (above the 100 cap),
// so the filter MUST drop ~50 of each per hunk and emit "(N lines
// truncated)" markers.
func largeGitDiff() string {
	var b strings.Builder
	b.WriteString("diff --git a/internal/foo.go b/internal/foo.go\n")
	b.WriteString("@@ -1,300 +1,300 @@\n")
	for i := 0; i < 150; i++ {
		b.WriteString("+ added line ")
		b.WriteString(strings.Repeat("x", 30))
		b.WriteByte('\n')
	}
	for i := 0; i < 150; i++ {
		b.WriteString("- removed line ")
		b.WriteString(strings.Repeat("y", 30))
		b.WriteByte('\n')
	}
	return b.String()
}

// realisticOpenAIBody returns the kind of request OpenCode / Claude
// Code typically send to LIAM mid-conversation: a system message,
// historical user/assistant exchanges with tool calls and tool
// results, plus a current user turn.
//
// All three risky shapes appear:
//
//   - assistant.tool_calls.function.arguments (must not be touched)
//   - tool message with large content (must compress via RTK)
//   - user content with image part (must not be touched)
//   - is_error tool_result (must NOT compress — preserve trace)
func realisticOpenAIBody(toolResultContent string) map[string]any {
	return map[string]any{
		"model":  "kr/claude-sonnet-4.5",
		"stream": true,
		"messages": []any{
			map[string]any{
				"role":    "system",
				"content": "You are TradingBotOps. Provide directional reads.",
			},
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":      "image_url",
						"image_url": map[string]any{"url": "data:image/png;base64,iVBOR=="},
					},
					map[string]any{
						"type": "text",
						"text": "review this chart",
					},
				},
			},
			map[string]any{
				"role":    "assistant",
				"content": "I'll inspect the diff first.",
				"tool_calls": []any{
					map[string]any{
						"id":   "call_1",
						"type": "function",
						"function": map[string]any{
							"name":      "read_file",
							"arguments": `{"filePath": "/Users/me/proj/internal/foo.go", "offset": 0, "limit": 200}`,
						},
					},
				},
			},
			// Tool result — large, RTK should compress this
			map[string]any{
				"role":         "tool",
				"tool_call_id": "call_1",
				"content":      toolResultContent,
			},
			// Error tool_result — RTK MUST NOT compress
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": "call_err",
						"is_error":    true,
						"content":     toolResultContent, // same content, but is_error
					},
				},
			},
			map[string]any{
				"role":    "user",
				"content": "now what?",
			},
		},
	}
}

// runFullPipeline mirrors the production call sequence in
// server.go::handleChatCompletions. Returns the marshalled Kiro payload
// after every transform has run.
func runFullPipeline(t *testing.T, body map[string]any, rtkOn, cavemanOn bool, level caveman.Level) []byte {
	t.Helper()

	if rtkOn {
		stats := rtk.Compress(body, true)
		t.Logf("rtk: %s", rtk.FormatLog(stats))
	}
	if cavemanOn {
		caveman.Inject(body, level)
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	model, _ := body["model"].(string)
	out, err := kiro.TranslateRequestForTest(model, bodyBytes, "arn:aws:codewhisperer:us-east-1:638616132270:profile/AAAACCCCXXXX")
	if err != nil {
		t.Fatalf("kiro translateRequest failed: %v", err)
	}
	return out
}

// --- the actual stress tests ----------------------------------------

// TestPipeline_ToolCallArgumentsSurviveAllStages is the regression
// sentinel for Session N+2: under no circumstance should the JSON
// arguments of an assistant.tool_calls entry be mutated. Compressing
// them would corrupt the very thing the streaming-truncation fix
// guarded against.
func TestPipeline_ToolCallArgumentsSurviveAllStages(t *testing.T) {
	body := realisticOpenAIBody(largeGitDiff())

	// Snapshot the tool_calls.arguments BEFORE any transform.
	asst := body["messages"].([]any)[2].(map[string]any)
	wantArgs := asst["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["arguments"].(string)

	out := runFullPipeline(t, body, true, true, caveman.LevelLite)

	// Tool call arguments get parsed by the Kiro translator into a
	// structured `input` object (see toolUses[].input.filePath in the
	// Kiro wire format). The original JSON string disappears, but the
	// VALUES must survive intact. Assert on the value, not the literal.
	if !strings.Contains(string(out), `"/Users/me/proj/internal/foo.go"`) {
		t.Fatalf("tool call arguments lost or mutated by pipeline.\nKiro payload (first 500B):\n%s",
			truncateForLog(string(out), 500))
	}

	// Also assert the snapshot from `body` matches what we expect: the
	// in-memory body must keep arguments verbatim too.
	gotArgs := asst["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["arguments"].(string)
	if gotArgs != wantArgs {
		t.Fatalf("tool_call.arguments mutated in place\nbefore: %q\nafter:  %q", wantArgs, gotArgs)
	}
}

// TestPipeline_OverlayStillPrependsAfterCaveman verifies that when
// caveman injects into the system message, the LIAM Kiro overlay
// still ends up prepended to the first user message. If caveman
// somehow displaced the overlay, we'd lose the deployment frame
// and the model would revert to "Kiro IDE" identity.
func TestPipeline_OverlayStillPrependsAfterCaveman(t *testing.T) {
	body := realisticOpenAIBody(largeGitDiff())

	out := runFullPipeline(t, body, true, true, caveman.LevelFull)
	payload := string(out)

	if !strings.Contains(payload, "deployed through the LIAM proxy") {
		t.Fatalf("LIAM overlay missing from Kiro payload after caveman injection.\nFirst 1KB:\n%s",
			truncateForLog(payload, 1024))
	}
	if !strings.Contains(payload, "Developer instructions:") {
		t.Fatalf("'Developer instructions:' marker missing — overlay flow broken.\nFirst 1KB:\n%s",
			truncateForLog(payload, 1024))
	}
}

// TestPipeline_CavemanReachesUpstream confirms that the caveman prompt
// (added to the OpenAI system message by Inject) actually survives the
// Kiro translate step and ends up visible to the upstream model. The
// Kiro translator extracts system content into "Developer instructions:"
// inside the overlay block — caveman should ride along.
func TestPipeline_CavemanReachesUpstream(t *testing.T) {
	body := realisticOpenAIBody(largeGitDiff())

	out := runFullPipeline(t, body, true, true, caveman.LevelUltra)
	payload := string(out)

	if !strings.Contains(payload, "Respond ultra-terse") {
		t.Fatalf("caveman ultra prompt not in upstream payload — caveman lost in translation.\nFirst 1.5KB:\n%s",
			truncateForLog(payload, 1500))
	}
}

// TestPipeline_RTKCompressedToolResult confirms the LARGE tool_result
// gets compressed (i.e. byte size shrunk) AND the compressed form
// still arrives at the upstream.
func TestPipeline_RTKCompressedToolResult(t *testing.T) {
	rawDiff := largeGitDiff()
	body := realisticOpenAIBody(rawDiff)

	out := runFullPipeline(t, body, true, false, caveman.LevelLite)
	payload := string(out)

	// The fixture has TWO tool_results carrying the same diff:
	//   - regular tool message → RTK compresses (drops above hunk cap)
	//   - is_error tool_result → RTK MUST preserve verbatim (150 stays)
	//
	// Each diff has 150 added "+ added line xxx..." entries. The hunk
	// cap is 100, so the regular one keeps ≤100 of those, while the
	// is_error one keeps all 150. Total in payload: ≤100 + 150 = ≤250.
	// Raw total (no compression): 150 × 2 = 300.
	//
	// We assert the post-compression payload count is BELOW the raw
	// total by a meaningful margin (at least the hunk-cap savings).
	repeatedLine := "added line " + strings.Repeat("x", 30)
	rawTotal := strings.Count(rawDiff, repeatedLine) * 2 // 150 × 2
	payloadCount := strings.Count(payload, repeatedLine)
	if payloadCount >= rawTotal {
		t.Fatalf("RTK did not compress: raw total %d copies of fixture line, payload still has %d",
			rawTotal, payloadCount)
	}
	if payloadCount > 260 {
		// Expected: ≤100 (hunk-capped regular) + 150 (preserved is_error)
		// = 250. We allow a small slack for any leakage.
		t.Fatalf("compression looks weak: expected ≤260 copies, got %d (raw=%d).",
			payloadCount, rawTotal)
	}
	// Compact-diff filter emits a "+N -M" tally line — assert one is present.
	if !strings.Contains(payload, "+150 -150") {
		t.Fatalf("git-diff compact tally missing from payload — filter may have failed silently.\nFirst 2KB:\n%s",
			truncateForLog(payload, 2048))
	}
	// Truncation marker should also appear since hunks exceeded cap.
	if !strings.Contains(payload, "lines truncated") {
		t.Fatalf("hunk truncation marker missing — filter did not trim oversized hunks")
	}
}

// TestPipeline_IsErrorPreserved verifies the rule: tool_result with
// is_error=true must NOT be compressed even when its content matches a
// compressible pattern. Stack traces lose meaning when summarised.
func TestPipeline_IsErrorPreserved(t *testing.T) {
	rawDiff := largeGitDiff()
	body := realisticOpenAIBody(rawDiff)

	out := runFullPipeline(t, body, true, false, caveman.LevelLite)
	payload := string(out)

	// The is_error tool_result lives in the second-to-last user
	// message. Its raw diff must arrive verbatim — at least the
	// trailing "removed line yyy..." pattern must still be intact.
	// Fixture has 150 copies; is_error preservation guarantees they
	// all survive. (The other 150 copies on the regular tool message
	// get hunk-capped to ≤100. Combined: ≥150 should remain.)
	repeatedRemoved := "removed line " + strings.Repeat("y", 30)
	if strings.Count(payload, repeatedRemoved) < 150 {
		t.Fatalf("is_error tool_result was compressed: expected ≥150 copies of fixture line (preserved), got %d",
			strings.Count(payload, repeatedRemoved))
	}
}

// TestPipeline_ImagePartUntouched verifies multimodal image parts
// are HANDLED by the translator (they may be normalised to a Bedrock
// image block, or — if base64 is invalid like in our fixture — to an
// "[Image base64 decode error]" marker). The key claim is that RTK
// did not silently drop the part: the translator still got to see
// it and process it.
func TestPipeline_ImagePartUntouched(t *testing.T) {
	body := realisticOpenAIBody(largeGitDiff())

	out := runFullPipeline(t, body, true, true, caveman.LevelLite)
	payload := string(out)

	// The fixture uses a placeholder base64 ("iVBOR==" — not real PNG
	// data), so the translator's decode path emits an error marker
	// instead of an actual image block. We just need to confirm the
	// image content reached the translator at all (i.e. RTK didn't
	// nuke it on the way through).
	imageHandled := strings.Contains(payload, "Image base64") ||
		strings.Contains(payload, "imageContext") ||
		strings.Contains(payload, "image_url")
	if !imageHandled {
		t.Fatalf("image part appears to be lost in pipeline (no marker, no block).\nFirst 2KB:\n%s",
			truncateForLog(payload, 2048))
	}
}

// TestPipeline_ThinkingSuffixIndependent verifies that a model with
// the thinking DSL `(8192)` suffix gets its name cleaned BEFORE token
// savers see it. We mimic the server.go strip logic and run the
// pipeline to confirm Kiro receives the clean model name.
func TestPipeline_ThinkingSuffixIndependent(t *testing.T) {
	body := realisticOpenAIBody(largeGitDiff())
	body["model"] = "kr/claude-haiku-4.5(8192)"

	// Mirror server.go thinking-DSL parsing: strip suffix, set
	// reasoning_effort. (We don't rely on test imports of server
	// internals — just replicate the lines that touch model name.)
	if model, ok := body["model"].(string); ok {
		if idx := strings.LastIndex(model, "("); idx > 0 && strings.HasSuffix(model, ")") {
			body["model"] = model[:idx]
			body["reasoning_effort"] = model[idx+1 : len(model)-1]
		}
	}

	out := runFullPipeline(t, body, true, false, caveman.LevelLite)
	payload := string(out)

	// Suffix must NOT survive into the Kiro request. (Kiro accepts
	// kr/claude-haiku-4.5 — the (8192) is metadata for our side only.)
	if strings.Contains(payload, "(8192)") {
		t.Fatalf("thinking DSL suffix leaked into upstream payload")
	}
}

// TestPipeline_BothSaversOff is the no-op baseline: with both savers
// disabled, the round-trip should still produce a valid Kiro payload
// with all the existing guarantees (overlay, tool_calls intact, etc.).
func TestPipeline_BothSaversOff(t *testing.T) {
	body := realisticOpenAIBody(largeGitDiff())

	out := runFullPipeline(t, body, false, false, caveman.LevelLite)
	payload := string(out)

	if !strings.Contains(payload, "deployed through the LIAM proxy") {
		t.Fatalf("LIAM overlay missing in baseline (no savers) — pre-existing bug, not RTK/caveman")
	}
	if !strings.Contains(payload, `"/Users/me/proj/internal/foo.go"`) {
		t.Fatalf("tool_call.arguments missing in baseline — pre-existing bug")
	}
}

// truncateForLog shortens long payloads for test failure messages so
// CI logs stay readable. We always show a short prefix; the failure
// message itself still names what was wrong.
func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n... (+" + itoaInt(len(s)-n) + " bytes)"
}

func itoaInt(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
