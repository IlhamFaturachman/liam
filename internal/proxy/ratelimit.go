package proxy

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Backoff schedule constants — match 9router/open-sse/config/errorConfig.js
// BACKOFF_CONFIG = { base: 2000ms, max: 5min, maxLevel: 15 }.
//
// Replaces the previous 4-category Categorize429 state machine that put
// accounts on long cooldowns + per-(account, model) locks for any
// unrecognised 429 — that caused single-account users to 503 within
// minutes of normal coding.
const (
	BackoffBaseMs       = 2000
	BackoffMaxMs        = 5 * 60 * 1000
	BackoffMaxLevel     = 15
	TransientCooldownMs = 30 * 1000
	LongCooldownMs      = 2 * 60 * 1000
	ShortCooldownMs     = 5 * 1000

	// Hard cap on Retry-After / provider-supplied cooldowns so a wildly
	// long upstream value (e.g. codex resets_at 6h) doesn't take an
	// account out of rotation for the rest of the session.
	MaxRateLimitCooldownMs = 30 * 60 * 1000
)

// ErrorDecision describes how the proxy should handle an upstream failure.
//
//   - UseBackoff = true: bump the per-account backoff_level via
//     db.BumpBackoff and use BackoffCooldown(level) for the wait.
//   - UseBackoff = false: apply CooldownMs as a fixed cooldown via
//     db.MarkAccountError.
//
// In both cases the caller switches to a different account when more
// than one is registered for the provider, and otherwise sleeps in
// place and retries the same account.
type ErrorDecision struct {
	UseBackoff bool
	CooldownMs time.Duration
	Reason     string
}

// errorRule mirrors one entry of 9router's ERROR_RULES table. Order
// matters: text rules are evaluated first (high → low priority), then
// status rules as a fallback.
type errorRule struct {
	text       string
	status     int
	backoff    bool
	cooldownMs int
}

// errorRules ports 9router/open-sse/config/errorConfig.js::ERROR_RULES
// 1:1. Keep these aligned: any change to upstream behaviour should be
// reflected in both forks.
var errorRules = []errorRule{
	// --- Text-based rules (checked first, order = priority) ---
	{text: "no credentials", cooldownMs: LongCooldownMs},
	{text: "request not allowed", cooldownMs: ShortCooldownMs},
	// "Improperly formed request" is a payload-shape problem (translator
	// bug, malformed multimodal input, oversized history). Cooling the
	// account down for 2 minutes was way too aggressive — single-account
	// users got locked out and the retry loop just hit the same account
	// after sleeping. Drop to a short 5s window so the account recovers
	// fast and the loop has a real chance to surface the underlying
	// error to the caller.
	{text: "improperly formed request", cooldownMs: ShortCooldownMs},
	{text: "rate limit", backoff: true},
	{text: "too many requests", backoff: true},
	{text: "quota exceeded", backoff: true},
	{text: "capacity", backoff: true},
	{text: "overloaded", backoff: true},

	// --- Status-based rules (fallback when text doesn't match) ---
	{status: 401, cooldownMs: LongCooldownMs},
	{status: 402, cooldownMs: LongCooldownMs},
	{status: 403, cooldownMs: LongCooldownMs},
	{status: 404, cooldownMs: LongCooldownMs},
	{status: 429, backoff: true},
}

// ClassifyError classifies an upstream failure into a backoff/cooldown
// decision. Matches text rules first, then status rules. Anything that
// doesn't match falls back to a 30 s transient cooldown so unknown
// failure modes still rotate accounts without permanently sidelining
// any single one.
//
// The Retry-After header (if present and parseable) overrides the
// rule's fixed cooldown — providers know best how long until retry is
// safe. We still cap at MaxRateLimitCooldownMs.
func ClassifyError(status int, body []byte, headers http.Header) ErrorDecision {
	bodyStr := strings.ToLower(string(body))

	for _, rule := range errorRules {
		if rule.text != "" && strings.Contains(bodyStr, rule.text) {
			return ruleToDecision(rule, headers)
		}
		if rule.status != 0 && rule.status == status {
			return ruleToDecision(rule, headers)
		}
	}

	// Default: transient cooldown for any unmatched error, including
	// 5xx upstream failures and network blips. Caller still rotates to
	// the next account.
	return ErrorDecision{
		UseBackoff: false,
		CooldownMs: time.Duration(TransientCooldownMs) * time.Millisecond,
		Reason:     "transient",
	}
}

func ruleToDecision(rule errorRule, headers http.Header) ErrorDecision {
	reason := ruleReason(rule)

	if rule.backoff {
		// Backoff rules ignore Retry-After: the per-account level is
		// the authority. The caller computes the wait after bumping
		// the level via BackoffCooldown.
		return ErrorDecision{UseBackoff: true, Reason: reason}
	}

	cooldown := time.Duration(rule.cooldownMs) * time.Millisecond
	if ra := headers.Get("Retry-After"); ra != "" {
		if d := parseRetryAfter(ra); d > 0 {
			capDuration := time.Duration(MaxRateLimitCooldownMs) * time.Millisecond
			if d > capDuration {
				d = capDuration
			}
			cooldown = d
		}
	}
	return ErrorDecision{UseBackoff: false, CooldownMs: cooldown, Reason: reason}
}

func ruleReason(rule errorRule) string {
	if rule.text != "" {
		return rule.text
	}
	if rule.status != 0 {
		return "status_" + strconv.Itoa(rule.status)
	}
	return "transient"
}

// BackoffCooldown returns the exponential cooldown for a given backoff
// level. Mirrors 9router's getQuotaCooldown:
//
//	level 1: 2 s, 2: 4 s, 3: 8 s, 4: 16 s, …, capped at 5 min.
//
// Level 0 (or below) is treated as level 1 — callers should always
// bump the level before computing the wait.
func BackoffCooldown(level int) time.Duration {
	if level < 1 {
		level = 1
	}
	if level > BackoffMaxLevel {
		level = BackoffMaxLevel
	}
	ms := BackoffBaseMs
	for i := 1; i < level; i++ {
		ms *= 2
		if ms >= BackoffMaxMs {
			return time.Duration(BackoffMaxMs) * time.Millisecond
		}
	}
	return time.Duration(ms) * time.Millisecond
}

// parseRetryAfter accepts seconds (integer or float), milliseconds
// (float < 1000), or HTTP date. Returns 0 if unparseable.
func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if secs, err := strconv.Atoi(value); err == nil {
		return time.Duration(secs) * time.Second
	}
	if f, err := strconv.ParseFloat(value, 64); err == nil {
		if f < 1000 {
			return time.Duration(f*1000) * time.Millisecond
		}
		return time.Duration(f) * time.Millisecond
	}
	if t, err := http.ParseTime(value); err == nil {
		d := time.Until(t)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}
