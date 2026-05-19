package antigravity

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/liam-auto/liam/internal/config"
	"github.com/liam-auto/liam/internal/db"
	"golang.org/x/sync/singleflight"
)

// RefreshErrorReason classifies why a token refresh failed so callers can
// decide whether to retry, mark dead, or just back off. Mirrors 9router's
// `isUnrecoverableRefreshError` taxonomy (tokenRefresh.js:14-24).
type RefreshErrorReason string

const (
	// RefreshErrorTransient: network/5xx — safe to retry next tick.
	RefreshErrorTransient RefreshErrorReason = "transient"
	// RefreshErrorInvalidGrant: refresh_token revoked/expired/reused — DEAD.
	// Account should be quarantined (status=disabled) so we don't keep
	// hammering Google with a known-bad token (which makes our IP look
	// even more like an abusive bot).
	RefreshErrorInvalidGrant RefreshErrorReason = "invalid_grant"
	// RefreshErrorInvalidClient: client_id/client_secret wrong, or OAuth app
	// suspended. Affects ALL accounts equally — don't quarantine the account.
	RefreshErrorInvalidClient RefreshErrorReason = "invalid_client"
	// RefreshErrorParse: response decode failed (Google changed shape, or
	// CDN served HTML error page). Treat as transient.
	RefreshErrorParse RefreshErrorReason = "parse"
)

// RefreshError wraps a refresh failure with a structured reason so the
// worker can decide whether to disable the account or keep retrying.
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

// IsUnrecoverable returns true when the error means "this refresh_token
// will never work again" — caller should disable the account, not retry.
func (e *RefreshError) IsUnrecoverable() bool {
	if e == nil {
		return false
	}
	return e.Reason == RefreshErrorInvalidGrant
}

// AsRefreshError extracts a *RefreshError from any error, or returns nil.
// Lets callers do `if rerr := AsRefreshError(err); rerr != nil { … }` without
// importing errors.As at every call site.
func AsRefreshError(err error) *RefreshError {
	if err == nil {
		return nil
	}
	if re, ok := err.(*RefreshError); ok {
		return re
	}
	return nil
}

// refreshGroup deduplicates concurrent refresh calls keyed by refresh_token.
//
// Why: Google's OAuth backend treats two POSTs with the same refresh_token
// arriving in the same second as `refresh_token_reused` → revokes the entire
// token family (refresh + access). With 300 accounts this race is inevitable
// when the worker tick collides with on-demand refresh from handleRefreshQuota
// or the harvest re-import path. singleflight.Group collapses concurrent calls
// for the same key to a single upstream request.
//
// Mirrors 9router's `refreshPromiseCache` (tokenRefresh.js:9, 521-533) but uses
// Go-native singleflight which auto-cleans the key when the call resolves.
var refreshGroup singleflight.Group

// RefreshToken refreshes an expired Google OAuth access token.
//
// Behaviour:
//   - Preserves the existing refresh_token if Google doesn't return a new one
//     (Google rotates rarely, but when it does we MUST capture it — using the
//     old token after a rotate triggers `invalid_grant` and kills the account).
//   - Dedupes concurrent calls for the same refresh_token (see refreshGroup).
//   - Returns *RefreshError so the worker can distinguish "retry next tick"
//     from "this account is dead, quarantine it".
func RefreshToken(cfg *config.Config, account *db.Account) (*db.AGCredentials, error) {
	var creds db.AGCredentials
	if err := json.Unmarshal(account.Credentials, &creds); err != nil {
		return nil, &RefreshError{Reason: RefreshErrorParse, Message: "parse stored credentials: " + err.Error()}
	}

	if creds.RefreshToken == "" {
		// No refresh_token at all — can't recover. Treat as invalid_grant
		// so the worker quarantines the row instead of retrying forever.
		return nil, &RefreshError{Reason: RefreshErrorInvalidGrant, Message: "no refresh_token stored for " + account.Email}
	}

	// singleflight key: refresh_token alone is enough — same token from
	// multiple goroutines must always collapse to one upstream call.
	v, err, _ := refreshGroup.Do(creds.RefreshToken, func() (interface{}, error) {
		return doRefresh(cfg, &creds)
	})
	if err != nil {
		return nil, err
	}
	out := v.(db.AGCredentials)
	return &out, nil
}

// doRefresh performs the actual HTTP exchange. Always called inside
// singleflight so concurrent callers share the result.
func doRefresh(cfg *config.Config, creds *db.AGCredentials) (db.AGCredentials, error) {
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {creds.RefreshToken},
		"client_id":     {cfg.AGClientID},
		"client_secret": {cfg.AGClientSecret},
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post(
		"https://oauth2.googleapis.com/token",
		"application/x-www-form-urlencoded",
		strings.NewReader(data.Encode()),
	)
	if err != nil {
		return db.AGCredentials{}, &RefreshError{Reason: RefreshErrorTransient, Message: "network: " + err.Error()}
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	// Classify by Google's documented OAuth2 error codes
	// (https://datatracker.ietf.org/doc/html/rfc6749#section-5.2).
	if resp.StatusCode != 200 {
		var errResp struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		_ = json.Unmarshal(body, &errResp)

		reason := RefreshErrorTransient
		switch errResp.Error {
		case "invalid_grant":
			// refresh_token revoked, expired, or reused. Permanent.
			reason = RefreshErrorInvalidGrant
		case "invalid_client", "unauthorized_client":
			// OAuth app misconfigured. Affects all accounts; don't blame this one.
			reason = RefreshErrorInvalidClient
		case "invalid_request", "invalid_scope":
			// Bug in our request. Transient from caller's POV — log loud.
			reason = RefreshErrorTransient
		}

		msg := fmt.Sprintf("status %d", resp.StatusCode)
		if errResp.Error != "" {
			msg += " " + errResp.Error
			if errResp.ErrorDescription != "" {
				msg += " (" + errResp.ErrorDescription + ")"
			}
		} else if len(body) > 0 {
			msg += ": " + truncateAG(string(body), 200)
		}
		return db.AGCredentials{}, &RefreshError{Reason: reason, Message: msg}
	}

	// Google's success response: access_token + expires_in are guaranteed.
	// refresh_token is OPTIONAL — only sent when Google rotates the family.
	// We must capture it when present; using an old token after rotation
	// produces invalid_grant and kills the account.
	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token,omitempty"`
		ExpiresIn    int    `json:"expires_in"`
		TokenType    string `json:"token_type"`
		Scope        string `json:"scope,omitempty"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return db.AGCredentials{}, &RefreshError{Reason: RefreshErrorParse, Message: "decode response: " + err.Error()}
	}

	if tokenResp.AccessToken == "" {
		return db.AGCredentials{}, &RefreshError{Reason: RefreshErrorParse, Message: "empty access_token in 200 response"}
	}

	// Build updated credentials. Preserve fields the response doesn't carry.
	out := *creds
	out.AccessToken = tokenResp.AccessToken
	if tokenResp.RefreshToken != "" {
		// Google rotated the family — this is the new authoritative refresh_token.
		out.RefreshToken = tokenResp.RefreshToken
	}
	if tokenResp.Scope != "" {
		out.Scope = tokenResp.Scope
	}
	expiresIn := tokenResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	out.ExpiresAt = time.Now().UTC().Add(time.Duration(expiresIn) * time.Second).Format(time.RFC3339)

	return out, nil
}

// LoadCodeAssist fetches projectId from Google's loadCodeAssist API
func LoadCodeAssist(accessToken string) (string, string, error) {
	body := `{"metadata":{"ideType":"IDE_UNSPECIFIED","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI"}}`

	req, err := http.NewRequest("POST",
		"https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist",
		strings.NewReader(body))
	if err != nil {
		return "", "", err
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "google-api-nodejs-client/9.15.1")
	req.Header.Set("X-Goog-Api-Client", "google-cloud-sdk vscode_cloudshelleditor/0.1")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("loadCodeAssist request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("loadCodeAssist status %d", resp.StatusCode)
	}

	var result struct {
		CloudaicompanionProject interface{} `json:"cloudaicompanionProject"`
		AllowedTiers            []struct {
			ID        string `json:"id"`
			IsDefault bool   `json:"isDefault"`
		} `json:"allowedTiers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", fmt.Errorf("parse loadCodeAssist: %w", err)
	}

	// Extract project ID
	var projectID string
	switch v := result.CloudaicompanionProject.(type) {
	case string:
		projectID = v
	case map[string]interface{}:
		if id, ok := v["id"].(string); ok {
			projectID = id
		}
	}

	if projectID == "" {
		return "", "", fmt.Errorf("no projectId in loadCodeAssist response")
	}

	// Extract tier ID
	tierID := "legacy-tier"
	for _, tier := range result.AllowedTiers {
		if tier.IsDefault && tier.ID != "" {
			tierID = strings.TrimSpace(tier.ID)
			break
		}
	}

	return projectID, tierID, nil
}

// IsTokenExpired checks if the access token is expired or expiring soon
func IsTokenExpired(creds *db.AGCredentials, leadMinutes int) bool {
	if creds.ExpiresAt == "" {
		return true
	}

	expiresAt, err := time.Parse(time.RFC3339, creds.ExpiresAt)
	if err != nil {
		// Try other formats
		expiresAt, err = time.Parse(time.RFC3339Nano, creds.ExpiresAt)
		if err != nil {
			return true
		}
	}

	threshold := time.Now().UTC().Add(time.Duration(leadMinutes) * time.Minute)
	return expiresAt.Before(threshold)
}

// --- Quota Fetch (mirrors kiro.FetchQuota pattern) -----------------

// QuotaBreakdownEntry represents per-model usage info parsed from
// the Cloud Code Assist fetchAvailableModels response.
//
// Antigravity reports usage as `remainingFraction` (0..1). We
// normalise to a 1000-base bucket like 9router does so the dashboard
// can render percent + a "X / 1000" counter without re-deriving it.
type QuotaBreakdownEntry struct {
	Used    float64 `json:"used"`
	Total   float64 `json:"total"`
	ResetAt string  `json:"reset_at,omitempty"`
	Label   string  `json:"label,omitempty"`
}

// QuotaResult is the AG analogue of kiro.QuotaResult: a single primary
// summary plus the full per-model breakdown.
type QuotaResult struct {
	Used      int                            `json:"used"`
	Total     int                            `json:"total"`
	ResetAt   string                         `json:"reset_at"`
	Plan      string                         `json:"plan"` // currentTier.name from loadCodeAssist
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

// importantAGModels is the subset 9router surfaces on the dashboard.
// Models outside this list are skipped to keep the breakdown panel
// focused on the SKUs we actually proxy via PROVIDER_MODELS.
var importantAGModels = map[string]bool{
	"claude-opus-4-6-thinking": true,
	"claude-sonnet-4-6":        true,
	"gemini-3.1-pro-high":      true,
	"gemini-3.1-pro-low":       true,
	"gemini-3-flash":           true,
	"gpt-oss-120b-medium":      true,
	"gemini-3-pro-high":        true,
	"gemini-3-pro-low":         true,
	"gemini-3.1-flash-image":   true,
	"gemini-pro-agent":         true,
	"gemini-3.1-flash-lite":    true,
}

// agQuotaTotal is the normalised "100% capacity" value we use for the
// summary `Total` field. Cloud Code Assist returns remainingFraction
// (0..1); converting to 1000-base lets the dashboard show "423/1000"
// with the same renderer used for Kiro accounts.
const agQuotaTotal = 1000

// FetchQuota pulls per-model quotas from Cloud Code Assist for an
// Antigravity account. It first calls loadCodeAssist (to obtain the
// project ID + tier name) then fetchAvailableModels (which returns
// models[*].quotaInfo with remainingFraction + resetTime).
//
// Returns *QuotaResult on success, or *QuotaError describing why no
// quota was available — callers can use Reason==QuotaErrorAuthExpired
// to drive a refresh-and-retry loop, mirroring the Kiro flow.
func FetchQuota(accessToken string) (*QuotaResult, error) {
	if accessToken == "" {
		return nil, &QuotaError{Reason: QuotaErrorAuthExpired, Message: "no access token"}
	}

	// Step 1: pull subscription info (projectId + currentTier.name).
	// loadCodeAssist may answer 401 when the token is expired — bubble
	// that up so the caller can refresh + retry.
	subInfo, subErr := loadCodeAssistSubscription(accessToken)
	if subErr != nil {
		return nil, subErr
	}

	projectID := subInfo.projectID
	plan := subInfo.tierName
	if plan == "" {
		plan = "Antigravity"
	}

	// Step 2: fetch available models with quota.
	body := map[string]interface{}{}
	if projectID != "" {
		body["project"] = projectID
	}
	bodyBytes, _ := json.Marshal(body)

	req, err := http.NewRequest("POST",
		"https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels",
		strings.NewReader(string(bodyBytes)))
	if err != nil {
		return nil, &QuotaError{Reason: QuotaErrorNetwork, Message: err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "antigravity/1.107.0 darwin/arm64")
	req.Header.Set("X-Client-Name", "antigravity")
	req.Header.Set("X-Client-Version", "1.107.0")
	// "x-request-source": "local" matches 9router's MITM bypass header.
	req.Header.Set("X-Request-Source", "local")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, &QuotaError{Reason: QuotaErrorNetwork, Message: err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, &QuotaError{
			Reason:  QuotaErrorAuthExpired,
			Message: fmt.Sprintf("fetchAvailableModels status %d: %s", resp.StatusCode, truncateAG(string(raw), 200)),
		}
	}
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, &QuotaError{
			Reason:  QuotaErrorNetwork,
			Message: fmt.Sprintf("fetchAvailableModels status %d: %s", resp.StatusCode, truncateAG(string(raw), 200)),
		}
	}

	var data struct {
		Models map[string]struct {
			DisplayName string `json:"displayName"`
			IsInternal  bool   `json:"isInternal"`
			QuotaInfo   *struct {
				RemainingFraction float64     `json:"remainingFraction"`
				ResetTime         interface{} `json:"resetTime"`
			} `json:"quotaInfo"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, &QuotaError{Reason: QuotaErrorNetwork, Message: "decode: " + err.Error()}
	}

	if len(data.Models) == 0 {
		return nil, &QuotaError{Reason: QuotaErrorEmpty, Message: "no models in response"}
	}

	breakdown := make(map[string]QuotaBreakdownEntry, len(importantAGModels))
	var summaryReset string
	var summary *QuotaBreakdownEntry

	for modelKey, info := range data.Models {
		if info.IsInternal {
			continue
		}
		if !importantAGModels[modelKey] {
			continue
		}
		if info.QuotaInfo == nil {
			continue
		}

		fraction := info.QuotaInfo.RemainingFraction
		if fraction < 0 {
			fraction = 0
		}
		if fraction > 1 {
			fraction = 1
		}

		remaining := float64(agQuotaTotal) * fraction
		used := float64(agQuotaTotal) - remaining
		resetAt := parseAGResetTime(info.QuotaInfo.ResetTime)
		if summaryReset == "" {
			summaryReset = resetAt
		}

		entry := QuotaBreakdownEntry{
			Used:    used,
			Total:   agQuotaTotal,
			ResetAt: resetAt,
			Label:   info.DisplayName,
		}
		breakdown[modelKey] = entry

		// Pick a stable "summary" model for the top-level used/total:
		// gemini-3.1-pro-high if available (the flagship), else first.
		if modelKey == "gemini-3.1-pro-high" {
			e := entry
			summary = &e
		} else if summary == nil {
			e := entry
			summary = &e
		}
	}

	if summary == nil {
		return nil, &QuotaError{Reason: QuotaErrorEmpty, Message: "no important models with quotaInfo"}
	}

	return &QuotaResult{
		Used:      int(summary.Used),
		Total:     int(summary.Total),
		ResetAt:   summaryReset,
		Plan:      plan,
		Breakdown: breakdown,
	}, nil
}

// agSubscriptionInfo bundles the bits of loadCodeAssist we care about.
type agSubscriptionInfo struct {
	projectID string
	tierName  string
}

// loadCodeAssistSubscription is a quota-aware variant of LoadCodeAssist
// that surfaces auth errors with QuotaErrorAuthExpired so the caller
// can refresh and retry. Unlike LoadCodeAssist it doesn't insist on a
// projectID being present (some fresh accounts return only a tier).
func loadCodeAssistSubscription(accessToken string) (*agSubscriptionInfo, *QuotaError) {
	body := `{"metadata":{"ideType":"IDE_UNSPECIFIED","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI"},"mode":1}`

	req, err := http.NewRequest("POST",
		"https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist",
		strings.NewReader(body))
	if err != nil {
		return nil, &QuotaError{Reason: QuotaErrorNetwork, Message: err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "google-api-nodejs-client/9.15.1")
	req.Header.Set("X-Goog-Api-Client", "google-cloud-sdk vscode_cloudshelleditor/0.1")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, &QuotaError{Reason: QuotaErrorNetwork, Message: err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, &QuotaError{
			Reason:  QuotaErrorAuthExpired,
			Message: fmt.Sprintf("loadCodeAssist status %d: %s", resp.StatusCode, truncateAG(string(raw), 200)),
		}
	}
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, &QuotaError{
			Reason:  QuotaErrorNetwork,
			Message: fmt.Sprintf("loadCodeAssist status %d: %s", resp.StatusCode, truncateAG(string(raw), 200)),
		}
	}

	var result struct {
		CloudaicompanionProject interface{} `json:"cloudaicompanionProject"`
		CurrentTier             *struct {
			Name string `json:"name"`
			ID   string `json:"id"`
		} `json:"currentTier"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, &QuotaError{Reason: QuotaErrorNetwork, Message: "decode: " + err.Error()}
	}

	out := &agSubscriptionInfo{}
	switch v := result.CloudaicompanionProject.(type) {
	case string:
		out.projectID = v
	case map[string]interface{}:
		if id, ok := v["id"].(string); ok {
			out.projectID = id
		}
	}
	if result.CurrentTier != nil {
		out.tierName = strings.TrimSpace(result.CurrentTier.Name)
	}
	return out, nil
}

// parseAGResetTime mirrors kiro.parseResetTime but is duplicated here
// so the two providers stay loosely coupled (the field shapes Google
// returns are technically the same, but Kiro's parser also accepts
// numeric epoch values that Google doesn't emit).
func parseAGResetTime(v interface{}) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		if val == "" {
			return ""
		}
		if t, err := time.Parse(time.RFC3339, val); err == nil {
			return t.Format(time.RFC3339)
		}
		return val
	case float64:
		if val <= 0 {
			return ""
		}
		var ts int64
		if val < 1e12 {
			ts = int64(val) * 1000
		} else {
			ts = int64(val)
		}
		return time.UnixMilli(ts).UTC().Format(time.RFC3339)
	}
	return ""
}

// truncateAG keeps log messages short and safe for upstream API errors.
func truncateAG(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
