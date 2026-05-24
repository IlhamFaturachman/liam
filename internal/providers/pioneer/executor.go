package pioneer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/liam-auto/liam/internal/db"
)

const baseURL = "https://api.pioneer.ai"

// Executor talks to Pioneer's OpenAI-compatible inference API.
// Since the request and response formats are already OpenAI standard,
// the executor is a clean passthrough — no translation needed.
type Executor struct {
	client *http.Client
}

// NewExecutor returns a ready-to-use Pioneer executor.
func NewExecutor() *Executor {
	return &Executor{
		client: &http.Client{
			// Pioneer is a hosted API, not a streaming-heavy upstream
			// like Kiro/AG. We still avoid a global Client.Timeout so
			// long streaming responses are not cut off.
			Transport: &http.Transport{
				TLSHandshakeTimeout:   15 * 1e9, // 15s
				ResponseHeaderTimeout: 60 * 1e9, // 60s time-to-first-byte
				IdleConnTimeout:       90 * 1e9, // 90s
				MaxIdleConns:          50,
				ForceAttemptHTTP2:     true,
				Proxy:                 http.ProxyFromEnvironment,
			},
		},
	}
}

// ExecuteWithSession proxies the request to Pioneer's /v1/chat/completions.
// The session ID is unused because Pioneer does not support session
// affinity — every request is stateless with a simple API key auth.
func (e *Executor) ExecuteWithSession(account *db.Account, model string, body []byte, stream bool, _ string) (*http.Response, error) {
	// Extract API key from credentials
	var creds PioneerCredentials
	if err := json.Unmarshal(account.Credentials, &creds); err != nil {
		return nil, fmt.Errorf("pioneer: invalid credentials: %w", err)
	}
	if creds.APIKey == "" {
		return nil, fmt.Errorf("pioneer: missing api_key in credentials")
	}

	// Strip the provider prefix from model name
	upstreamModel := strings.TrimPrefix(model, "pio/")
	upstreamModel = strings.TrimPrefix(upstreamModel, "pioneer/")

	// Patch the model name in the body to the bare upstream model ID
	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("pioneer: invalid request body: %w", err)
	}
	req["model"] = upstreamModel
	patchedBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("pioneer: marshal error: %w", err)
	}

	httpReq, err := http.NewRequest("POST", baseURL+"/v1/chat/completions", bytes.NewReader(patchedBody))
	if err != nil {
		return nil, fmt.Errorf("pioneer: request build error: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-API-Key", creds.APIKey)

	resp, err := e.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("pioneer: upstream error: %w", err)
	}

	return resp, nil
}
