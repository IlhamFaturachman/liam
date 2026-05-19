package kiro

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/liam-auto/liam/internal/db"
	"golang.org/x/sync/singleflight"
)

// Kiro desktop auth endpoint (doesn't require clientId/clientSecret)
const kiroDesktopRefreshURL = "https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken"

// RefreshErrorReason classifies why a refresh failed. Mirrors the AG-side
// taxonomy so the worker can use one quarantine path for both providers.
type RefreshErrorReason string

const (
	RefreshErrorTransient    RefreshErrorReason = "transient"
	RefreshErrorInvalidGrant RefreshErrorReason = "invalid_grant"
	RefreshErrorParse        RefreshErrorReason = "parse"
)

// RefreshError wraps a Kiro refresh failure with a structured reason.
type RefreshError struct {
	Reason  RefreshErrorReason
	Message string
}

func (e *RefreshError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return string(e.Reason)
	}
	return string(e.Reason) + ": " + e.Message
}

// IsUnrecoverable returns true when the refresh_token is permanently dead.
func (e *RefreshError) IsUnrecoverable() bool {
	if e == nil {
		return false
	}
	return e.Reason == RefreshErrorInvalidGrant
}

// AsRefreshError extracts a *RefreshError from any error, or returns nil.
func AsRefreshError(err error) *RefreshError {
	if err == nil {
		return nil
	}
	if re, ok := err.(*RefreshError); ok {
		return re
	}
	return nil
}

// refreshGroup deduplicates concurrent refresh calls per refresh_token. Kiro's
// auth endpoint is more tolerant than Google's (rotates refresh_token on every
// successful refresh, no `refresh_token_reused` family revoke), but two
// parallel refreshes still race on which rotated refresh_token wins — the
// loser's stored refresh_token is invalidated. singleflight keeps it serial.
var refreshGroup singleflight.Group

// RefreshToken refreshes a Kiro access token using the desktop auth endpoint.
//
// Kiro's response includes a fresh refresh_token on every successful call.
// We MUST capture it (already does); singleflight prevents the parallel-race
// where two callers both store their own rotated token and one becomes stale.
func RefreshToken(account *db.Account) (*KiroCredentials, error) {
	var creds KiroCredentials
	if err := json.Unmarshal(account.Credentials, &creds); err != nil {
		return nil, &RefreshError{Reason: RefreshErrorParse, Message: "parse credentials: " + err.Error()}
	}

	if creds.RefreshToken == "" {
		return nil, &RefreshError{Reason: RefreshErrorInvalidGrant, Message: "no refresh_token stored"}
	}

	v, err, _ := refreshGroup.Do(creds.RefreshToken, func() (interface{}, error) {
		return doRefresh(&creds)
	})
	if err != nil {
		return nil, err
	}
	out := v.(KiroCredentials)
	return &out, nil
}

func doRefresh(creds *KiroCredentials) (KiroCredentials, error) {
	body, _ := json.Marshal(map[string]string{
		"refreshToken": creds.RefreshToken,
	})

	req, err := http.NewRequest("POST", kiroDesktopRefreshURL, strings.NewReader(string(body)))
	if err != nil {
		return KiroCredentials{}, &RefreshError{Reason: RefreshErrorTransient, Message: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return KiroCredentials{}, &RefreshError{Reason: RefreshErrorTransient, Message: "network: " + err.Error()}
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		// Kiro returns 400/401 with a body like {"message":"..."} when the
		// refresh_token is dead. 5xx is transient. Be conservative: only
		// classify 4xx as InvalidGrant.
		reason := RefreshErrorTransient
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			reason = RefreshErrorInvalidGrant
		}
		return KiroCredentials{}, &RefreshError{
			Reason:  reason,
			Message: fmt.Sprintf("status %d: %s", resp.StatusCode, truncate(string(respBody), 200)),
		}
	}

	var tokenResp struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ProfileArn   string `json:"profileArn"`
		ExpiresIn    int    `json:"expiresIn"`
	}
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		return KiroCredentials{}, &RefreshError{Reason: RefreshErrorParse, Message: "decode response: " + err.Error()}
	}

	if tokenResp.AccessToken == "" {
		return KiroCredentials{}, &RefreshError{Reason: RefreshErrorParse, Message: "empty accessToken in 200 response"}
	}

	out := *creds
	out.AccessToken = tokenResp.AccessToken
	if tokenResp.RefreshToken != "" {
		out.RefreshToken = tokenResp.RefreshToken
	}
	if tokenResp.ProfileArn != "" {
		out.ProfileARN = tokenResp.ProfileArn
	}
	expiresIn := tokenResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	out.ExpiresAt = time.Now().UTC().Add(time.Duration(expiresIn) * time.Second).Format(time.RFC3339)

	return out, nil
}

// IsTokenExpired checks if the access token is expired or expiring soon
func IsTokenExpired(creds *KiroCredentials, leadMinutes int) bool {
	if creds.ExpiresAt == "" {
		return true
	}
	expiresAt, err := time.Parse(time.RFC3339, creds.ExpiresAt)
	if err != nil {
		expiresAt, err = time.Parse(time.RFC3339Nano, creds.ExpiresAt)
		if err != nil {
			return true
		}
	}
	threshold := time.Now().UTC().Add(time.Duration(leadMinutes) * time.Minute)
	return expiresAt.Before(threshold)
}

// QuotaBreakdownEntry represents per-resource usage info parsed from
// the CodeWhisperer / Q getUsageLimits response.
type QuotaBreakdownEntry struct {
	Used    float64 `json:"used"`
	Total   float64 `json:"total"`
	ResetAt string  `json:"reset_at,omitempty"`
	Label   string  `json:"label,omitempty"`
}

// QuotaResult holds the result of a quota fetch
type QuotaResult struct {
	Used      int                            `json:"used"`
	Total     int                            `json:"total"`
	ResetAt   string                         `json:"reset_at"` // ISO timestamp
	Plan      string                         `json:"plan"`     // "Kiro Pro", "Kiro Free", etc.
	Breakdown map[string]QuotaBreakdownEntry `json:"breakdown,omitempty"`
}

// QuotaErrorReason classifies why a quota fetch failed so callers can decide
// whether to refresh tokens, surface an info message, or fall back gracefully.
type QuotaErrorReason string

const (
	QuotaErrorNone        QuotaErrorReason = ""
	QuotaErrorAuthExpired QuotaErrorReason = "auth_expired"
	QuotaErrorNetwork     QuotaErrorReason = "network"
	QuotaErrorEmpty       QuotaErrorReason = "empty"
)

// QuotaError wraps a quota fetch failure with a structured reason.
type QuotaError struct {
	Reason  QuotaErrorReason
	Message string
}

func (e *QuotaError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return string(e.Reason)
	}
	return e.Message
}

// DefaultProfileARN matches 9router's DEFAULT_PROFILE_ARN fallback. It lets
// the GetUsageLimits POST/Q endpoints succeed when an account has no
// profileArn yet (e.g. social-auth tokens that never saw loadCodeAssist).
const DefaultProfileARN = "arn:aws:codewhisperer:us-east-1:638616132270:profile/AAAACCCCXXXX"

// FetchQuota fetches usage limits from Kiro API. It always returns a
// populated QuotaResult on success, or a *QuotaError describing why no
// quota was available.
func FetchQuota(accessToken, profileARN string) (*QuotaResult, error) {
	if accessToken == "" {
		return nil, &QuotaError{Reason: QuotaErrorAuthExpired, Message: "no access token"}
	}

	// Fall back to the shared default profileArn (matches 9router behaviour)
	// so quota fetch still works for social-auth/imported tokens that never
	// completed loadCodeAssist.
	effectiveProfileARN := strings.TrimSpace(profileARN)
	if effectiveProfileARN == "" {
		effectiveProfileARN = DefaultProfileARN
	}

	client := &http.Client{Timeout: 15 * time.Second}

	type attempt struct {
		name  string
		doReq func() (*http.Request, error)
	}

	attempts := []attempt{
		{
			name: "codewhisperer-get",
			doReq: func() (*http.Request, error) {
				req, err := http.NewRequest("GET",
					"https://codewhisperer.us-east-1.amazonaws.com/getUsageLimits?isEmailRequired=true&origin=AI_EDITOR&resourceType=AGENTIC_REQUEST", nil)
				if err != nil {
					return nil, err
				}
				req.Header.Set("Authorization", "Bearer "+accessToken)
				req.Header.Set("Accept", "application/json")
				req.Header.Set("x-amz-user-agent", "aws-sdk-js/1.0.0 KiroIDE")
				req.Header.Set("user-agent", "aws-sdk-js/1.0.0 KiroIDE")
				return req, nil
			},
		},
		{
			name: "codewhisperer-post",
			doReq: func() (*http.Request, error) {
				body := fmt.Sprintf(`{"origin":"AI_EDITOR","profileArn":"%s","resourceType":"AGENTIC_REQUEST"}`, effectiveProfileARN)
				req, err := http.NewRequest("POST", "https://codewhisperer.us-east-1.amazonaws.com", strings.NewReader(body))
				if err != nil {
					return nil, err
				}
				req.Header.Set("Authorization", "Bearer "+accessToken)
				req.Header.Set("Content-Type", "application/x-amz-json-1.0")
				req.Header.Set("x-amz-target", "AmazonCodeWhispererService.GetUsageLimits")
				req.Header.Set("Accept", "application/json")
				return req, nil
			},
		},
		{
			name: "q-get",
			doReq: func() (*http.Request, error) {
				url := fmt.Sprintf("https://q.us-east-1.amazonaws.com/getUsageLimits?origin=AI_EDITOR&profileArn=%s&resourceType=AGENTIC_REQUEST", effectiveProfileARN)
				req, err := http.NewRequest("GET", url, nil)
				if err != nil {
					return nil, err
				}
				req.Header.Set("Authorization", "Bearer "+accessToken)
				req.Header.Set("Accept", "application/json")
				return req, nil
			},
		},
	}

	var sawAuthError bool
	var lastFailure string

	for _, a := range attempts {
		req, err := a.doReq()
		if err != nil {
			lastFailure = fmt.Sprintf("%s: %v", a.name, err)
			continue
		}

		resp, err := client.Do(req)
		if err != nil {
			lastFailure = fmt.Sprintf("%s: %v", a.name, err)
			continue
		}

		result, parseErr := parseQuotaResponse(resp)
		if parseErr != nil {
			if parseErr.Reason == QuotaErrorAuthExpired {
				sawAuthError = true
			}
			lastFailure = fmt.Sprintf("%s: %s", a.name, parseErr.Message)
			continue
		}
		if result != nil {
			return result, nil
		}
		// nil result with no error → empty body, try next endpoint.
		lastFailure = fmt.Sprintf("%s: empty body", a.name)
	}

	if sawAuthError {
		return nil, &QuotaError{Reason: QuotaErrorAuthExpired, Message: lastFailure}
	}
	if lastFailure == "" {
		lastFailure = "all quota endpoints failed"
	}
	return nil, &QuotaError{Reason: QuotaErrorNetwork, Message: lastFailure}
}

// parseQuotaResponse turns a single getUsageLimits HTTP response into a
// QuotaResult. It returns (nil, nil) when the status is 200 but the
// payload was empty (caller should try the next endpoint).
func parseQuotaResponse(resp *http.Response) (*QuotaResult, *QuotaError) {
	defer resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		body, _ := io.ReadAll(resp.Body)
		return nil, &QuotaError{
			Reason:  QuotaErrorAuthExpired,
			Message: fmt.Sprintf("status %d: %s", resp.StatusCode, truncate(string(body), 200)),
		}
	}

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, &QuotaError{
			Reason:  QuotaErrorNetwork,
			Message: fmt.Sprintf("status %d: %s", resp.StatusCode, truncate(string(body), 200)),
		}
	}

	var data struct {
		UsageBreakdownList []struct {
			ResourceType              string  `json:"resourceType"`
			CurrentUsageWithPrecision float64 `json:"currentUsageWithPrecision"`
			UsageLimitWithPrecision   float64 `json:"usageLimitWithPrecision"`
			FreeTrialInfo             *struct {
				CurrentUsageWithPrecision float64 `json:"currentUsageWithPrecision"`
				UsageLimitWithPrecision   float64 `json:"usageLimitWithPrecision"`
				FreeTrialExpiry           string  `json:"freeTrialExpiry"`
			} `json:"freeTrialInfo"`
		} `json:"usageBreakdownList"`
		NextDateReset    interface{} `json:"nextDateReset"`
		ResetDate        interface{} `json:"resetDate"`
		SubscriptionInfo *struct {
			SubscriptionTitle string `json:"subscriptionTitle"`
		} `json:"subscriptionInfo"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, &QuotaError{Reason: QuotaErrorNetwork, Message: "decode: " + err.Error()}
	}

	if len(data.UsageBreakdownList) == 0 {
		return nil, nil
	}

	resetAt := parseResetTime(data.NextDateReset)
	if resetAt == "" {
		resetAt = parseResetTime(data.ResetDate)
	}

	plan := "Kiro"
	if data.SubscriptionInfo != nil && strings.TrimSpace(data.SubscriptionInfo.SubscriptionTitle) != "" {
		plan = data.SubscriptionInfo.SubscriptionTitle
	}

	breakdown := make(map[string]QuotaBreakdownEntry, len(data.UsageBreakdownList))
	var primary *QuotaBreakdownEntry

	for _, b := range data.UsageBreakdownList {
		key := strings.ToLower(strings.TrimSpace(b.ResourceType))
		if key == "" {
			key = "unknown"
		}

		entry := QuotaBreakdownEntry{
			Used:    b.CurrentUsageWithPrecision,
			Total:   b.UsageLimitWithPrecision,
			ResetAt: resetAt,
		}
		breakdown[key] = entry

		// AGENTIC_REQUEST is the headline resource that drives the
		// summary used/total/reset_at returned at the top level. If
		// it's absent we fall back to whatever appears first.
		if key == "agentic_request" {
			e := entry
			primary = &e
		} else if primary == nil {
			e := entry
			primary = &e
		}

		if b.FreeTrialInfo != nil {
			freeReset := parseResetTime(b.FreeTrialInfo.FreeTrialExpiry)
			if freeReset == "" {
				freeReset = resetAt
			}
			breakdown[key+"_freetrial"] = QuotaBreakdownEntry{
				Used:    b.FreeTrialInfo.CurrentUsageWithPrecision,
				Total:   b.FreeTrialInfo.UsageLimitWithPrecision,
				ResetAt: freeReset,
			}
		}
	}

	if primary == nil {
		// Defensive: should not happen because we already checked length.
		return nil, &QuotaError{Reason: QuotaErrorEmpty, Message: "no usable breakdown entries"}
	}

	return &QuotaResult{
		Used:      int(primary.Used),
		Total:     int(primary.Total),
		ResetAt:   resetAt,
		Plan:      plan,
		Breakdown: breakdown,
	}, nil
}

// truncate keeps log messages short and safe for upstream API errors.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// parseResetTime converts various time formats to ISO string
func parseResetTime(v interface{}) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		if val == "" {
			return ""
		}
		// Try parse as ISO
		if t, err := time.Parse(time.RFC3339, val); err == nil {
			return t.Format(time.RFC3339)
		}
		// Try as numeric string (unix timestamp)
		return val
	case float64:
		if val <= 0 {
			return ""
		}
		var ts int64
		if val < 1e12 {
			ts = int64(val) * 1000 // seconds to ms
		} else {
			ts = int64(val)
		}
		return time.UnixMilli(ts).UTC().Format(time.RFC3339)
	}
	return ""
}
