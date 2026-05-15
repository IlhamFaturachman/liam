package kiro

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/liam-auto/liam/internal/db"
)

// Kiro desktop auth endpoint (doesn't require clientId/clientSecret)
const kiroDesktopRefreshURL = "https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken"

// RefreshToken refreshes a Kiro access token using the desktop auth endpoint
// This endpoint only requires the refresh token — no clientId/clientSecret needed
func RefreshToken(account *db.Account) (*KiroCredentials, error) {
	var creds KiroCredentials
	if err := json.Unmarshal(account.Credentials, &creds); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}

	if creds.RefreshToken == "" {
		return nil, fmt.Errorf("no refresh token")
	}

	// Use desktop auth endpoint (no clientId/clientSecret required)
	body, _ := json.Marshal(map[string]string{
		"refreshToken": creds.RefreshToken,
	})

	req, err := http.NewRequest("POST", kiroDesktopRefreshURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("refresh failed with status %d", resp.StatusCode)
	}

	var tokenResp struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ProfileArn   string `json:"profileArn"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	// Update credentials
	creds.AccessToken = tokenResp.AccessToken
	if tokenResp.RefreshToken != "" {
		creds.RefreshToken = tokenResp.RefreshToken
	}
	if tokenResp.ProfileArn != "" {
		creds.ProfileARN = tokenResp.ProfileArn
	}
	// Desktop auth doesn't return expiresIn — assume 1 hour
	creds.ExpiresAt = time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)

	return &creds, nil
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
