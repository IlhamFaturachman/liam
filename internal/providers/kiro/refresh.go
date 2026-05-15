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

// FetchQuota fetches usage limits from Kiro API
// Returns (used, total, error)
func FetchQuota(accessToken, profileARN string) (int, int, error) {
	if accessToken == "" {
		return 0, 0, fmt.Errorf("no access token")
	}

	if profileARN == "" {
		profileARN = "arn:aws:codewhisperer:us-east-1:638616132270:profile/AAAACCCCXXXX"
	}

	client := &http.Client{Timeout: 15 * time.Second}

	// Try multiple endpoints (same as 9Router)
	type attempt struct {
		name   string
		doReq  func() (*http.Request, error)
	}

	attempts := []attempt{
		{
			name: "codewhisperer-post",
			doReq: func() (*http.Request, error) {
				body := fmt.Sprintf(`{"origin":"AI_EDITOR","profileArn":"%s","resourceType":"AGENTIC_REQUEST"}`, profileARN)
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
			name: "codewhisperer-get",
			doReq: func() (*http.Request, error) {
				req, err := http.NewRequest("GET", "https://codewhisperer.us-east-1.amazonaws.com/getUsageLimits?isEmailRequired=true&origin=AI_EDITOR&resourceType=AGENTIC_REQUEST", nil)
				if err != nil {
					return nil, err
				}
				req.Header.Set("Authorization", "Bearer "+accessToken)
				req.Header.Set("Accept", "application/json")
				req.Header.Set("x-amz-user-agent", "aws-sdk-js/1.0.0 KiroIDE")
				return req, nil
			},
		},
		{
			name: "q-get",
			doReq: func() (*http.Request, error) {
				url := fmt.Sprintf("https://q.us-east-1.amazonaws.com/getUsageLimits?origin=AI_EDITOR&profileArn=%s&resourceType=AGENTIC_REQUEST", profileARN)
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

	for _, a := range attempts {
		req, err := a.doReq()
		if err != nil {
			continue
		}

		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			continue
		}

		var data struct {
			UsageBreakdownList []struct {
				ResourceType              string  `json:"resourceType"`
				CurrentUsageWithPrecision float64 `json:"currentUsageWithPrecision"`
				UsageLimitWithPrecision   float64 `json:"usageLimitWithPrecision"`
			} `json:"usageBreakdownList"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			continue
		}

		// Find AGENTIC_REQUEST quota
		for _, breakdown := range data.UsageBreakdownList {
			if strings.EqualFold(breakdown.ResourceType, "AGENTIC_REQUEST") {
				used := int(breakdown.CurrentUsageWithPrecision)
				total := int(breakdown.UsageLimitWithPrecision)
				return used, total, nil
			}
		}

		// Use first entry if no AGENTIC_REQUEST
		if len(data.UsageBreakdownList) > 0 {
			b := data.UsageBreakdownList[0]
			return int(b.CurrentUsageWithPrecision), int(b.UsageLimitWithPrecision), nil
		}
	}

	return 0, 0, fmt.Errorf("all quota endpoints failed")
}
