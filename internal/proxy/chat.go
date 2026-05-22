package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/liam-auto/liam/internal/db"
)

// handleChatCompletions is the main proxy endpoint (POST /v1/chat/completions).
// It resolves the model, applies token savers, picks an account, executes
// the upstream request with retries, and streams/returns the response.
func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Failed to read request body")
		return
	}
	defer r.Body.Close()

	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	model, _ := req["model"].(string)
	if model == "" {
		writeError(w, http.StatusBadRequest, "Missing 'model' field")
		return
	}

	// Resolve aliases
	if resolved := s.aliases.Resolve(model); resolved != "" {
		model = resolved
		req["model"] = resolved
		body, _ = json.Marshal(req)
	}

	// Thinking DSL: model(value) syntax
	if idx := strings.LastIndex(model, "("); idx > 0 && strings.HasSuffix(model, ")") {
		baseModel := model[:idx]
		thinkingValue := model[idx+1 : len(model)-1]
		model = baseModel
		req["model"] = baseModel

		switch thinkingValue {
		case "none":
			req["reasoning_effort"] = "none"
		case "auto":
			// let model decide
		case "low":
			req["reasoning_effort"] = "low"
		case "medium":
			req["reasoning_effort"] = "medium"
		case "high":
			req["reasoning_effort"] = "high"
		case "max":
			req["reasoning_effort"] = "max"
		default:
			if _, err := fmt.Sscanf(thinkingValue, "%d", new(int)); err == nil {
				req["reasoning_effort"] = thinkingValue
			} else {
				req["reasoning_effort"] = "high"
			}
		}
		body, _ = json.Marshal(req)
	}

	// Apply Kiro thinking default when no DSL suffix present
	if s.cfg.KiroThinkingDefault != "" && strings.ToLower(s.cfg.KiroThinkingDefault) != "off" {
		if strings.HasPrefix(model, "kr/") || strings.HasPrefix(model, "kiro/") {
			if _, ok := req["reasoning_effort"]; !ok {
				req["reasoning_effort"] = s.cfg.KiroThinkingDefault
				body, _ = json.Marshal(req)
			}
		}
	}

	// Handle -thinking suffix (backward compat)
	if strings.HasSuffix(model, "-thinking") {
		isKiro := strings.HasPrefix(model, "kr/") || strings.HasPrefix(model, "kiro/")
		baseModel := strings.TrimSuffix(model, "-thinking")
		_, baseErr := s.registry.Get(baseModel)

		if isKiro {
			_ = baseErr // pass-through for Kiro
		} else if baseErr == nil {
			model = baseModel
			req["model"] = baseModel
			if _, ok := req["reasoning_effort"]; !ok {
				req["reasoning_effort"] = "high"
			}
			body, _ = json.Marshal(req)
		}
	}

	// Strip thinking if model has thinking:false flag
	if s.registry.IsThinkingDisabled(model) {
		stripThinkingFromRequest(req)
		body, _ = json.Marshal(req)
	}

	stream, _ := req["stream"].(bool)

	provider := s.resolveProviderFromModel(model)
	providerInfo := s.providers.ByID(provider)

	// Token savers (RTK + Caveman)
	policy := loadTokenSaverPolicy(s.db, r.Header)
	body = applyTokenSavers(req, body, policy)

	// Session affinity
	sessionID := extractSessionID(r, req)

	// Combo dispatch
	comboModels := s.combo.ResolveCombo(model)
	if comboModels != nil {
		s.handleComboRequest(w, r, req, body, comboModels, stream, startTime)
		return
	}

	// Retry loop
	var lastErr error
	for attempt := 0; attempt < s.cfg.MaxRetriesPerRequest; attempt++ {
		account, err := s.pool.PickForSession(provider, model, sessionID)
		if err != nil {
			if attempt < s.cfg.MaxRetriesPerRequest-1 {
				wait := time.Duration(BackoffBaseMs) * time.Millisecond
				log.Printf("[RETRY %d] No active accounts (%v), sleeping %v", attempt+1, err, wait)
				time.Sleep(wait)
				continue
			}
			writeError(w, http.StatusServiceUnavailable, fmt.Sprintf("No available accounts: %v", err))
			return
		}

		// Inline token refresh
		if providerInfo != nil && providerInfo.Refresh != nil {
			if refreshErr := providerInfo.Refresh(s.cfg, s.db, account); refreshErr != nil {
				log.Printf("[REFRESH] Warning for %s: %v", account.Email, refreshErr)
			}
		}

		sessionID := s.pool.GetSessionID(account.ID)

		var resp *http.Response
		if providerInfo == nil || providerInfo.Executor == nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("Unsupported provider: %s", provider))
			return
		}
		resp, err = providerInfo.Executor.ExecuteWithSession(account, model, body, stream, sessionID)

		// Transport error
		if err != nil {
			lastErr = err
			cooldown, msg := s.applyAccountError(account, 0, []byte(err.Error()), nil)
			log.Printf("[RETRY %d] Account %s transport error: %v -> %s", attempt+1, account.Email, err, msg)
			if s.pool.Count(provider) <= 1 {
				time.Sleep(cooldown)
			}
			continue
		}

		// Non-retryable input errors
		if resp.StatusCode == 400 || resp.StatusCode == 404 || resp.StatusCode == 422 {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if IsNonRetryable(resp.StatusCode, respBody) {
				log.Printf("[NON-RETRYABLE] %s: %d — %s", account.Email, resp.StatusCode, string(respBody[:min(len(respBody), 100)]))
				s.logFailedRequest(r, account, provider, model, body, resp.StatusCode, string(respBody), startTime)
				writeError(w, resp.StatusCode, ExtractErrorMessage(respBody))
				return
			}
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 100)]))
			cooldown, msg := s.applyAccountError(account, resp.StatusCode, respBody, resp.Header)
			log.Printf("[RETRY %d] Account %s HTTP %d -> %s | body: %s", attempt+1, account.Email, resp.StatusCode, msg, string(respBody[:min(len(respBody), 200)]))
			s.logFailedRequest(r, account, provider, model, body, resp.StatusCode, string(respBody), startTime)
			if s.pool.Count(provider) <= 1 {
				time.Sleep(cooldown)
			}
			continue
		}

		// 401/403/429/5xx
		if resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 429 || resp.StatusCode >= 500 {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			cooldown, msg := s.applyAccountError(account, resp.StatusCode, respBody, resp.Header)
			log.Printf("[RETRY %d] Account %s HTTP %d -> %s", attempt+1, account.Email, resp.StatusCode, msg)
			s.logFailedRequest(r, account, provider, model, body, resp.StatusCode, string(respBody), startTime)
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			if s.pool.Count(provider) <= 1 {
				time.Sleep(cooldown)
			}
			continue
		}

		// Success
		s.db.MarkAccountSuccess(account.ID)

		reqBodyLog := string(body)
		if len(reqBodyLog) > 5120 {
			reqBodyLog = reqBodyLog[:5120] + "\n...(truncated)"
		}

		latency := int(time.Since(startTime).Milliseconds())
		usageLog := &db.UsageLog{
			APIKeyID:     r.Header.Get("X-API-Key-ID"),
			AccountID:    account.ID,
			AccountEmail: account.Email,
			Provider:     provider,
			Model:        model,
			StatusCode:   resp.StatusCode,
			LatencyMs:    latency,
			RequestBody:  reqBodyLog,
		}

		if stream {
			streamBody := s.streamResponse(w, resp, model)
			usageLog.TokensIn, usageLog.TokensOut = extractTokenUsage(streamBody)
			usageLog.ResponseBody = "(streaming response)"
		} else {
			respBody := s.forwardResponseCapture(w, resp, model)
			usageLog.TokensIn, usageLog.TokensOut = extractTokenUsage(respBody)
			if len(respBody) > 5120 {
				usageLog.ResponseBody = respBody[:5120] + "\n...(truncated)"
			} else {
				usageLog.ResponseBody = respBody
			}
		}

		s.db.LogUsage(usageLog)
		BroadcastRequest(map[string]interface{}{
			"id":            usageLog.ID,
			"created_at":    usageLog.CreatedAt.Format(time.RFC3339Nano),
			"account_id":    usageLog.AccountID,
			"account_email": usageLog.AccountEmail,
			"provider":      usageLog.Provider,
			"model":         usageLog.Model,
			"status_code":   usageLog.StatusCode,
			"latency_ms":    usageLog.LatencyMs,
			"tokens_in":     usageLog.TokensIn,
			"tokens_out":    usageLog.TokensOut,
		})
		return
	}

	writeError(w, http.StatusBadGateway, fmt.Sprintf("All retries failed: %v", lastErr))
}

// handleComboRequest tries each model in a combo until one succeeds.
func (s *Server) handleComboRequest(w http.ResponseWriter, r *http.Request, req map[string]interface{}, originalBody []byte, comboModels []string, stream bool, startTime time.Time) {
	var lastErr error

	for _, comboModel := range comboModels {
		req["model"] = comboModel
		body, _ := json.Marshal(req)

		provider := s.resolveProviderFromModel(comboModel)
		providerInfo := s.providers.ByID(provider)
		account, err := s.pool.PickForModel(provider, comboModel)
		if err != nil {
			lastErr = err
			log.Printf("[COMBO] No accounts for %s: %v", comboModel, err)
			continue
		}

		if providerInfo != nil && providerInfo.Refresh != nil {
			providerInfo.Refresh(s.cfg, s.db, account)
		}

		sessionID := s.pool.GetSessionID(account.ID)

		var resp *http.Response
		if providerInfo == nil || providerInfo.Executor == nil {
			continue
		}
		resp, err = providerInfo.Executor.ExecuteWithSession(account, comboModel, body, stream, sessionID)

		if err != nil {
			lastErr = err
			cooldown, msg := s.applyAccountError(account, 0, []byte(err.Error()), nil)
			log.Printf("[COMBO] %s account %s transport error: %v -> %s", comboModel, account.Email, err, msg)
			_ = cooldown
			continue
		}

		if resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 429 || resp.StatusCode >= 500 {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			cooldown, msg := s.applyAccountError(account, resp.StatusCode, respBody, resp.Header)
			lastErr = fmt.Errorf("HTTP %d from %s", resp.StatusCode, comboModel)
			log.Printf("[COMBO] %s account %s HTTP %d -> %s, trying next", comboModel, account.Email, resp.StatusCode, msg)
			_ = cooldown
			continue
		}

		// Success
		s.db.MarkAccountSuccess(account.ID)

		reqBodyLog := string(originalBody)
		if len(reqBodyLog) > 5120 {
			reqBodyLog = reqBodyLog[:5120] + "\n...(truncated)"
		}
		latency := int(time.Since(startTime).Milliseconds())
		usageLog := &db.UsageLog{
			APIKeyID:     r.Header.Get("X-API-Key-ID"),
			AccountID:    account.ID,
			AccountEmail: account.Email,
			Provider:     provider,
			Model:        comboModel,
			StatusCode:   resp.StatusCode,
			LatencyMs:    latency,
			RequestBody:  reqBodyLog,
		}

		BroadcastRequest(map[string]interface{}{
			"time":       time.Now().UTC().Format("15:04:05"),
			"model":      comboModel,
			"account":    account.Email,
			"latency_ms": latency,
			"status":     resp.StatusCode,
		})

		if stream {
			streamBody := s.streamResponse(w, resp, comboModel)
			usageLog.TokensIn, usageLog.TokensOut = extractTokenUsage(streamBody)
			usageLog.ResponseBody = "(streaming response)"
		} else {
			respBody := s.forwardResponseCapture(w, resp, comboModel)
			usageLog.TokensIn, usageLog.TokensOut = extractTokenUsage(respBody)
			if len(respBody) > 5120 {
				usageLog.ResponseBody = respBody[:5120] + "\n...(truncated)"
			} else {
				usageLog.ResponseBody = respBody
			}
		}
		s.db.LogUsage(usageLog)
		BroadcastRequest(map[string]interface{}{
			"id":            usageLog.ID,
			"created_at":    usageLog.CreatedAt.Format(time.RFC3339Nano),
			"account_id":    usageLog.AccountID,
			"account_email": usageLog.AccountEmail,
			"provider":      usageLog.Provider,
			"model":         usageLog.Model,
			"status_code":   usageLog.StatusCode,
			"latency_ms":    usageLog.LatencyMs,
			"tokens_in":     usageLog.TokensIn,
			"tokens_out":    usageLog.TokensOut,
		})
		return
	}

	writeError(w, http.StatusBadGateway, fmt.Sprintf("All models in combo failed: %v", lastErr))
}

// streamResponse pipes SSE from upstream to client, capturing the tail
// for token usage extraction.
func (s *Server) streamResponse(w http.ResponseWriter, resp *http.Response, model string) string {
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(resp.StatusCode)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return ""
	}

	const maxCapture = 1 * 1024 * 1024
	captured := make([]byte, 0, 64*1024)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		out := line
		if bytes.HasPrefix(line, []byte("data: {")) {
			jsonPart := line[len("data: "):]
			if rewritten := rewriteModelField(jsonPart, model); !bytes.Equal(rewritten, jsonPart) {
				out = append([]byte("data: "), rewritten...)
			}
		}
		out = append(out, '\n')
		w.Write(out)
		flusher.Flush()

		captured = append(captured, out...)
		if len(captured) > maxCapture {
			captured = captured[len(captured)-maxCapture:]
		}
	}
	if err := scanner.Err(); err != nil {
		log.Printf("Stream read error: %v", err)
	}
	return string(captured)
}

// forwardResponseCapture returns non-streaming response and captures body.
func (s *Server) forwardResponseCapture(w http.ResponseWriter, resp *http.Response, model string) string {
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	body = rewriteModelInBody(body, model)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
	return string(body)
}

// extractTokenUsage extracts (input, output) token counts from response body.
func extractTokenUsage(body string) (in int, out int) {
	if body == "" {
		return 0, 0
	}
	trimmed := strings.TrimSpace(body)

	// Non-streaming JSON
	if strings.HasPrefix(trimmed, "{") {
		var probe struct {
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				InputTokens      int `json:"input_tokens"`
				OutputTokens     int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(trimmed), &probe); err == nil && probe.Usage != nil {
			in = probe.Usage.PromptTokens
			if in == 0 {
				in = probe.Usage.InputTokens
			}
			out = probe.Usage.CompletionTokens
			if out == 0 {
				out = probe.Usage.OutputTokens
			}
			return in, out
		}
	}

	// SSE stream: scan for last usage record
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk struct {
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				InputTokens      int `json:"input_tokens"`
				OutputTokens     int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if chunk.Usage == nil {
			continue
		}
		if chunk.Usage.PromptTokens > 0 || chunk.Usage.InputTokens > 0 {
			in = chunk.Usage.PromptTokens
			if in == 0 {
				in = chunk.Usage.InputTokens
			}
		}
		if chunk.Usage.CompletionTokens > 0 || chunk.Usage.OutputTokens > 0 {
			out = chunk.Usage.CompletionTokens
			if out == 0 {
				out = chunk.Usage.OutputTokens
			}
		}
	}
	return in, out
}

// stripThinkingFromRequest removes thinking-related fields from the request.
func stripThinkingFromRequest(req map[string]interface{}) {
	delete(req, "reasoning_effort")
	delete(req, "thinking")
	if extra, ok := req["extra_body"].(map[string]interface{}); ok {
		delete(extra, "reasoning_effort")
		delete(extra, "thinking")
		delete(extra, "thinking_config")
	}
}

// extractSessionID extracts a session identifier for affinity.
// Priority: X-Session-ID > metadata.user_id > X-Client-Request-Id > conversation_id
func extractSessionID(r *http.Request, body map[string]interface{}) string {
	if sid := r.Header.Get("X-Session-ID"); sid != "" {
		return sid
	}
	if meta, ok := body["metadata"].(map[string]interface{}); ok {
		if uid, ok := meta["user_id"].(string); ok && uid != "" {
			return uid
		}
	}
	if crid := r.Header.Get("X-Client-Request-Id"); crid != "" {
		return crid
	}
	if cid, ok := body["conversation_id"].(string); ok && cid != "" {
		return cid
	}
	return ""
}

// applyAccountError classifies an upstream failure and applies cooldown/backoff.
func (s *Server) applyAccountError(account *db.Account, status int, body []byte, headers http.Header) (time.Duration, string) {
	decision := ClassifyError(status, body, headers)
	if decision.UseBackoff {
		level, err := s.db.BumpBackoff(account.ID, BackoffMaxLevel, BackoffBaseMs/1000, BackoffMaxMs/1000)
		if err != nil {
			log.Printf("[BACKOFF] BumpBackoff failed for %s: %v", account.ID, err)
		}
		cooldown := BackoffCooldown(level)
		s.db.MarkAccountError(account.ID, "rate_limit: "+decision.Reason, 0)
		return cooldown, fmt.Sprintf("backoff L%d (%s) cooldown %v", level, decision.Reason, cooldown)
	}
	cooldown := decision.CooldownMs
	cooldownSecs := int(cooldown.Seconds())
	if cooldownSecs < 1 {
		cooldownSecs = 1
	}
	s.db.MarkAccountError(account.ID, decision.Reason, cooldownSecs)
	return cooldown, fmt.Sprintf("cooldown %v (%s)", cooldown, decision.Reason)
}

// logFailedRequest persists a failed upstream request to usage_logs.
func (s *Server) logFailedRequest(r *http.Request, account *db.Account, provider, model string, body []byte, statusCode int, respBody string, startTime time.Time) {
	const failedLogCap = 100 * 1024
	reqBodyLog := string(body)
	if len(reqBodyLog) > failedLogCap {
		reqBodyLog = reqBodyLog[:failedLogCap] + "\n...(truncated)"
	}
	respLog := respBody
	if len(respLog) > failedLogCap {
		respLog = respLog[:failedLogCap] + "\n...(truncated)"
	}
	usageLog := &db.UsageLog{
		APIKeyID:     r.Header.Get("X-API-Key-ID"),
		AccountID:    account.ID,
		AccountEmail: account.Email,
		Provider:     provider,
		Model:        model,
		StatusCode:   statusCode,
		LatencyMs:    int(time.Since(startTime).Milliseconds()),
		Error:        respLog,
		RequestBody:  reqBodyLog,
		ResponseBody: respLog,
	}
	if err := s.db.LogUsage(usageLog); err != nil {
		log.Printf("[LOG-FAILED] persist failed request: %v", err)
		return
	}
	BroadcastRequest(map[string]interface{}{
		"id":            usageLog.ID,
		"created_at":    usageLog.CreatedAt.Format(time.RFC3339Nano),
		"account_id":    usageLog.AccountID,
		"account_email": usageLog.AccountEmail,
		"provider":      usageLog.Provider,
		"model":         usageLog.Model,
		"status_code":   usageLog.StatusCode,
		"latency_ms":    usageLog.LatencyMs,
		"error":         respLog,
	})
}
