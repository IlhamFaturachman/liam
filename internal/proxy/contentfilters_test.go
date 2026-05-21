package proxy

import "testing"

func TestContentFilter_ExactMatch(t *testing.T) {
	req := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "Powerful AI Agent here"},
		},
	}
	rules := []contentFilterRule{
		{
			PatternType:     "exact",
			Pattern:         "Powerful AI Agent",
			Replacement:     "Advanced AI Agent",
			CaseInsensitive: false,
			Enabled:         true,
			Position:        1,
		},
	}
	compiled, err := compileContentFilterRules(rules)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	changed := applyContentFiltersToRequest(req, "local", compiled)
	if !changed {
		t.Fatalf("expected request mutation")
	}
	got := req["messages"].([]any)[0].(map[string]any)["content"].(string)
	if got != "Advanced AI Agent here" {
		t.Fatalf("unexpected content: %q", got)
	}
}

func TestContentFilter_CaseInsensitiveExact(t *testing.T) {
	req := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "x-anthropic-billing-header"},
		},
	}
	rules := []contentFilterRule{
		{
			PatternType:     "exact",
			Pattern:         "X-Anthropic-Billing-Header",
			Replacement:     "",
			CaseInsensitive: true,
			Enabled:         true,
			Position:        1,
		},
	}
	compiled, err := compileContentFilterRules(rules)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	changed := applyContentFiltersToRequest(req, "local", compiled)
	if !changed {
		t.Fatalf("expected request mutation")
	}
	got := req["messages"].([]any)[0].(map[string]any)["content"].(string)
	if got != "" {
		t.Fatalf("unexpected content: %q", got)
	}
}

func TestContentFilter_Regex(t *testing.T) {
	req := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "user_123 and user_456"},
		},
	}
	rules := []contentFilterRule{
		{
			PatternType:     "regex",
			Pattern:         `user_\d+`,
			Replacement:     "user_redacted",
			CaseInsensitive: false,
			Enabled:         true,
			Position:        1,
		},
	}
	compiled, err := compileContentFilterRules(rules)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	changed := applyContentFiltersToRequest(req, "local", compiled)
	if !changed {
		t.Fatalf("expected request mutation")
	}
	got := req["messages"].([]any)[0].(map[string]any)["content"].(string)
	if got != "user_redacted and user_redacted" {
		t.Fatalf("unexpected content: %q", got)
	}
}

func TestContentFilter_FirstMatchWins(t *testing.T) {
	req := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "abc"},
		},
	}
	rules := []contentFilterRule{
		{
			PatternType:     "exact",
			Pattern:         "abc",
			Replacement:     "def",
			CaseInsensitive: false,
			Enabled:         true,
			Position:        1,
		},
		{
			PatternType:     "exact",
			Pattern:         "def",
			Replacement:     "xyz",
			CaseInsensitive: false,
			Enabled:         true,
			Position:        2,
		},
	}
	compiled, err := compileContentFilterRules(rules)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	changed := applyContentFiltersToRequest(req, "local", compiled)
	if !changed {
		t.Fatalf("expected request mutation")
	}
	got := req["messages"].([]any)[0].(map[string]any)["content"].(string)
	if got != "def" {
		t.Fatalf("unexpected content: %q", got)
	}
}

func TestContentFilter_TargetsAndSafety(t *testing.T) {
	req := map[string]any{
		"model": "kr/claude-opus-4.7",
		"messages": []any{
			map[string]any{"role": "system", "content": "token=SECRET"},
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "token=SECRET"},
					map[string]any{"type": "tool_result", "content": "token=SECRET"},
				},
			},
			map[string]any{
				"role":         "tool",
				"tool_call_id": "call_1",
				"content":      "token=SECRET",
			},
			map[string]any{
				"role": "assistant",
				"tool_calls": []any{
					map[string]any{
						"id":   "call_1",
						"type": "function",
						"function": map[string]any{
							"name":      "search",
							"arguments": `{"apiKey":"token=SECRET"}`,
						},
					},
				},
			},
		},
		"tools": []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": "search",
					"parameters": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"apiKey": map[string]any{"type": "string", "description": "token=SECRET"},
						},
					},
				},
			},
		},
	}
	rules := []contentFilterRule{
		{
			PatternType:     "exact",
			Pattern:         "token=SECRET",
			Replacement:     "token=REDACTED",
			CaseInsensitive: false,
			Enabled:         true,
			Position:        1,
		},
	}
	compiled, err := compileContentFilterRules(rules)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	changed := applyContentFiltersToRequest(req, "local", compiled)
	if !changed {
		t.Fatalf("expected request mutation")
	}

	msgs := req["messages"].([]any)
	if got := msgs[0].(map[string]any)["content"].(string); got != "token=REDACTED" {
		t.Fatalf("system content not filtered: %q", got)
	}
	userParts := msgs[1].(map[string]any)["content"].([]any)
	if got := userParts[0].(map[string]any)["text"].(string); got != "token=REDACTED" {
		t.Fatalf("user text part not filtered: %q", got)
	}
	if got := userParts[1].(map[string]any)["content"].(string); got != "token=REDACTED" {
		t.Fatalf("tool_result content not filtered: %q", got)
	}
	if got := msgs[2].(map[string]any)["content"].(string); got != "token=REDACTED" {
		t.Fatalf("tool message content not filtered: %q", got)
	}

	toolCalls := msgs[3].(map[string]any)["tool_calls"].([]any)
	gotArgs := toolCalls[0].(map[string]any)["function"].(map[string]any)["arguments"].(string)
	if gotArgs != `{"apiKey":"token=SECRET"}` {
		t.Fatalf("tool call args should be untouched, got: %q", gotArgs)
	}
	gotModel := req["model"].(string)
	if gotModel != "kr/claude-opus-4.7" {
		t.Fatalf("model should be untouched, got: %q", gotModel)
	}
	gotSchemaDesc := req["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["parameters"].(map[string]any)["properties"].(map[string]any)["apiKey"].(map[string]any)["description"].(string)
	if gotSchemaDesc != "token=SECRET" {
		t.Fatalf("tool schema should be untouched, got: %q", gotSchemaDesc)
	}
}

func TestContentFilter_ModeOffNoChange(t *testing.T) {
	req := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "Powerful AI Agent"},
		},
	}
	rules := []contentFilterRule{
		{
			PatternType:     "exact",
			Pattern:         "Powerful AI Agent",
			Replacement:     "Advanced AI Agent",
			CaseInsensitive: false,
			Enabled:         true,
			Position:        1,
		},
	}
	compiled, err := compileContentFilterRules(rules)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	changed := applyContentFiltersToRequest(req, "off", compiled)
	if changed {
		t.Fatalf("mode off should not mutate request")
	}
	got := req["messages"].([]any)[0].(map[string]any)["content"].(string)
	if got != "Powerful AI Agent" {
		t.Fatalf("unexpected content: %q", got)
	}
}

func TestContentFilter_RejectInvalidRegex(t *testing.T) {
	rules := []contentFilterRule{
		{
			PatternType:     "regex",
			Pattern:         "(unclosed",
			Replacement:     "x",
			CaseInsensitive: false,
			Enabled:         true,
			Position:        1,
		},
	}
	if _, err := compileContentFilterRules(rules); err == nil {
		t.Fatalf("expected regex compile error")
	}
}
