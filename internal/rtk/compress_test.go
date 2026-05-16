package rtk

import (
	"encoding/json"
	"strings"
	"testing"
)

// Stress tests for the RTK compress entry point. Every claim in the
// "anti-break checklist" of CONTEXT.md should have at least one test
// here that fails loudly if the claim regresses.
//
// Coverage by claim:
//
//   - Tool call ARGUMENTS untouched          → TestToolCallArgumentsUntouched
//   - is_error tool results preserved         → TestIsErrorPreserved
//   - Multimodal image parts skipped          → TestMultimodalImageUntouched
//   - Body size below MinCompressSize skipped → TestSmallBodySkipped
//   - Body size above RawCap skipped          → TestOversizedBodySkipped
//   - Disabled flag → no-op                   → TestDisabledNoOp
//   - All four content shapes work            → TestShapeOpenAITool*, TestShapeClaude*
//   - Output never longer than input          → TestNoGrowthGuarantee
//   - Output never empty                      → TestNoEmptyGuarantee
//   - Stats track hits accurately             → TestStatsBookkeeping

const realisticGitDiff = `diff --git a/internal/foo.go b/internal/foo.go
index 1234567..abcdef0 100644
--- a/internal/foo.go
+++ b/internal/foo.go
@@ -10,6 +10,8 @@ func Foo() error {
 	if err != nil {
 		return err
 	}
+	// New validation step
+	validate(input)
 	return nil
 }
@@ -50,7 +52,8 @@ func Bar() {
-	old := compute()
+	old := computeNew()
+	old.refresh()
 	process(old)
 }
`

// makeBigDiff produces a diff > MinCompressSize (500B) so it actually
// triggers compression. Tests below use this to verify hits register.
func makeBigDiff() string {
	var b strings.Builder
	b.WriteString("diff --git a/big.go b/big.go\n@@ -1,200 +1,200 @@\n")
	for i := 0; i < 200; i++ {
		b.WriteString("+ added line ")
		b.WriteString(strings.Repeat("x", 20))
		b.WriteByte('\n')
	}
	return b.String()
}

func TestDisabledNoOp(t *testing.T) {
	body := mustParse(`{"messages":[{"role":"tool","content":"` + escapeForJSON(makeBigDiff()) + `"}]}`)
	stats := Compress(body, false)
	if stats != nil {
		t.Fatalf("expected nil stats when disabled, got %+v", stats)
	}
}

func TestSmallBodySkipped(t *testing.T) {
	// 100 bytes — below MinCompressSize (500). Even if it looks like a
	// git diff, RTK should not even autodetect it.
	body := mustParse(`{"messages":[{"role":"tool","content":"diff --git a/x b/x\n+ small change"}]}`)
	stats := Compress(body, true)
	if stats == nil {
		t.Fatalf("expected non-nil stats")
	}
	if len(stats.Hits) != 0 {
		t.Fatalf("expected 0 hits for small body, got %d", len(stats.Hits))
	}
}

func TestOversizedBodySkipped(t *testing.T) {
	// Above RawCap — autodetect cost not worth it.
	huge := strings.Repeat("a", RawCap+1)
	body := map[string]any{
		"messages": []any{
			map[string]any{"role": "tool", "content": huge},
		},
	}
	stats := Compress(body, true)
	if stats == nil {
		t.Fatalf("expected non-nil stats")
	}
	if len(stats.Hits) != 0 {
		t.Fatalf("expected 0 hits for oversized body, got %d", len(stats.Hits))
	}
}

func TestToolCallArgumentsUntouched(t *testing.T) {
	// CRITICAL: assistant tool_calls.function.arguments is the JSON the
	// model emitted. Compressing it = corrupting the very thing
	// Session N+2 fixed. This test asserts RTK never touches it even
	// when it looks like a compressible blob.
	bigArgs := makeBigDiff() // even though this looks like git diff
	body := mustParse(`{"messages":[
		{"role":"assistant","tool_calls":[{
			"id":"call_1","type":"function",
			"function":{"name":"x","arguments":"` + escapeForJSON(bigArgs) + `"}
		}]},
		{"role":"tool","tool_call_id":"call_1","content":"` + escapeForJSON(makeBigDiff()) + `"}
	]}`)

	before, _ := json.Marshal(body["messages"].([]any)[0])
	stats := Compress(body, true)
	after, _ := json.Marshal(body["messages"].([]any)[0])

	if string(before) != string(after) {
		t.Fatalf("assistant tool_calls mutated!\nbefore: %s\nafter:  %s", before, after)
	}
	// Tool result on the next message SHOULD be compressed though.
	if stats == nil || len(stats.Hits) == 0 {
		t.Fatalf("tool_result should still compress, got 0 hits")
	}
}

func TestIsErrorPreserved(t *testing.T) {
	body := mustParse(`{"messages":[
		{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"x","is_error":true,"content":"` + escapeForJSON(makeBigDiff()) + `"}
		]}
	]}`)

	before := body["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["content"]
	stats := Compress(body, true)
	after := body["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["content"]

	if before != after {
		t.Fatalf("is_error tool_result was compressed!\nbefore: %v\nafter:  %v", before, after)
	}
	if stats != nil && len(stats.Hits) > 0 {
		t.Fatalf("expected 0 hits when is_error blocks all compression, got %d", len(stats.Hits))
	}
}

func TestMultimodalImageUntouched(t *testing.T) {
	// Image parts in user content array must be left alone.
	body := mustParse(`{"messages":[
		{"role":"user","content":[
			{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBOR..."}},
			{"type":"text","text":"describe this"},
			{"type":"tool_result","content":"` + escapeForJSON(makeBigDiff()) + `"}
		]}
	]}`)

	before, _ := json.Marshal(body["messages"].([]any)[0].(map[string]any)["content"].([]any)[0])
	stats := Compress(body, true)
	after, _ := json.Marshal(body["messages"].([]any)[0].(map[string]any)["content"].([]any)[0])

	if string(before) != string(after) {
		t.Fatalf("image part mutated!\nbefore: %s\nafter:  %s", before, after)
	}
	// tool_result in same array should still compress
	if stats == nil || len(stats.Hits) == 0 {
		t.Fatalf("expected tool_result in mixed-content array to compress")
	}
}

func TestShapeOpenAIToolString(t *testing.T) {
	// Shape 1: {role:"tool", content:"text"}
	body := mustParse(`{"messages":[{"role":"tool","content":"` + escapeForJSON(makeBigDiff()) + `"}]}`)
	stats := Compress(body, true)
	if stats == nil || len(stats.Hits) == 0 {
		t.Fatalf("expected compression on OpenAI tool string shape")
	}
	if stats.Hits[0].Shape != "openai-tool" {
		t.Fatalf("wrong shape tag: got %q", stats.Hits[0].Shape)
	}
}

func TestShapeOpenAIToolArray(t *testing.T) {
	// Shape 2: {role:"tool", content:[{type:"text", text:"..."}]}
	body := mustParse(`{"messages":[{"role":"tool","content":[
		{"type":"text","text":"` + escapeForJSON(makeBigDiff()) + `"}
	]}]}`)
	stats := Compress(body, true)
	if stats == nil || len(stats.Hits) == 0 {
		t.Fatalf("expected compression on OpenAI tool array shape")
	}
	if stats.Hits[0].Shape != "openai-tool-array" {
		t.Fatalf("wrong shape tag: got %q", stats.Hits[0].Shape)
	}
}

func TestShapeClaudeStringInUser(t *testing.T) {
	// Shape 3: tool_result with string content inside user array
	body := mustParse(`{"messages":[{"role":"user","content":[
		{"type":"tool_result","content":"` + escapeForJSON(makeBigDiff()) + `"}
	]}]}`)
	stats := Compress(body, true)
	if stats == nil || len(stats.Hits) == 0 {
		t.Fatalf("expected compression on claude string-in-user shape")
	}
	if stats.Hits[0].Shape != "claude-string" {
		t.Fatalf("wrong shape tag: got %q", stats.Hits[0].Shape)
	}
}

func TestShapeClaudeArrayInUser(t *testing.T) {
	// Shape 4: tool_result with array content
	body := mustParse(`{"messages":[{"role":"user","content":[
		{"type":"tool_result","content":[
			{"type":"text","text":"` + escapeForJSON(makeBigDiff()) + `"}
		]}
	]}]}`)
	stats := Compress(body, true)
	if stats == nil || len(stats.Hits) == 0 {
		t.Fatalf("expected compression on claude array-in-user shape")
	}
	if stats.Hits[0].Shape != "claude-array" {
		t.Fatalf("wrong shape tag: got %q", stats.Hits[0].Shape)
	}
}

func TestNoGrowthGuarantee(t *testing.T) {
	// Edge case: pathological input where compression would PRODUCE
	// more bytes than the original. Filter must passthrough.
	tiny := strings.Repeat("diff --git a/x b/x\n", 100)
	body := mustParse(`{"messages":[{"role":"tool","content":"` + escapeForJSON(tiny) + `"}]}`)
	bytesBefore := len(body["messages"].([]any)[0].(map[string]any)["content"].(string))

	Compress(body, true)
	after := body["messages"].([]any)[0].(map[string]any)["content"].(string)

	if len(after) > bytesBefore {
		t.Fatalf("compression grew the input: %d → %d bytes", bytesBefore, len(after))
	}
}

func TestNoEmptyGuarantee(t *testing.T) {
	// If a filter produces empty output (impossible with current
	// filters but the guard exists for future ones), passthrough.
	body := mustParse(`{"messages":[{"role":"tool","content":"` + escapeForJSON(makeBigDiff()) + `"}]}`)
	Compress(body, true)
	out := body["messages"].([]any)[0].(map[string]any)["content"].(string)
	if out == "" {
		t.Fatalf("compressed output was empty")
	}
}

func TestStatsBookkeeping(t *testing.T) {
	body := mustParse(`{"messages":[
		{"role":"tool","content":"` + escapeForJSON(makeBigDiff()) + `"},
		{"role":"tool","content":"` + escapeForJSON(makeBigDiff()) + `"}
	]}`)
	stats := Compress(body, true)
	if stats == nil {
		t.Fatalf("expected stats")
	}
	if len(stats.Hits) != 2 {
		t.Fatalf("expected 2 hits, got %d", len(stats.Hits))
	}
	if stats.BytesAfter >= stats.BytesBefore {
		t.Fatalf("BytesAfter (%d) should be less than BytesBefore (%d)", stats.BytesAfter, stats.BytesBefore)
	}
	for _, h := range stats.Hits {
		if h.Saved <= 0 {
			t.Fatalf("hit reported non-positive savings: %+v", h)
		}
	}
}

func TestEmptyMessages(t *testing.T) {
	body := mustParse(`{"messages":[]}`)
	stats := Compress(body, true)
	if stats == nil {
		t.Fatalf("expected non-nil stats even for empty messages")
	}
	if len(stats.Hits) != 0 {
		t.Fatalf("expected 0 hits, got %d", len(stats.Hits))
	}
}

func TestNoMessagesArray(t *testing.T) {
	// Body without messages array (e.g. /v1/embeddings shape).
	body := mustParse(`{"input":"hello","model":"text-embedding-ada-002"}`)
	stats := Compress(body, true)
	if stats != nil {
		t.Fatalf("expected nil stats when messages array missing, got %+v", stats)
	}
}

// FormatLog should produce a human-readable string only when there are hits.
func TestFormatLog(t *testing.T) {
	if FormatLog(nil) != "" {
		t.Fatalf("nil stats should produce empty log line")
	}
	if FormatLog(&Stats{}) != "" {
		t.Fatalf("zero-hit stats should produce empty log line")
	}
	stats := &Stats{
		BytesBefore: 1000,
		BytesAfter:  600,
		Hits:        []HitEntry{{Filter: "git-diff", Shape: "openai-tool", Saved: 400}},
	}
	line := FormatLog(stats)
	if !strings.Contains(line, "saved 400B") || !strings.Contains(line, "git-diff") {
		t.Fatalf("FormatLog output unexpected: %s", line)
	}
}

// --- Helpers ----------------------------------------------------------

// mustParse parses a JSON literal into the dynamic body shape Compress
// expects. Anything we can't parse is a test bug.
func mustParse(s string) map[string]any {
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		panic("test JSON literal failed to parse: " + err.Error() + "\n" + s)
	}
	return m
}

// escapeForJSON shells string content into a JSON literal safely. We use
// it for diff-shaped fixtures inside test JSON literals.
func escapeForJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1]) // strip outer quotes
}
