package caveman

import (
	"strings"
	"testing"
)

// caveman_test.go covers all body shapes Inject must handle plus the
// safety claims (don't touch user/assistant content, don't touch
// non-text parts, prepend system message when missing).

func TestInjectAppendsToStringSystem(t *testing.T) {
	body := map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": "You are helpful."},
			map[string]any{"role": "user", "content": "hello"},
		},
	}
	if !Inject(body, LevelLite) {
		t.Fatalf("inject returned false")
	}
	msgs := body["messages"].([]any)
	sys := msgs[0].(map[string]any)["content"].(string)
	if !strings.HasPrefix(sys, "You are helpful.") {
		t.Fatalf("original system content lost: %q", sys)
	}
	if !strings.Contains(sys, "Respond tersely") {
		t.Fatalf("caveman lite prompt missing: %q", sys)
	}
}

func TestInjectAppendsToArraySystem(t *testing.T) {
	body := map[string]any{
		"messages": []any{
			map[string]any{
				"role": "system",
				"content": []any{
					map[string]any{"type": "input_text", "text": "Be concise."},
				},
			},
		},
	}
	if !Inject(body, LevelFull) {
		t.Fatalf("inject returned false")
	}
	msgs := body["messages"].([]any)
	parts := msgs[0].(map[string]any)["content"].([]any)
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(parts))
	}
	last := parts[1].(map[string]any)
	if last["type"] != "input_text" {
		t.Fatalf("expected input_text part, got %v", last["type"])
	}
	text := last["text"].(string)
	if !strings.Contains(text, "Respond like terse caveman") {
		t.Fatalf("caveman full prompt missing: %q", text)
	}
}

func TestInjectPrependsWhenSystemMissing(t *testing.T) {
	body := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
	}
	if !Inject(body, LevelUltra) {
		t.Fatalf("inject returned false")
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages after prepend, got %d", len(msgs))
	}
	first := msgs[0].(map[string]any)
	if first["role"] != "system" {
		t.Fatalf("expected first message to be system, got %v", first["role"])
	}
	content := first["content"].(string)
	if !strings.Contains(content, "Respond ultra-terse") {
		t.Fatalf("caveman ultra prompt missing: %q", content)
	}
}

func TestInjectAcceptsDeveloperRole(t *testing.T) {
	// OpenAI's "developer" role (responses API) is treated as system
	// by 9router; LIAM mirrors that behaviour.
	body := map[string]any{
		"messages": []any{
			map[string]any{"role": "developer", "content": "Use Go."},
			map[string]any{"role": "user", "content": "hi"},
		},
	}
	Inject(body, LevelLite)
	msgs := body["messages"].([]any)
	dev := msgs[0].(map[string]any)["content"].(string)
	if !strings.Contains(dev, "Respond tersely") {
		t.Fatalf("developer-role injection skipped: %q", dev)
	}
}

func TestInjectDoesNotTouchUserContent(t *testing.T) {
	// User and assistant messages are off-limits.
	body := map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": "sys"},
			map[string]any{"role": "user", "content": "user msg"},
			map[string]any{"role": "assistant", "content": "asst msg"},
		},
	}
	Inject(body, LevelLite)
	msgs := body["messages"].([]any)
	if msgs[1].(map[string]any)["content"] != "user msg" {
		t.Fatalf("user content mutated: %v", msgs[1])
	}
	if msgs[2].(map[string]any)["content"] != "asst msg" {
		t.Fatalf("assistant content mutated: %v", msgs[2])
	}
}

func TestInjectInvalidLevelNoop(t *testing.T) {
	body := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "hi"},
		},
	}
	if Inject(body, Level("garbage")) {
		t.Fatalf("inject should return false for unknown level")
	}
	if len(body["messages"].([]any)) != 1 {
		t.Fatalf("body mutated despite invalid level")
	}
}

func TestInjectNoMessagesArray(t *testing.T) {
	body := map[string]any{"input": "hello"}
	if Inject(body, LevelLite) {
		t.Fatalf("inject should return false when messages array missing")
	}
}

func TestInjectNilBody(t *testing.T) {
	if Inject(nil, LevelLite) {
		t.Fatalf("inject should return false for nil body")
	}
}

func TestPromptForKnownLevels(t *testing.T) {
	for _, lvl := range []Level{LevelLite, LevelFull, LevelUltra} {
		got := PromptFor(lvl)
		if got == "" {
			t.Fatalf("PromptFor(%q) returned empty", lvl)
		}
		if !strings.Contains(got, "Code blocks, file paths, commands") {
			t.Fatalf("PromptFor(%q) missing shared boundaries", lvl)
		}
	}
}

func TestIsValidLevel(t *testing.T) {
	cases := map[string]bool{
		"lite":   true,
		"full":   true,
		"ultra":  true,
		"":       false,
		"normal": false,
		"LITE":   false, // case-sensitive on purpose
		"foo":    false,
	}
	for in, want := range cases {
		if got := IsValidLevel(in); got != want {
			t.Fatalf("IsValidLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestInjectDoesNotDuplicate(t *testing.T) {
	// Calling Inject twice in the same request should produce two
	// copies of the prompt — that's expected behaviour, the caller
	// owns idempotency. We just verify it doesn't crash or corrupt.
	body := map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": "base"},
		},
	}
	Inject(body, LevelLite)
	Inject(body, LevelLite)
	sys := body["messages"].([]any)[0].(map[string]any)["content"].(string)
	if strings.Count(sys, "Respond tersely") != 2 {
		t.Fatalf("expected 2 caveman prompts after 2 injects, got: %q", sys)
	}
}

func TestInjectEmptyStringSystem(t *testing.T) {
	// System message exists but content is "". Should set content
	// directly instead of "" + sep + prompt.
	body := map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": ""},
		},
	}
	Inject(body, LevelLite)
	got := body["messages"].([]any)[0].(map[string]any)["content"].(string)
	if strings.HasPrefix(got, "\n\n") {
		t.Fatalf("empty-string system should NOT have leading separator: %q", got)
	}
	if !strings.HasPrefix(got, "Respond tersely") {
		t.Fatalf("expected prompt at start of empty-string content: %q", got)
	}
}
