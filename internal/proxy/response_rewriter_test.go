package proxy

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestRewriteModelField_replacesModel(t *testing.T) {
	input := []byte(`{"id":"chatcmpl-1","model":"anthropic.claude-sonnet","choices":[]}`)
	got := rewriteModelField(input, "kr/claude-sonnet-4-6")

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(got, &obj); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	var model string
	if err := json.Unmarshal(obj["model"], &model); err != nil {
		t.Fatalf("model field is not a string: %v", err)
	}
	if model != "kr/claude-sonnet-4-6" {
		t.Errorf("want model %q, got %q", "kr/claude-sonnet-4-6", model)
	}
	var id string
	json.Unmarshal(obj["id"], &id)
	if id != "chatcmpl-1" {
		t.Errorf("id field was altered, got %q", id)
	}
}

func TestRewriteModelField_noModelField(t *testing.T) {
	input := []byte(`{"id":"chatcmpl-1","choices":[]}`)
	got := rewriteModelField(input, "kr/claude-sonnet-4-6")
	if !bytes.Equal(got, input) {
		t.Errorf("expected no change when model field absent, got %s", got)
	}
}

func TestRewriteModelField_emptyTarget(t *testing.T) {
	input := []byte(`{"id":"x","model":"upstream","choices":[]}`)
	got := rewriteModelField(input, "")
	if !bytes.Equal(got, input) {
		t.Error("expected no change when targetModel is empty")
	}
}

func TestRewriteModelInChunk_rewritesDataLine(t *testing.T) {
	chunk := []byte("data: {\"id\":\"c1\",\"model\":\"anthropic.claude\",\"choices\":[]}\n\ndata: [DONE]\n")
	got := rewriteModelInChunk(chunk, "kr/claude-3-5")

	if !bytes.Contains(got, []byte(`"model":"kr/claude-3-5"`)) {
		t.Errorf("model not rewritten in chunk:\n%s", got)
	}
	if !bytes.Contains(got, []byte("data: [DONE]")) {
		t.Error("[DONE] line was altered")
	}
}

func TestRewriteModelInChunk_emptyTarget(t *testing.T) {
	chunk := []byte("data: {\"model\":\"upstream\"}\n\n")
	got := rewriteModelInChunk(chunk, "")
	if !bytes.Equal(got, chunk) {
		t.Error("expected no change when targetModel is empty")
	}
}

func TestRewriteModelInChunk_nonJsonDataLine(t *testing.T) {
	chunk := []byte("data: [DONE]\n\n")
	got := rewriteModelInChunk(chunk, "kr/model")
	if !bytes.Equal(got, chunk) {
		t.Errorf("expected non-JSON data line to pass through unchanged, got %s", got)
	}
}

func TestRewriteModelInChunk_preservesCRLF(t *testing.T) {
	chunk := []byte("data: {\"id\":\"c1\",\"model\":\"upstream\",\"choices\":[]}\r\n\r\n")
	got := rewriteModelInChunk(chunk, "kr/claude-3-5")

	if !bytes.Contains(got, []byte(`"model":"kr/claude-3-5"`)) {
		t.Errorf("model not rewritten in CRLF chunk:\n%s", got)
	}
	// The rewritten line must still end with \r\n framing, not just \n.
	if !bytes.Contains(got, []byte("}\r\n")) {
		t.Errorf("CRLF line ending was corrupted:\n%q", got)
	}
}

func TestRewriteModelInChunk_malformedDataLine(t *testing.T) {
	chunk := []byte("data: {not json}\n\n")
	got := rewriteModelInChunk(chunk, "kr/model")
	if !bytes.Equal(got, chunk) {
		t.Errorf("expected malformed JSON data line to pass through unchanged, got %s", got)
	}
}

func TestRewriteModelInBody_replacesModel(t *testing.T) {
	input := []byte(`{"model":"upstream-internal","choices":[{"message":{"content":"hi"}}]}`)
	got := rewriteModelInBody(input, "ag/gemini-2.5-pro")

	var obj map[string]json.RawMessage
	json.Unmarshal(got, &obj)
	var model string
	json.Unmarshal(obj["model"], &model)
	if model != "ag/gemini-2.5-pro" {
		t.Errorf("want %q, got %q", "ag/gemini-2.5-pro", model)
	}
}
