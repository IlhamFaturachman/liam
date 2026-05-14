package antigravity

import (
	"encoding/json"
	"fmt"
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
