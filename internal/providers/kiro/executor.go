package kiro

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/liam-auto/liam/internal/db"
)

// Executor handles Kiro (AWS CodeWhisperer) requests
type Executor struct {
	client *http.Client
}

// NewExecutor creates a new Kiro executor
func NewExecutor() *Executor {
	return &Executor{
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

// Execute sends a request to Kiro API
func (e *Executor) Execute(account *db.Account, model string, body []byte, stream bool) (*http.Response, error) {
	return e.ExecuteWithSession(account, model, body, stream, "")
}

// ExecuteWithSession sends a request with a stable session ID (for anti-ban + caching)
func (e *Executor) ExecuteWithSession(account *db.Account, model string, body []byte, stream bool, sessionID string) (*http.Response, error) {
	// Parse credentials
	var creds KiroCredentials
	if err := json.Unmarshal(account.Credentials, &creds); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}

	if creds.AccessToken == "" {
		return nil, fmt.Errorf("no access token for account %s", account.Email)
	}

	region := creds.Region
	if region == "" {
		region = "us-east-1"
	}

	// Translate OpenAI request to Kiro format
	kiroBody, err := translateRequest(model, body, creds.ProfileARN)
	if err != nil {
		return nil, fmt.Errorf("translate request: %w", err)
	}

	// Build URL
	url := fmt.Sprintf("https://codewhisperer.%s.amazonaws.com/generateAssistantResponse", region)

	// Build request
	req, err := http.NewRequest("POST", url, bytes.NewReader(kiroBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+creds.AccessToken)
	req.Header.Set("Amz-Sdk-Invocation-Id", uuid.New().String())
	req.Header.Set("Amz-Sdk-Request", "attempt=1; max=1")
	// Impersonate Kiro IDE so the upstream applies the same tier/limits
	// the desktop client gets. The previous "liam-kiro/1.0" identifier
	// likely landed us in the generic CodeWhisperer 200k bucket; matching
	// 9router's official IDE headers is our best shot at the >200k tier
	// the IDE itself enjoys for Opus 4.7's 1M context.
	req.Header.Set("User-Agent", "AWS-SDK-JS/3.0.0 kiro-ide/1.0.0")
	req.Header.Set("X-Amz-User-Agent", "aws-sdk-js/3.0.0 kiro-ide/1.0.0")
	if sessionID != "" {
		req.Header.Set("X-Amz-Session-Id", sessionID)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute: %w", err)
	}

	// If error status, return as-is for handler to process
	if resp.StatusCode != 200 {
		return resp, nil
	}

	// Translate streaming response (Kiro is always binary EventStream)
	pr := translateStreamingResponse(resp.Body, model)
	translatedResp := &http.Response{
		StatusCode: 200,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
		},
		Body: pr,
	}
	return translatedResp, nil
}

// Read response body as bytes (for error handling)
func (e *Executor) ReadErrorBody(resp *http.Response) string {
	if resp == nil || resp.Body == nil {
		return ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}
