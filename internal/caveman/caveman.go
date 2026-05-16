// Package caveman implements the Caveman Mode token-saver: inject a terse-
// style instruction into the system message so the model produces ~65%
// fewer output tokens without losing technical substance.
//
// Adapted from 9router/open-sse/rtk/caveman.js + cavemanPrompts.js, which
// in turn adapt the Caveman skill (https://github.com/JuliusBrussee/caveman).
//
// Design notes for LIAM:
//
//  1. Caveman runs at the OpenAI canonical layer (before any provider
//     translateRequest), so it works transparently for Kiro and AG today
//     and any provider added later. The injection target is OpenAI-shaped
//     `messages[]` with a `role:"system"` entry.
//
//  2. We APPEND to existing system content rather than prepend or replace.
//     This keeps the user's developer instructions (and the LIAM overlay
//     for Kiro, which lives downstream of caveman) above the caveman
//     prompt, preserving role/persona/format guidance precedence.
//
//  3. If the request has no system message, we insert one at the front
//     of `messages`. This is the same pattern 9router uses.
//
//  4. Caveman is OFF by default in LIAM. RTK is invisible (just
//     compresses request bytes), but Caveman changes the model's *style*
//     — terse, telegraphic. Users not expecting that find it jarring,
//     so opt-in only.
package caveman

import (
	"strings"
)

// Level is the caveman intensity. Higher = terser output.
//
//	LevelLite  : drop filler, keep grammar
//	LevelFull  : drop articles, fragments OK, short synonyms
//	LevelUltra : telegraphic, abbreviations, arrow notation
type Level string

const (
	LevelLite  Level = "lite"
	LevelFull  Level = "full"
	LevelUltra Level = "ultra"
)

// IsValidLevel reports whether s names a caveman level we know.
func IsValidLevel(s string) bool {
	switch Level(s) {
	case LevelLite, LevelFull, LevelUltra:
		return true
	}
	return false
}

// sharedBoundaries spell out which content stays untouched by the caveman
// style. Code blocks, paths, errors, security warnings — anything where a
// terse rewrite would lose information. These boundaries appear in every
// level so the model always knows what NOT to compress.
const sharedBoundaries = "Code blocks, file paths, commands, errors, URLs: keep exact. " +
	"Security warnings, irreversible action confirmations, multi-step " +
	"ordered sequences: write normal. Resume terse style after."

// prompts maps each level to the instruction string injected into the
// system message. Verbatim ports of cavemanPrompts.js so terseness
// behaviour matches across LIAM / 9router.
var prompts = map[Level]string{
	LevelLite: strings.Join([]string{
		"Respond tersely. Keep grammar and full sentences but drop filler, hedging and pleasantries (just/really/basically/sure/of course/I'd be happy to).",
		"Pattern: state the thing, the action, the reason. Then next step.",
		sharedBoundaries,
		"Active every response until user asks for normal mode.",
	}, " "),
	LevelFull: strings.Join([]string{
		"Respond like terse caveman. All technical substance stay exact, only fluff die.",
		"Drop: articles (a/an/the), filler (just/really/basically/actually/simply), pleasantries, hedging. Fragments OK. Short synonyms (big not extensive, fix not implement a solution for).",
		"Pattern: [thing] [action] [reason]. [next step].",
		sharedBoundaries,
		"Active every response until user asks for normal mode.",
	}, " "),
	LevelUltra: strings.Join([]string{
		"Respond ultra-terse. Maximum compression. Telegraphic.",
		"Abbreviate (DB/auth/config/req/res/fn/impl), strip conjunctions, use arrows for causality (X → Y). One word when one word enough.",
		"Pattern: [thing] → [result]. [fix].",
		sharedBoundaries,
		"Active every response until user asks for normal mode.",
	}, " "),
}

// PromptFor returns the caveman instruction for the given level.
// Returns "" when level is unknown — caller should treat as no-op.
func PromptFor(level Level) string {
	return prompts[level]
}

// Inject appends the caveman prompt to the OpenAI-shaped `body.messages`
// array. Mutates `body` in place. No-op when level is unknown, body is
// nil, or messages array missing.
//
// We always operate on the OpenAI shape: any provider-specific
// translateRequest downstream sees the caveman instruction as part of
// the system message and routes it correctly into Claude `system` /
// Gemini `systemInstruction` / Kiro overlay. That's the whole point of
// running caveman *before* translation.
func Inject(body map[string]any, level Level) bool {
	prompt := prompts[level]
	if body == nil || prompt == "" {
		return false
	}
	msgsAny, ok := body["messages"]
	if !ok {
		return false
	}
	msgs, ok := msgsAny.([]any)
	if !ok {
		return false
	}

	// Find existing system OR developer message and append.
	for i, raw := range msgs {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		if role != "system" && role != "developer" {
			continue
		}
		appendToOpenAIMessage(m, prompt)
		msgs[i] = m
		body["messages"] = msgs
		return true
	}

	// No system message yet — prepend a fresh one.
	newMsg := map[string]any{
		"role":    "system",
		"content": prompt,
	}
	body["messages"] = append([]any{newMsg}, msgs...)
	return true
}

const sep = "\n\n"

// appendToOpenAIMessage appends `prompt` to the message's content,
// handling all three OpenAI content shapes (string, array of parts, or
// nil).
func appendToOpenAIMessage(m map[string]any, prompt string) {
	switch c := m["content"].(type) {
	case string:
		if c == "" {
			m["content"] = prompt
		} else {
			m["content"] = c + sep + prompt
		}
	case []any:
		// Responses-style: array of {type:"input_text"|"text", text:...}
		c = append(c, map[string]any{"type": "input_text", "text": prompt})
		m["content"] = c
	default:
		m["content"] = prompt
	}
}
