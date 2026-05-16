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
)

// RefreshToken refreshes an expired Google OAuth access token
func RefreshToken(cfg *config.Config, account *db.Account) (*db.AGCredentials, error) {
	var creds db.AGCredentials
	if err := json.Unmarshal(account.Credentials, &creds); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}

	if creds.RefreshToken == "" {
		return nil, fmt.Errorf("no refresh token for account %s", account.Email)
	}

	// Google OAuth2 token refresh
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {creds.RefreshToken},
		"client_id":     {cfg.AGClientID},
		"client_secret": {cfg.AGClientSecret},
	}

	resp, err := http.Post(
		"https://oauth2.googleapis.com/token",
		"application/x-www-form-urlencoded",
		strings.NewReader(data.Encode()),
	)
	if err != nil {
		return nil, fmt.Errorf("refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("refresh failed with status %d", resp.StatusCode)
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		TokenType   string `json:"token_type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("parse refresh response: %w", err)
	}

	// Update credentials
	creds.AccessToken = tokenResp.AccessToken
	expiresAt := time.Now().UTC().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	creds.ExpiresAt = expiresAt.Format(time.RFC3339)

	return &creds, nil
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
	req.Header.Set("User-Agent", "google-api-nodejs-client/9.15.1")
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
