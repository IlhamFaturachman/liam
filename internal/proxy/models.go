package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/liam-auto/liam/internal/db"
	"github.com/liam-auto/liam/internal/models"
	"github.com/liam-auto/liam/internal/providers/antigravity"
	liamsync "github.com/liam-auto/liam/internal/sync"
)

// ModelsHandler handles model registry, aliases, test, and refresh
type ModelsHandler struct {
	cfg      ServerConfig
	db       *db.Database
	registry *models.Registry
	aliases  *models.AliasStore
	pool     *AccountPool
	ag       *antigravity.Executor
	syncer   *liamsync.Syncer
}

// ServerConfig is a small interface to avoid circular dependencies
type ServerConfig interface {
	GetPort() int
}

// NewModelsHandler creates a new models handler
func NewModelsHandler(cfg ServerConfig, database *db.Database, registry *models.Registry, aliases *models.AliasStore, pool *AccountPool, ag *antigravity.Executor, syncer *liamsync.Syncer) *ModelsHandler {
	return &ModelsHandler{cfg: cfg, db: database, registry: registry, aliases: aliases, pool: pool, ag: ag, syncer: syncer}
}

// HandleListModels returns all models, optionally filtered by provider
func (h *ModelsHandler) HandleListModels(w http.ResponseWriter, r *http.Request) {
	provider := r.URL.Query().Get("provider")
	list, err := h.registry.List(provider)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if list == nil {
		list = []models.Model{}
	}
	writeJSON(w, 200, list)
}

// HandleAddCustomModel adds a user-defined custom model
func (h *ModelsHandler) HandleAddCustomModel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProviderAlias string                 `json:"provider_alias"`
		ModelID       string                 `json:"model_id"`
		DisplayName   string                 `json:"display_name"`
		Type          string                 `json:"type"`
		Metadata      map[string]interface{} `json:"metadata"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if req.ProviderAlias == "" || req.ModelID == "" {
		writeError(w, 400, "provider_alias and model_id required")
		return
	}

	model, err := h.registry.AddCustom(req.ProviderAlias, req.ModelID, req.DisplayName, req.Type, req.Metadata)
	if err != nil {
		writeError(w, 400, fmt.Sprintf("failed to add model: %v", err))
		return
	}
	if h.syncer != nil {
		h.syncer.PushCustomModelAsync(model.ID, model.ProviderAlias, model.ModelID, model.DisplayName, model.Type, model.IsEnabled, model.Metadata)
	}
	writeJSON(w, 201, model)
}

// HandleRemoveModel removes a model from the registry
func (h *ModelsHandler) HandleRemoveModel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "*")
	if id == "" {
		writeError(w, 400, "missing id")
		return
	}
	if err := h.registry.Remove(id); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if h.syncer != nil {
		h.syncer.DeleteCustomModelAsync(id)
	}
	writeJSON(w, 200, map[string]string{"status": "removed"})
}

// HandleToggleModel enables/disables a model
func (h *ModelsHandler) HandleToggleModel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "*")
	var req struct {
		Enabled bool `json:"enabled"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	if err := h.registry.Toggle(id, req.Enabled); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	// Mirror the toggle upstream so other devices see the new state.
	if h.syncer != nil {
		if m, mErr := h.registry.Get(id); mErr == nil && m != nil {
			h.syncer.PushCustomModelAsync(m.ID, m.ProviderAlias, m.ModelID, m.DisplayName, m.Type, m.IsEnabled, m.Metadata)
		}
	}
	writeJSON(w, 200, map[string]interface{}{"status": "ok", "enabled": req.Enabled})
}

// HandleDisableAllForProvider disables all models for a provider
func (h *ModelsHandler) HandleDisableAllForProvider(w http.ResponseWriter, r *http.Request) {
	alias := chi.URLParam(r, "alias")
	var req struct {
		Enabled bool `json:"enabled"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	if err := h.registry.SetEnabledForProvider(alias, req.Enabled); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]interface{}{"status": "ok"})
}

// HandleTestModel tests a model by calling self at /v1/chat/completions
func (h *ModelsHandler) HandleTestModel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if req.Model == "" {
		writeError(w, 400, "model required")
		return
	}

	// Get internal test key
	apiKey, err := h.db.EnsureInternalTestKey()
	if err != nil {
		writeJSON(w, 200, map[string]interface{}{
			"ok":    false,
			"error": fmt.Sprintf("Failed to get internal key: %v", err),
		})
		return
	}

	// Build minimal test request
	testBody, _ := json.Marshal(map[string]interface{}{
		"model":      req.Model,
		"max_tokens": 1,
		"stream":     false,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
	})

	// Call self
	url := fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", h.cfg.GetPort())
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(testBody))
	if err != nil {
		writeJSON(w, 200, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	start := time.Now()
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(httpReq)
	latency := int(time.Since(start).Milliseconds())

	if err != nil {
		writeJSON(w, 200, map[string]interface{}{"ok": false, "latency_ms": latency, "error": err.Error()})
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		var parsed map[string]interface{}
		json.Unmarshal(body, &parsed)
		detail := extractErrorDetail(parsed, body)
		writeJSON(w, 200, map[string]interface{}{
			"ok":         false,
			"latency_ms": latency,
			"error":      fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncStr(detail, 240)),
			"status":     resp.StatusCode,
		})
		return
	}

	// Detect SSE stream response. Kiro's executor always streams (even
	// when stream=false) so the body looks like
	//   data: {...}\n\ndata: {...}\n\ndata: [DONE]\n\n
	// Treat any chunk with content/finish_reason as success.
	bodyStr := string(body)
	if strings.HasPrefix(strings.TrimSpace(bodyStr), "data:") {
		ok, sseErr := parseSSESuccess(bodyStr)
		if !ok {
			writeJSON(w, 200, map[string]interface{}{
				"ok":         false,
				"latency_ms": latency,
				"error":      truncStr(sseErr, 240),
				"status":     resp.StatusCode,
			})
			return
		}
		writeJSON(w, 200, map[string]interface{}{
			"ok":         true,
			"latency_ms": latency,
			"error":      nil,
			"status":     resp.StatusCode,
		})
		return
	}

	// Non-streaming JSON response.
	var parsed map[string]interface{}
	json.Unmarshal(body, &parsed)

	// Check provider-level error in body
	if errField, ok := parsed["error"]; ok && errField != nil {
		errStr := extractErrorDetail(parsed, body)
		writeJSON(w, 200, map[string]interface{}{
			"ok":         false,
			"latency_ms": latency,
			"error":      truncStr(errStr, 240),
			"status":     resp.StatusCode,
		})
		return
	}

	// Check choices array
	choices, _ := parsed["choices"].([]interface{})
	if len(choices) == 0 {
		writeJSON(w, 200, map[string]interface{}{
			"ok":         false,
			"latency_ms": latency,
			"error":      "Provider returned no completion choices",
			"status":     resp.StatusCode,
		})
		return
	}

	writeJSON(w, 200, map[string]interface{}{
		"ok":         true,
		"latency_ms": latency,
		"error":      nil,
		"status":     resp.StatusCode,
	})
}

// parseSSESuccess scans an OpenAI-compatible SSE body and returns true when
// at least one chunk carried delta content, a finish_reason, or a tool call.
// Returns (false, errMessage) when the only chunks were errors / empty.
func parseSSESuccess(body string) (bool, string) {
	lines := strings.Split(body, "\n")
	sawContent := false
	sawFinish := false
	var lastErr string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}

		var chunk map[string]interface{}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}

		// Provider-level error inside an SSE event.
		if errField, ok := chunk["error"]; ok && errField != nil {
			lastErr = extractErrorDetail(chunk, []byte(payload))
			continue
		}

		choices, _ := chunk["choices"].([]interface{})
		for _, c := range choices {
			cm, _ := c.(map[string]interface{})
			if cm == nil {
				continue
			}
			if fr, ok := cm["finish_reason"].(string); ok && fr != "" {
				sawFinish = true
			}
			delta, _ := cm["delta"].(map[string]interface{})
			if delta == nil {
				continue
			}
			if content, _ := delta["content"].(string); content != "" {
				sawContent = true
			}
			// Models that emit thinking-only output before metering events
			// (Opus 4.7, DeepSeek, GLM-5) need this branch — without it the
			// SSE probe falsely reports "no completion content".
			if rc, _ := delta["reasoning_content"].(string); rc != "" {
				sawContent = true
			}
			if tc, _ := delta["tool_calls"].([]interface{}); len(tc) > 0 {
				sawContent = true
			}
		}
	}

	if sawContent || sawFinish {
		return true, ""
	}
	if lastErr == "" {
		lastErr = "Provider returned no completion content"
	}
	return false, lastErr
}

// HandleRefreshModels fetches live model list from upstream
func (h *ModelsHandler) HandleRefreshModels(w http.ResponseWriter, r *http.Request) {
	alias := chi.URLParam(r, "alias")
	if alias != "ag" && alias != "antigravity" {
		writeError(w, 400, "Live fetch only supported for Antigravity")
		return
	}

	// Pick an active account
	account, err := h.pool.Pick("antigravity")
	if err != nil {
		writeError(w, 503, fmt.Sprintf("No active accounts: %v", err))
		return
	}

	// Parse credentials
	var creds db.AGCredentials
	if err := json.Unmarshal(account.Credentials, &creds); err != nil {
		writeError(w, 500, "Failed to parse credentials")
		return
	}

	// Fetch from upstream
	upstreamModels, err := antigravity.FetchModels(creds.AccessToken)
	if err != nil {
		writeError(w, 502, fmt.Sprintf("Failed to fetch: %v", err))
		return
	}

	// Compare with current registry
	current, _ := h.registry.List("ag")
	currentIDs := map[string]bool{}
	for _, m := range current {
		currentIDs[m.ModelID] = true
	}

	var newModels []map[string]string
	for _, m := range upstreamModels {
		if !currentIDs[m.ID] {
			newModels = append(newModels, map[string]string{"id": m.ID, "name": m.Name})
		}
	}

	writeJSON(w, 200, map[string]interface{}{
		"models":     upstreamModels,
		"new_models": newModels,
	})
}

// HandleListAliases returns all model aliases
func (h *ModelsHandler) HandleListAliases(w http.ResponseWriter, r *http.Request) {
	list, err := h.aliases.List()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if list == nil {
		list = []models.Alias{}
	}
	writeJSON(w, 200, list)
}

// HandleSetAlias creates or updates an alias
func (h *ModelsHandler) HandleSetAlias(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Alias  string `json:"alias"`
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if req.Alias == "" || req.Target == "" {
		writeError(w, 400, "alias and target required")
		return
	}

	if err := h.aliases.Set(req.Alias, req.Target); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// HandleDeleteAlias removes an alias
func (h *ModelsHandler) HandleDeleteAlias(w http.ResponseWriter, r *http.Request) {
	alias := chi.URLParam(r, "alias")
	if alias == "" {
		writeError(w, 400, "missing alias")
		return
	}
	if err := h.aliases.Delete(alias); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "deleted"})
}

// --- Helpers ---

func extractErrorDetail(parsed map[string]interface{}, body []byte) string {
	if errMap, ok := parsed["error"].(map[string]interface{}); ok {
		if msg, ok := errMap["message"].(string); ok {
			return msg
		}
	}
	if errStr, ok := parsed["error"].(string); ok {
		return errStr
	}
	if msg, ok := parsed["message"].(string); ok {
		return msg
	}
	return strings.TrimSpace(string(body))
}

func truncStr(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
