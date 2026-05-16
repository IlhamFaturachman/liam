package kiro

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/liam-auto/liam/internal/db"
)

// Executor handles Kiro (AWS CodeWhisperer) requests
type Executor struct {
	client *http.Client
}

// NewExecutor creates a new Kiro executor.
//
// We deliberately do NOT set http.Client.Timeout: that field caps the
// entire request lifecycle including streaming body reads, which would
// truncate long Kiro EventStream responses (commonly 5–20 minutes for
// agentic coding tasks). Mid-stream truncation surfaces downstream as
// half-formed tool calls like
//
//	{"filePath": "/path/to/file.md"
//
// which then explodes in the consumer's JSON.parse with the dreaded
// "Expected '}'" error. Instead we install a custom Transport whose
// ResponseHeaderTimeout caps the *time-to-first-byte* (so a hung
// upstream still surfaces quickly) without bounding the streaming body.
// See CLIProxyAPI's AGENTS.md: "after an upstream connection is
// established, do not set timeouts for any subsequent network behavior."
func NewExecutor() *Executor {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          50,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &Executor{
		client: &http.Client{Transport: transport},
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

	// Trace whether thinking-mode is active for this request. Useful for
	// operators verifying that `model(max)` / `model-thinking` actually
	// flow through the translator. Only logs when the tag is present;
	// silent for plain non-thinking calls so the log stays tidy.
	if bytes.Contains(kiroBody, []byte("<thinking_mode>enabled</thinking_mode>")) {
		const tag = "<max_thinking_length>"
		if i := bytes.Index(kiroBody, []byte(tag)); i >= 0 {
			rest := kiroBody[i+len(tag):]
			if end := bytes.IndexByte(rest, '<'); end > 0 {
				log.Printf("[KIRO] thinking enabled, model=%s, budget=%s", model, rest[:end])
			}
		}
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

	// On 4xx/5xx, dump the upstream payload to disk so we can inspect
	// what we sent vs what Kiro rejected. The dashboard's request_body
	// shows the OpenAI-format input *before* translation; this dump is
	// the actual JSON the upstream saw, which is what really matters
	// for debugging "Improperly formed request" and friends.
	if resp.StatusCode >= 400 {
		dumpFailedKiroPayload(account.ID, model, kiroBody, resp.StatusCode)
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

// dumpFailedKiroPayload writes the JSON we sent to Kiro to a debug file
// so the operator can compare it against what 9router/Kiro IDE produces.
// We keep at most the 10 most recent dumps under ~/.liam/debug/kiro-fail/
// so the directory doesn't grow unbounded.
func dumpFailedKiroPayload(accountID, model string, body []byte, statusCode int) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	dir := filepath.Join(home, ".liam", "debug", "kiro-fail")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}

	// Trim oldest files when we exceed the cap. Best-effort: any error
	// here is silently ignored to keep the request hot path fast.
	if entries, err := os.ReadDir(dir); err == nil && len(entries) >= 10 {
		// os.ReadDir returns alphabetical order; our filenames are
		// timestamp-prefixed so sort by name == sort by time.
		excess := len(entries) - 9
		for i := 0; i < excess; i++ {
			os.Remove(filepath.Join(dir, entries[i].Name()))
		}
	}

	ts := time.Now().UTC().Format("20060102T150405.000")
	safeModel := strings.ReplaceAll(model, "/", "_")
	name := fmt.Sprintf("%s-%d-%s-%s.json", ts, statusCode, safeModel, accountID[:8])
	path := filepath.Join(dir, name)
	_ = os.WriteFile(path, body, 0644)
}
