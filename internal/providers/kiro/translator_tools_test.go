package kiro

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestToolSpecsHaveRequired verifies we always inject `required: []` and
// the rest of the JSON-schema essentials. Kiro upstream rejects tool
// definitions that miss `required`, so a regression here would silently
// crash every tool-using request.
func TestToolSpecsHaveRequired(t *testing.T) {
	body := []byte(`{
		"model": "kr/claude-sonnet-4.6",
		"messages": [{"role": "user", "content": "test"}],
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "no_required",
					"description": "",
					"parameters": {"type": "object", "properties": {"x": {"type": "string"}}}
				}
			},
			{
				"type": "function",
				"function": {
					"name": "empty_schema",
					"parameters": {}
				}
			}
		]
	}`)
	out, err := translateRequest("kr/claude-sonnet-4.6", body, "arn:test")
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	var parsed map[string]interface{}
	json.Unmarshal(out, &parsed)
	cm := parsed["conversationState"].(map[string]interface{})["currentMessage"].(map[string]interface{})
	uim := cm["userInputMessage"].(map[string]interface{})
	ctx := uim["userInputMessageContext"].(map[string]interface{})
	tools := ctx["tools"].([]interface{})
	for i, raw := range tools {
		spec := raw.(map[string]interface{})["toolSpecification"].(map[string]interface{})
		schema := spec["inputSchema"].(map[string]interface{})["json"].(map[string]interface{})
		if _, ok := schema["required"]; !ok {
			t.Errorf("tool[%d] missing required", i)
		}
		if _, ok := schema["properties"]; !ok {
			t.Errorf("tool[%d] missing properties", i)
		}
	}
}

// TestEmptyAssistantContentIsPlaceholdered guards against the "Kiro
// rejects empty assistant text" footgun. Models like Opus 4.7 sometimes
// emit messages with only tool_calls and no visible text; the previous
// translator would forward an empty content field which the upstream
// validator rejected with a 400.
func TestEmptyAssistantContentIsPlaceholdered(t *testing.T) {
	body := []byte(`{
		"model": "kr/claude-sonnet-4.6",
		"messages": [
			{"role": "user", "content": "use the tool"},
			{"role": "assistant", "content": "", "tool_calls": [
				{"id": "call_1", "type": "function", "function": {"name": "ping", "arguments": "{}"}}
			]},
			{"role": "tool", "tool_call_id": "call_1", "content": "pong"},
			{"role": "user", "content": "thanks"}
		]
	}`)
	out, err := translateRequest("kr/claude-sonnet-4.6", body, "arn:test")
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	var parsed map[string]interface{}
	json.Unmarshal(out, &parsed)
	hist := parsed["conversationState"].(map[string]interface{})["history"].([]interface{})
	for _, raw := range hist {
		m := raw.(map[string]interface{})
		if a, ok := m["assistantResponseMessage"].(map[string]interface{}); ok {
			if c, _ := a["content"].(string); strings.TrimSpace(c) == "" {
				t.Errorf("assistant message has empty content (would 400)")
			}
		}
	}
}

// TestToolMessageBecomesUserToolResult covers the "tool" role from
// OpenAI/Anthropic clients. It must surface as a user turn carrying
// userInputMessageContext.toolResults — anything else makes Kiro lose the
// function output and the model has no way to continue the chain.
func TestToolMessageBecomesUserToolResult(t *testing.T) {
	body := []byte(`{
		"model": "kr/claude-sonnet-4.6",
		"messages": [
			{"role": "user", "content": "search for cat"},
			{"role": "assistant", "content": "looking", "tool_calls": [
				{"id": "call_42", "type": "function", "function": {"name": "search", "arguments": "{\"q\":\"cat\"}"}}
			]},
			{"role": "tool", "tool_call_id": "call_42", "content": "found 3 photos"},
			{"role": "user", "content": "summarize"}
		]
	}`)
	out, err := translateRequest("kr/claude-sonnet-4.6", body, "arn:test")
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	var parsed map[string]interface{}
	json.Unmarshal(out, &parsed)
	hist := parsed["conversationState"].(map[string]interface{})["history"].([]interface{})

	// We expect: [user, assistant, user(toolResult)].
	if len(hist) != 3 {
		t.Fatalf("expected 3 history items, got %d", len(hist))
	}
	thirdUser, ok := hist[2].(map[string]interface{})["userInputMessage"].(map[string]interface{})
	if !ok {
		t.Fatalf("third history item should be a userInputMessage")
	}
	ctx, ok := thirdUser["userInputMessageContext"].(map[string]interface{})
	if !ok {
		t.Fatalf("tool turn missing userInputMessageContext")
	}
	results, ok := ctx["toolResults"].([]interface{})
	if !ok || len(results) != 1 {
		t.Fatalf("expected 1 toolResult, got %v", ctx["toolResults"])
	}
	first := results[0].(map[string]interface{})
	if first["toolUseId"] != "call_42" {
		t.Errorf("wrong toolUseId: %v", first["toolUseId"])
	}
}

// TestConsecutiveUserTurnsMerge ensures we collapse adjacent user-style
// messages — Kiro requires strict alternation, so two user turns in a row
// would otherwise be rejected by the validator.
func TestConsecutiveUserTurnsMerge(t *testing.T) {
	body := []byte(`{
		"model": "kr/claude-sonnet-4.6",
		"messages": [
			{"role": "user", "content": "first"},
			{"role": "user", "content": "second"},
			{"role": "assistant", "content": "got it"},
			{"role": "user", "content": "third"}
		]
	}`)
	out, err := translateRequest("kr/claude-sonnet-4.6", body, "arn:test")
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	var parsed map[string]interface{}
	json.Unmarshal(out, &parsed)
	hist := parsed["conversationState"].(map[string]interface{})["history"].([]interface{})

	// Expect [merged user "first\n\nsecond", assistant "got it"].
	if len(hist) != 2 {
		t.Fatalf("expected 2 history items after merge, got %d: %v", len(hist), hist)
	}
	first := hist[0].(map[string]interface{})["userInputMessage"].(map[string]interface{})
	content, _ := first["content"].(string)
	if !strings.Contains(content, "first") || !strings.Contains(content, "second") {
		t.Errorf("merged content missing one of the user turns: %q", content)
	}
}

// TestTrimHistoryToBudgetRespectsCurrentMessage simulates a runaway
// 200k+ token chat by stuffing history with large filler turns. The
// translator should trim oldest pairs until the payload fits, while never
// touching the active currentMessage. This is exactly the scenario that
// throws a 400 in 9router around 150k+ tokens.
func TestTrimHistoryToBudgetRespectsCurrentMessage(t *testing.T) {
	// 50 KB of filler per turn × 40 turns ≈ 2 MB raw — well above the
	// 750 KB budget so we expect aggressive trimming.
	filler := strings.Repeat("X", 50*1024)
	msgs := []map[string]interface{}{
		{"role": "system", "content": "be helpful"},
	}
	for i := 0; i < 40; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		msgs = append(msgs, map[string]interface{}{"role": role, "content": filler})
	}
	msgs = append(msgs, map[string]interface{}{"role": "user", "content": "ok summarize"})

	body, _ := json.Marshal(map[string]interface{}{
		"model":    "kr/claude-opus-4.7",
		"messages": msgs,
	})
	out, err := translateRequest("kr/claude-opus-4.7", body, "arn:test")
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if len(out) > payloadBudgetBytes+50*1024 {
		t.Errorf("payload too large after trimming: %d bytes", len(out))
	}
	var parsed map[string]interface{}
	json.Unmarshal(out, &parsed)
	cm := parsed["conversationState"].(map[string]interface{})["currentMessage"].(map[string]interface{})
	uim := cm["userInputMessage"].(map[string]interface{})
	cur, _ := uim["content"].(string)
	if !strings.Contains(cur, "ok summarize") {
		t.Errorf("currentMessage was lost during trim: %q", cur)
	}
	hist := parsed["conversationState"].(map[string]interface{})["history"].([]interface{})
	if len(hist) >= 40 {
		t.Errorf("expected history to be trimmed, still got %d items", len(hist))
	}
	t.Logf("trimmed: payload=%d bytes, history=%d items", len(out), len(hist))
}

// TestSchemaPassThroughPreservesAnnotations verifies buildToolSpecs keeps
// caller-provided JSON-schema annotations intact. We only patch required
// structural keys (type/properties/required) and do not aggressively rewrite
// schemas, which previously caused malformed tool payloads in real traffic.
func TestSchemaPassThroughPreservesAnnotations(t *testing.T) {
	body := []byte(`{
		"model": "kr/claude-sonnet-4.6",
		"messages": [{"role": "user", "content": "test"}],
		"tools": [{
			"type": "function",
			"function": {
				"name": "noisy_tool",
				"description": "has lots of cruft",
				"parameters": {
					"$schema": "http://json-schema.org/draft-07/schema#",
					"$id": "noisy",
					"$comment": "internal note",
					"definitions": {"x": {"type": "string"}},
					"$defs": {"y": {"type": "number"}},
					"examples": [{"x": "demo"}],
					"readOnly": true,
					"writeOnly": false,
					"deprecated": false,
					"contentMediaType": "application/json",
					"contentEncoding": "utf-8",
					"type": "object",
					"properties": {
						"file_path": {"type": "string", "description": "path"}
					},
					"required": ["file_path"]
				}
			}
		}]
	}`)
	out, err := translateRequest("kr/claude-sonnet-4.6", body, "arn:test")
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	var parsed map[string]interface{}
	json.Unmarshal(out, &parsed)
	cm := parsed["conversationState"].(map[string]interface{})["currentMessage"].(map[string]interface{})
	uim := cm["userInputMessage"].(map[string]interface{})
	ctx := uim["userInputMessageContext"].(map[string]interface{})
	tools := ctx["tools"].([]interface{})
	spec := tools[0].(map[string]interface{})["toolSpecification"].(map[string]interface{})
	schema := spec["inputSchema"].(map[string]interface{})["json"].(map[string]interface{})

	for _, key := range []string{
		"$schema", "$id", "$comment", "definitions", "$defs",
		"examples", "readOnly", "writeOnly", "deprecated",
		"contentMediaType", "contentEncoding",
	} {
		if _, present := schema[key]; !present {
			t.Errorf("schema annotation %q should be preserved", key)
		}
	}
	// Essentials must still be there.
	if schema["type"] != "object" {
		t.Errorf("type field clobbered: %v", schema["type"])
	}
	if _, ok := schema["properties"]; !ok {
		t.Errorf("properties stripped")
	}
	if _, ok := schema["required"]; !ok {
		t.Errorf("required stripped")
	}
}

// TestSchemaRefIsPreserved verifies $ref pointers are passed through
// unchanged. Kiro tolerates references and we avoid schema surgery.
func TestSchemaRefIsPreserved(t *testing.T) {
	body := []byte(`{
		"model": "kr/claude-sonnet-4.6",
		"messages": [{"role": "user", "content": "test"}],
		"tools": [{
			"type": "function",
			"function": {
				"name": "ref_tool",
				"parameters": {
					"type": "object",
					"properties": {
						"value": {"$ref": "#/definitions/Whatever"}
					},
					"required": ["value"]
				}
			}
		}]
	}`)
	out, err := translateRequest("kr/claude-sonnet-4.6", body, "arn:test")
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	var parsed map[string]interface{}
	json.Unmarshal(out, &parsed)
	cm := parsed["conversationState"].(map[string]interface{})["currentMessage"].(map[string]interface{})
	uim := cm["userInputMessage"].(map[string]interface{})
	ctx := uim["userInputMessageContext"].(map[string]interface{})
	tools := ctx["tools"].([]interface{})
	spec := tools[0].(map[string]interface{})["toolSpecification"].(map[string]interface{})
	schema := spec["inputSchema"].(map[string]interface{})["json"].(map[string]interface{})
	props := schema["properties"].(map[string]interface{})
	value := props["value"].(map[string]interface{})
	ref, hasRef := value["$ref"]
	if !hasRef {
		t.Fatalf("expected $ref to be preserved, got: %v", value)
	}
	if ref != "#/definitions/Whatever" {
		t.Errorf("unexpected $ref value: %v", ref)
	}
}
