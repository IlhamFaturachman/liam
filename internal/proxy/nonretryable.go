package proxy

import (
	"bytes"
	"strings"
)

// Non-retryable error keywords — if response contains these, don't retry
// (all accounts will fail with the same input)
var nonRetryableKeywords = []string{
	"CONTENT_LENGTH_EXCEEDS_THRESHOLD",
	"INVALID_MODEL",
	"invalid_model",
	"model_not_found",
	"Model not found",
	"context_length_exceeded",
	"invalid_request_error",
	"maximum context length",
	"content_policy_violation",
	"invalid_api_key",
	"billing_not_active",
	"unsupported_model",
}

// Non-retryable status codes (when combined with error keywords)
var nonRetryableStatuses = map[int]bool{
	400: true, // Bad request (input error)
	404: true, // Not found (model doesn't exist)
	422: true, // Unprocessable (validation error)
}

// IsNonRetryable checks if an error response should NOT be retried
// Returns true if the error is caused by the request itself (not the account)
func IsNonRetryable(statusCode int, body []byte) bool {
	if !nonRetryableStatuses[statusCode] {
		return false
	}

	bodyLower := bytes.ToLower(body)
	for _, keyword := range nonRetryableKeywords {
		if bytes.Contains(bodyLower, bytes.ToLower([]byte(keyword))) {
			return true
		}
	}
	return false
}

// ExtractErrorMessage pulls a human-readable error from response body
func ExtractErrorMessage(body []byte) string {
	s := string(body)
	// Try to find "message" field
	if idx := strings.Index(s, `"message"`); idx >= 0 {
		// Find the value after ":"
		rest := s[idx+9:]
		if qIdx := strings.Index(rest, `"`); qIdx >= 0 {
			rest = rest[qIdx+1:]
			if endIdx := strings.Index(rest, `"`); endIdx >= 0 {
				msg := rest[:endIdx]
				if len(msg) > 200 {
					return msg[:200] + "..."
				}
				return msg
			}
		}
	}
	// Fallback: return first 200 chars
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}
