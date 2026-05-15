package proxy

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RateLimitCategory classifies different types of rate limit responses
type RateLimitCategory int

const (
	// SoftRetry — transient blip, retry immediately same account
	SoftRetry RateLimitCategory = iota
	// InstantRetrySameAuth — tiny delay (100-500ms), same account
	InstantRetrySameAuth
	// ShortCooldownSwitch — 5-30s cooldown on this account, switch to another
	ShortCooldownSwitch
	// FullQuotaExhausted — 5-30min cooldown, account done for this model
	FullQuotaExhausted
)

// RateLimitDecision holds the categorization result
type RateLimitDecision struct {
	Category RateLimitCategory
	Cooldown time.Duration
	Reason   string
}

// Categorize429 analyzes a rate limit response and returns handling decision
func Categorize429(statusCode int, body []byte, headers http.Header) RateLimitDecision {
	bodyStr := strings.ToLower(string(body))

	// 1. Check for quota exhaustion keywords (most severe)
	if containsAny(bodyStr, "quota_exhausted", "resource_exhausted", "quota exceeded", "billing", "credits") {
		cooldown := parseRetryDuration(headers, bodyStr)
		if cooldown == 0 {
			cooldown = 5 * time.Minute
		}
		if cooldown > 30*time.Minute {
			cooldown = 30 * time.Minute
		}
		return RateLimitDecision{
			Category: FullQuotaExhausted,
			Cooldown: cooldown,
			Reason:   "quota_exhausted",
		}
	}

	// 2. Check Retry-After header
	if retryAfter := headers.Get("Retry-After"); retryAfter != "" {
		duration := parseRetryAfterHeader(retryAfter)
		if duration <= 500*time.Millisecond {
			return RateLimitDecision{Category: InstantRetrySameAuth, Cooldown: duration, Reason: "retry_after_short"}
		}
		if duration <= 30*time.Second {
			return RateLimitDecision{Category: ShortCooldownSwitch, Cooldown: duration, Reason: "retry_after_medium"}
		}
		return RateLimitDecision{Category: FullQuotaExhausted, Cooldown: duration, Reason: "retry_after_long"}
	}

	// 3. Check for soft rate limit keywords (least severe)
	if containsAny(bodyStr, "rate_limited", "rate limit", "too many requests", "slow down") &&
		!containsAny(bodyStr, "quota", "exhausted", "exceeded") {
		return RateLimitDecision{
			Category: SoftRetry,
			Cooldown: 100 * time.Millisecond,
			Reason:   "soft_rate_limit",
		}
	}

	// 4. Check for "reset after" time in body (AG-specific)
	if resetDuration := parseResetAfterFromBody(bodyStr); resetDuration > 0 {
		if resetDuration <= 10*time.Second {
			return RateLimitDecision{Category: ShortCooldownSwitch, Cooldown: resetDuration, Reason: "reset_after_short"}
		}
		return RateLimitDecision{Category: FullQuotaExhausted, Cooldown: resetDuration, Reason: "reset_after_long"}
	}

	// 5. Check x-ratelimit headers
	if remaining := headers.Get("x-ratelimit-remaining"); remaining == "0" {
		resetStr := headers.Get("x-ratelimit-reset")
		if resetStr != "" {
			ts, err := strconv.ParseInt(resetStr, 10, 64)
			if err == nil {
				cooldown := time.Until(time.Unix(ts, 0))
				if cooldown <= 0 {
					cooldown = 5 * time.Second
				}
				if cooldown <= 30*time.Second {
					return RateLimitDecision{Category: ShortCooldownSwitch, Cooldown: cooldown, Reason: "ratelimit_reset"}
				}
				return RateLimitDecision{Category: FullQuotaExhausted, Cooldown: cooldown, Reason: "ratelimit_reset_long"}
			}
		}
	}

	// 6. Default: short cooldown + switch
	return RateLimitDecision{
		Category: ShortCooldownSwitch,
		Cooldown: 10 * time.Second,
		Reason:   "unknown_429",
	}
}

// --- Helpers ---

func containsAny(s string, keywords ...string) bool {
	for _, kw := range keywords {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}

func parseRetryAfterHeader(value string) time.Duration {
	// Try seconds (integer)
	if secs, err := strconv.Atoi(value); err == nil {
		return time.Duration(secs) * time.Second
	}
	// Try milliseconds (float)
	if ms, err := strconv.ParseFloat(value, 64); err == nil {
		if ms < 1000 {
			return time.Duration(ms*1000) * time.Microsecond
		}
		return time.Duration(ms) * time.Millisecond
	}
	// Try HTTP date
	if t, err := http.ParseTime(value); err == nil {
		d := time.Until(t)
		if d < 0 {
			return 0
		}
		return d
	}
	return 5 * time.Second // Default
}

func parseResetAfterFromBody(body string) time.Duration {
	// Parse "reset after 2h7m23s" or "1h30m" or "45m" or "30s"
	idx := strings.Index(body, "reset after ")
	if idx == -1 {
		return 0
	}
	timeStr := body[idx+len("reset after "):]
	// Extract until non-time character
	end := 0
	for end < len(timeStr) && (timeStr[end] >= '0' && timeStr[end] <= '9' || timeStr[end] == 'h' || timeStr[end] == 'm' || timeStr[end] == 's') {
		end++
	}
	if end == 0 {
		return 0
	}
	timeStr = timeStr[:end]

	var total time.Duration
	var num int
	for _, c := range timeStr {
		if c >= '0' && c <= '9' {
			num = num*10 + int(c-'0')
		} else {
			switch c {
			case 'h':
				total += time.Duration(num) * time.Hour
			case 'm':
				total += time.Duration(num) * time.Minute
			case 's':
				total += time.Duration(num) * time.Second
			}
			num = 0
		}
	}
	return total
}

func parseRetryDuration(headers http.Header, body string) time.Duration {
	// Try Retry-After header first
	if ra := headers.Get("Retry-After"); ra != "" {
		return parseRetryAfterHeader(ra)
	}
	// Try body
	return parseResetAfterFromBody(body)
}
