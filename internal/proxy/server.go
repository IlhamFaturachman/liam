package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/liam-auto/liam/internal/config"
	"github.com/liam-auto/liam/internal/dashboard"
	"github.com/liam-auto/liam/internal/db"
	"github.com/liam-auto/liam/internal/harvest"
	"github.com/liam-auto/liam/internal/integrations"
	"github.com/liam-auto/liam/internal/models"
	"github.com/liam-auto/liam/internal/providers/antigravity"
	"github.com/liam-auto/liam/internal/providers/kiro"
	liamsync "github.com/liam-auto/liam/internal/sync"
)

// Server holds the proxy server state
type Server struct {
	cfg           *config.Config
	db            *db.Database
	pool          *AccountPool
	ag            *antigravity.Executor
	kiro          *kiro.Executor
	harvest       *harvest.HarvestService
	registry      *models.Registry
	aliases       *models.AliasStore
	modelsHandler *ModelsHandler
	integrations  *integrations.Service
	syncer        *liamsync.Syncer
	combo         *ComboHandler
}

// GetPort implements ServerConfig for ModelsHandler
func (s *Server) GetPort() int {
	return s.cfg.Port
}

// Start initializes and starts the HTTP server
func Start(cfg *config.Config, database *db.Database) error {
	registry := models.NewRegistry(database.Conn())
	if err := registry.SeedBuiltIn(); err != nil {
		log.Printf("[REGISTRY] Seed warning: %v", err)
	}
	aliases := models.NewAliasStore(database.Conn())

	s := &Server{
		cfg:          cfg,
		db:           database,
		pool:         NewAccountPool(database, cfg),
		ag:           antigravity.NewExecutor(cfg),
		kiro:         kiro.NewExecutor(),
		harvest:      harvest.NewHarvestService(cfg, database),
		registry:     registry,
		aliases:      aliases,
		integrations: integrations.NewService(),
		combo:        NewComboHandler(database),
	}
	s.modelsHandler = NewModelsHandler(s, database, registry, aliases, s.pool, s.ag)

	// Initialize Supabase sync
	syncClient := liamsync.NewClient(cfg.SupabaseURL, cfg.SupabaseKey)
	s.syncer = liamsync.NewSyncer(syncClient, database)
	if s.syncer.IsEnabled() {
		if err := liamsync.EnsureTables(syncClient); err != nil {
			log.Printf("[SYNC] Warning: %v", err)
		}
		if err := s.syncer.InitialSync(); err != nil {
			log.Printf("[SYNC] Initial sync warning: %v", err)
		}
		// Start sync worker (every 30s)
		go s.syncWorker()
	}

	// Ensure internal test key exists
	if _, err := database.EnsureInternalTestKey(); err != nil {
		log.Printf("[INIT] Internal test key warning: %v", err)
	}

	r := chi.NewRouter()

	// Middleware
	r.Use(quietLogger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(120 * time.Second))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"*"},
		AllowCredentials: true,
	}))

	// Health
	r.Get("/health", s.handleHealth)

	// OpenAI-compatible API
	r.Route("/v1", func(r chi.Router) {
		r.Use(s.authMiddleware)
		r.Post("/chat/completions", s.handleChatCompletions)
		r.Get("/models", s.handleModels)
	})

	// Dashboard (new handler)
	dash := dashboard.NewHandler(database)
	r.Get("/dashboard", dash.ServeStatic)
	r.Get("/dashboard/*", dash.ServeStatic)

	// Management API (protected: localhost OR valid dashboard token)
	r.Route("/api", func(r chi.Router) {
		// Auth endpoints are exempt from auth middleware (login needs to work without token)
		r.Post("/auth/login", dash.HandleLogin)
		r.Post("/auth/verify", dash.HandleVerify)
		r.Post("/auth/password", dash.HandleChangePassword)

		// All other /api routes require auth
		r.Group(func(r chi.Router) {
			r.Use(apiAuthMiddleware(database))

		// Accounts
		r.Get("/accounts", s.handleListAccounts)
		r.Post("/accounts", s.handleAddAccount)
		r.Delete("/accounts/{id}", s.handleDeleteAccount)

		// Keys
		r.Get("/keys", s.handleListKeys)
		r.Post("/keys", s.handleCreateKey)
		r.Delete("/keys/{id}", s.handleDeleteKey)

		// Settings
		r.Get("/settings/base-url", s.handleGetBaseURL)
		r.Post("/settings/base-url", s.handleSetBaseURL)

		// Stats & Usage
		r.Get("/stats", s.handleStats)
		r.Get("/usage/recent", s.HandleUsageRecent)
		r.Get("/usage/stats", s.HandleUsageStats)
		r.Get("/usage/chart", s.HandleUsageChart)
		r.Get("/usage/{id}", s.HandleUsageDetail)
		r.Get("/providers/stats", s.HandleProviderStats)

		// Models
		r.Get("/models", s.modelsHandler.HandleListModels)
		r.Post("/models/custom", s.modelsHandler.HandleAddCustomModel)
		r.Delete("/models/custom/*", s.modelsHandler.HandleRemoveModel)
		r.Post("/models/toggle/*", s.modelsHandler.HandleToggleModel)
		r.Post("/models/test", s.modelsHandler.HandleTestModel)
		r.Post("/providers/{alias}/refresh-models", s.modelsHandler.HandleRefreshModels)
		r.Post("/providers/{alias}/disable-all", s.modelsHandler.HandleDisableAllForProvider)

		// Aliases
		r.Get("/aliases", s.modelsHandler.HandleListAliases)
		r.Post("/aliases", s.modelsHandler.HandleSetAlias)
		r.Delete("/aliases/{alias}", s.modelsHandler.HandleDeleteAlias)

		// Integrations
		r.Get("/integrations", s.integrations.HandleList)
		r.Get("/integrations/{tool}", s.integrations.HandleGet)
		r.Post("/integrations/{tool}/snippet", s.integrations.HandleSnippet)
		r.Post("/integrations/{tool}/apply", s.integrations.HandleApply)
		r.Post("/integrations/{tool}/reset", s.integrations.HandleReset)

		// Sync
		r.Get("/sync/status", s.handleSyncStatus)
		r.Post("/sync/now", s.handleSyncNow)

		// Combos
		r.Get("/combos", s.combo.HandleList)
		r.Post("/combos", s.combo.HandleCreate)
		r.Put("/combos/{id}", s.combo.HandleUpdate)
		r.Delete("/combos/{id}", s.combo.HandleDelete)

		// Routing settings
		r.Get("/settings/routing", s.handleGetRouting)
		r.Post("/settings/routing", s.handleSetRouting)

		// Account reorder (drag-and-drop)
		r.Post("/accounts/reorder", s.handleReorderAccounts)

		// Account import (manual add)
		r.Post("/accounts/import/ag", s.handleImportAG)
		r.Post("/accounts/import/kiro", s.handleImportKiro)
		r.Get("/oauth/ag/authorize", s.handleAGAuthorize)
		r.Post("/oauth/ag/exchange", s.handleAGExchange)

		// Account actions
		r.Post("/accounts/{id}/refresh-quota", s.handleRefreshQuota)
		r.Get("/accounts/{id}/locks", s.handleGetAccountLocks)
		r.Post("/accounts/{id}/excluded-models", s.handleSetExcludedModels)
		r.Patch("/accounts/{id}", s.handleEditAccount)

		// Harvest
		r.Post("/harvest/start", s.harvest.HandleStart)
		r.Get("/harvest/status", s.harvest.HandleStatus)
		r.Post("/harvest/stop", s.harvest.HandleStop)
		}) // end r.Group (auth-protected)
	}) // end r.Route("/api")

	// SSE live feed (outside /api route group)
	r.Get("/sse/requests", s.HandleSSE)

	addr := fmt.Sprintf(":%d", cfg.Port)
	return http.ListenAndServe(addr, r)
}

// --- Auth Middleware ---

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "" {
			writeError(w, http.StatusUnauthorized, "Missing Authorization header")
			return
		}

		rawKey := strings.TrimPrefix(auth, "Bearer ")
		if rawKey == auth {
			writeError(w, http.StatusUnauthorized, "Invalid Authorization format. Use: Bearer li-xxx")
			return
		}

		key, err := s.db.ValidateAPIKey(rawKey)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}

		// Store key in context for later use
		r.Header.Set("X-API-Key-ID", key.ID)
		next.ServeHTTP(w, r)
	})
}

// --- Chat Completions (main proxy endpoint) ---

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()

	// Parse request body
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

	// Resolve aliases (user-defined shortcuts → canonical model)
	if resolved := s.aliases.Resolve(model); resolved != "" {
		model = resolved
		req["model"] = resolved
		body, _ = json.Marshal(req)
	}

	// Thinking DSL: handle model(value) syntax — e.g. ag/claude-opus-4-6(8192) or ag/model(high)
	if idx := strings.LastIndex(model, "("); idx > 0 && strings.HasSuffix(model, ")") {
		baseModel := model[:idx]
		thinkingValue := model[idx+1 : len(model)-1]
		model = baseModel
		req["model"] = baseModel

		// Map value to reasoning_effort
		switch thinkingValue {
		case "none":
			req["reasoning_effort"] = "none"
		case "auto":
			// Don't set — let model decide
		case "low":
			req["reasoning_effort"] = "low"
		case "medium":
			req["reasoning_effort"] = "medium"
		case "high":
			req["reasoning_effort"] = "high"
		case "max":
			req["reasoning_effort"] = "max"
		default:
			// Numeric value — store as direct budget (will be mapped in executor)
			if _, err := fmt.Sscanf(thinkingValue, "%d", new(int)); err == nil {
				req["reasoning_effort"] = thinkingValue // Pass numeric string, executor maps it
			} else {
				req["reasoning_effort"] = "high" // Unknown → default high
			}
		}
		body, _ = json.Marshal(req)
	}

	// Option C Thinking: handle -thinking suffix (backward compat)
	if strings.HasSuffix(model, "-thinking") {
		baseModel := strings.TrimSuffix(model, "-thinking")
		// Check if base model exists in registry
		if _, err := s.registry.Get(baseModel); err == nil {
			model = baseModel
			req["model"] = baseModel
			// Set reasoning_effort = "high" if not already set
			if _, ok := req["reasoning_effort"]; !ok {
				req["reasoning_effort"] = "high"
			}
			body, _ = json.Marshal(req)
		}
	}

	// Strip thinking config if model has thinking:false flag
	if s.registry.IsThinkingDisabled(model) {
		stripThinkingFromRequest(req)
		body, _ = json.Marshal(req)
	}

	stream, _ := req["stream"].(bool)

	// Determine provider from model
	provider := resolveProvider(model)

	// Extract session ID for session affinity
	sessionID := extractSessionID(r, req)

	// Check if model is a combo
	comboModels := s.combo.ResolveCombo(model)
	if comboModels != nil {
		// Combo mode: try each model in order until one succeeds
		s.handleComboRequest(w, r, req, body, comboModels, stream, startTime)
		return
	}

	// Pick best account (with per-model lock + session affinity)
	var lastErr error
	for attempt := 0; attempt < s.cfg.MaxRetriesPerRequest; attempt++ {
		account, err := s.pool.PickForSession(provider, model, sessionID)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, fmt.Sprintf("No available accounts: %v", err))
			return
		}

		// Inline token refresh if needed
		if provider == "antigravity" {
			if refreshErr := RefreshIfNeeded(s.cfg, s.db, account); refreshErr != nil {
				log.Printf("[REFRESH] Warning for %s: %v", account.Email, refreshErr)
			}
		} else if provider == "kiro" {
			if refreshErr := RefreshKiroIfNeeded(s.cfg, s.db, account); refreshErr != nil {
				log.Printf("[REFRESH] Warning for %s: %v", account.Email, refreshErr)
			}
		}

		// Get stable session ID for this account
		sessionID := s.pool.GetSessionID(account.ID)

		// Execute request based on provider
		var resp *http.Response
		switch provider {
		case "antigravity":
			resp, err = s.ag.ExecuteWithSession(account, model, body, stream, sessionID)
		case "kiro":
			resp, err = s.kiro.ExecuteWithSession(account, model, body, stream, sessionID)
		default:
			writeError(w, http.StatusBadRequest, fmt.Sprintf("Unsupported provider: %s", provider))
			return
		}

		if err != nil {
			lastErr = err
			log.Printf("[RETRY %d] Account %s error: %v", attempt+1, account.Email, err)
			cooldown := s.pool.CalculateCooldown(account.ConsecutiveErrors + 1)
			s.db.MarkAccountError(account.ID, err.Error(), cooldown)
			// Set per-model lock
			if model != "" {
				s.db.SetModelLock(account.ID, model, time.Now().UTC().Add(time.Duration(cooldown)*time.Second))
			}
			continue
		}

		// Check non-retryable errors (don't retry — all accounts will fail with same input)
		if resp.StatusCode == 400 || resp.StatusCode == 404 || resp.StatusCode == 422 {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if IsNonRetryable(resp.StatusCode, respBody) {
				log.Printf("[NON-RETRYABLE] %s: %d — %s", account.Email, resp.StatusCode, string(respBody[:min(len(respBody), 100)]))
				writeError(w, resp.StatusCode, ExtractErrorMessage(respBody))
				return
			}
			// Not non-retryable 400 — treat as account error, retry
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 100)]))
			cooldown := s.pool.CalculateCooldown(account.ConsecutiveErrors + 1)
			s.db.MarkAccountError(account.ID, lastErr.Error(), cooldown)
			continue
		}

		// Check upstream status
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			lastErr = fmt.Errorf("auth error %d from upstream", resp.StatusCode)
			log.Printf("[RETRY %d] Account %s auth error: %d", attempt+1, account.Email, resp.StatusCode)
			cooldown := s.pool.CalculateCooldown(account.ConsecutiveErrors + 1)
			s.db.MarkAccountError(account.ID, lastErr.Error(), cooldown)
			if model != "" {
				s.db.SetModelLock(account.ID, model, time.Now().UTC().Add(time.Duration(cooldown)*time.Second))
			}
			resp.Body.Close()
			continue
		}

		if resp.StatusCode == 429 {
			// Use 429 Decision Engine (categorized rate limit handling)
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			decision := Categorize429(resp.StatusCode, respBody, resp.Header)

			switch decision.Category {
			case SoftRetry:
				// Retry immediately same account — don't switch, don't cooldown
				log.Printf("[RETRY %d] Account %s soft rate limit (%s), retrying in %v", attempt+1, account.Email, decision.Reason, decision.Cooldown)
				time.Sleep(decision.Cooldown)
				continue

			case InstantRetrySameAuth:
				// Tiny delay, same account
				log.Printf("[RETRY %d] Account %s instant retry (%s), waiting %v", attempt+1, account.Email, decision.Reason, decision.Cooldown)
				time.Sleep(decision.Cooldown)
				continue

			case ShortCooldownSwitch:
				// Brief cooldown on this account, try different account
				log.Printf("[RETRY %d] Account %s short cooldown (%s), switching account for %v", attempt+1, account.Email, decision.Reason, decision.Cooldown)
				cooldownSecs := int(decision.Cooldown.Seconds())
				if cooldownSecs < 5 { cooldownSecs = 5 }
				s.db.MarkAccountError(account.ID, "rate_limited: "+decision.Reason, cooldownSecs)
				if model != "" {
					s.db.SetModelLock(account.ID, model, time.Now().UTC().Add(decision.Cooldown))
				}
				lastErr = fmt.Errorf("rate limited (%s)", decision.Reason)
				continue

			case FullQuotaExhausted:
				// Long cooldown, account exhausted
				log.Printf("[RETRY %d] Account %s quota exhausted (%s), cooldown %v", attempt+1, account.Email, decision.Reason, decision.Cooldown)
				cooldownSecs := int(decision.Cooldown.Seconds())
				if cooldownSecs < 300 { cooldownSecs = 300 }
				s.db.MarkAccountError(account.ID, "quota_exhausted: "+decision.Reason, cooldownSecs)
				if model != "" {
					s.db.SetModelLock(account.ID, model, time.Now().UTC().Add(decision.Cooldown))
				}
				lastErr = fmt.Errorf("quota exhausted (%s)", decision.Reason)
				continue
			}
		}

		// Success - mark account healthy
		s.db.MarkAccountSuccess(account.ID)

		// Truncate request body for logging (max 5KB)
		reqBodyLog := string(body)
		if len(reqBodyLog) > 5120 {
			reqBodyLog = reqBodyLog[:5120] + "\n...(truncated)"
		}

		// Log usage
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

		// Broadcast to SSE dashboard
		BroadcastRequest(map[string]interface{}{
			"time":       time.Now().UTC().Format("15:04:05"),
			"model":      model,
			"account":    account.Email,
			"latency_ms": latency,
			"status":     resp.StatusCode,
		})

		// Stream or return response
		if stream {
			s.streamResponse(w, resp)
			usageLog.ResponseBody = "(streaming response)"
		} else {
			respBody := s.forwardResponseCapture(w, resp)
			if len(respBody) > 5120 {
				usageLog.ResponseBody = respBody[:5120] + "\n...(truncated)"
			} else {
				usageLog.ResponseBody = respBody
			}
		}

		// Save usage log after response is sent
		s.db.LogUsage(usageLog)
		return
	}

	// All retries exhausted
	writeError(w, http.StatusBadGateway, fmt.Sprintf("All retries failed: %v", lastErr))
}

// streamResponse pipes SSE from upstream to client
func (s *Server) streamResponse(w http.ResponseWriter, resp *http.Response) {
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(resp.StatusCode)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			flusher.Flush()
		}
		if err != nil {
			if err != io.EOF {
				log.Printf("Stream read error: %v", err)
			}
			break
		}
	}
}

// forwardResponseCapture returns non-streaming response and captures body for logging
func (s *Server) forwardResponseCapture(w http.ResponseWriter, resp *http.Response) string {
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
	return string(body)
}

// --- Models endpoint ---

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	// Use registry for enabled models
	registryModels, err := s.registry.List("")
	if err != nil {
		writeError(w, 500, "failed to list models")
		return
	}

	modelsList := []map[string]interface{}{}
	for _, m := range registryModels {
		if !m.IsEnabled {
			continue
		}
		owner := "liam"
		if p := getProviderName(m.ProviderAlias); p != "" {
			owner = p
		}
		modelsList = append(modelsList, map[string]interface{}{
			"id":       m.ID,
			"object":   "model",
			"owned_by": owner,
		})
	}

	// Add combo names as virtual models
	combos, _ := s.db.ListCombos()
	for _, c := range combos {
		modelsList = append(modelsList, map[string]interface{}{
			"id":       c.Name,
			"object":   "model",
			"owned_by": "liam-combo",
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"object": "list",
		"data":   modelsList,
	})
}

func getProviderName(alias string) string {
	switch alias {
	case "ag":
		return "antigravity"
	case "kr":
		return "kiro"
	}
	return alias
}

// syncWorker runs bidirectional sync every 30 seconds
func (s *Server) syncWorker() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if err := s.syncer.Sync(); err != nil {
			log.Printf("[SYNC] Error: %v", err)
		}
	}
}

// handleComboRequest tries each model in a combo until one succeeds
func (s *Server) handleComboRequest(w http.ResponseWriter, r *http.Request, req map[string]interface{}, originalBody []byte, comboModels []string, stream bool, startTime time.Time) {
	var lastErr error

	for _, comboModel := range comboModels {
		// Update model in request
		req["model"] = comboModel
		body, _ := json.Marshal(req)

		provider := resolveProvider(comboModel)
		account, err := s.pool.PickForModel(provider, comboModel)
		if err != nil {
			lastErr = err
			log.Printf("[COMBO] No accounts for %s: %v", comboModel, err)
			continue // Try next model in combo
		}

		// Inline token refresh
		if provider == "antigravity" {
			RefreshIfNeeded(s.cfg, s.db, account)
		} else if provider == "kiro" {
			RefreshKiroIfNeeded(s.cfg, s.db, account)
		}

		sessionID := s.pool.GetSessionID(account.ID)

		// Execute
		var resp *http.Response
		switch provider {
		case "antigravity":
			resp, err = s.ag.ExecuteWithSession(account, comboModel, body, stream, sessionID)
		case "kiro":
			resp, err = s.kiro.ExecuteWithSession(account, comboModel, body, stream, sessionID)
		default:
			continue
		}

		if err != nil {
			lastErr = err
			log.Printf("[COMBO] %s failed (account %s): %v", comboModel, account.Email, err)
			cooldown := s.pool.CalculateCooldown(account.ConsecutiveErrors + 1)
			s.db.MarkAccountError(account.ID, err.Error(), cooldown)
			s.db.SetModelLock(account.ID, comboModel, time.Now().UTC().Add(time.Duration(cooldown)*time.Second))
			continue
		}

		if resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 429 {
			lastErr = fmt.Errorf("HTTP %d from %s", resp.StatusCode, comboModel)
			log.Printf("[COMBO] %s returned %d, trying next", comboModel, resp.StatusCode)
			cooldown := s.pool.CalculateCooldown(account.ConsecutiveErrors + 1)
			s.db.MarkAccountError(account.ID, lastErr.Error(), cooldown)
			s.db.SetModelLock(account.ID, comboModel, time.Now().UTC().Add(time.Duration(cooldown)*time.Second))
			resp.Body.Close()
			continue
		}

		// Success
		s.db.MarkAccountSuccess(account.ID)

		// Log
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
			s.streamResponse(w, resp)
			usageLog.ResponseBody = "(streaming response)"
		} else {
			respBody := s.forwardResponseCapture(w, resp)
			if len(respBody) > 5120 {
				usageLog.ResponseBody = respBody[:5120] + "\n...(truncated)"
			} else {
				usageLog.ResponseBody = respBody
			}
		}
		s.db.LogUsage(usageLog)
		return
	}

	// All combo models failed
	writeError(w, http.StatusBadGateway, fmt.Sprintf("All models in combo failed: %v", lastErr))
}

// handleSyncStatus returns current sync status for dashboard
func (s *Server) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.syncer.Status())
}

// handleSyncNow triggers an immediate sync
func (s *Server) handleSyncNow(w http.ResponseWriter, r *http.Request) {
	if !s.syncer.IsEnabled() {
		writeError(w, 400, "Supabase sync not configured. Set SUPABASE_URL and SUPABASE_KEY env vars.")
		return
	}
	if err := s.syncer.Sync(); err != nil {
		writeError(w, 500, fmt.Sprintf("Sync failed: %v", err))
		return
	}
	writeJSON(w, 200, map[string]interface{}{"status": "synced", "last_sync": s.syncer.LastSyncTime().Format(time.RFC3339)})
}

// stripThinkingFromRequest removes thinking-related fields from the OpenAI request
func stripThinkingFromRequest(req map[string]interface{}) {
	delete(req, "reasoning_effort")
	delete(req, "thinking")
	if extra, ok := req["extra_body"].(map[string]interface{}); ok {
		delete(extra, "reasoning_effort")
		delete(extra, "thinking")
		delete(extra, "thinking_config")
	}
}

// extractSessionID extracts a session identifier from the request for session affinity
// Priority: X-Session-ID header > metadata.user_id > X-Client-Request-Id > conversation_id
func extractSessionID(r *http.Request, body map[string]interface{}) string {
	// 1. Explicit session header
	if sid := r.Header.Get("X-Session-ID"); sid != "" {
		return sid
	}
	// 2. metadata.user_id (Claude Code sends this)
	if meta, ok := body["metadata"].(map[string]interface{}); ok {
		if uid, ok := meta["user_id"].(string); ok && uid != "" {
			return uid
		}
	}
	// 3. X-Client-Request-Id
	if crid := r.Header.Get("X-Client-Request-Id"); crid != "" {
		return crid
	}
	// 4. conversation_id in body
	if cid, ok := body["conversation_id"].(string); ok && cid != "" {
		return cid
	}
	// 5. No session ID found — no affinity
	return ""
}

// quietLogger is a chi middleware that logs requests except for noisy polling endpoints
func quietLogger(next http.Handler) http.Handler {
	logger := middleware.Logger(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip logging for noisy polling endpoints
		if r.URL.Path == "/api/harvest/status" ||
			r.URL.Path == "/sse/requests" ||
			r.URL.Path == "/api/usage/recent" ||
			r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		logger.ServeHTTP(w, r)
	})
}

// --- Health ---

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	accounts, _ := s.db.ListAccounts("")
	active := 0
	for _, a := range accounts {
		if a.Status == "active" {
			active++
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":          "ok",
		"total_accounts":  len(accounts),
		"active_accounts": active,
		"version":         "0.1.0",
	})
}

// --- Management API ---

func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	provider := r.URL.Query().Get("provider")
	accounts, err := s.db.ListAccounts(provider)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, accounts)
}

func (s *Server) handleAddAccount(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Provider    string          `json:"provider"`
		Email       string          `json:"email"`
		Credentials json.RawMessage `json:"credentials"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	account := &db.Account{
		Provider:    input.Provider,
		Email:       input.Email,
		Status:      "active",
		Credentials: input.Credentials,
	}

	if err := s.db.UpsertAccount(account); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, account)
}

func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, 400, "missing id")
		return
	}
	if err := s.db.DeleteAccount(id); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.db.ListAPIKeys()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name string `json:"name"`
	}
	json.NewDecoder(r.Body).Decode(&input)
	if input.Name == "" {
		input.Name = "default"
	}

	key, rawKey, err := s.db.CreateAPIKey(input.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"key": rawKey,
		"id":  key.ID,
	})
}

func (s *Server) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing id")
		return
	}
	if err := s.db.DeleteAPIKey(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleGetBaseURL(w http.ResponseWriter, r *http.Request) {
	baseURL := s.db.GetSetting("base_url", "")
	if baseURL == "" {
		baseURL = fmt.Sprintf("http://localhost:%d/v1", s.cfg.Port)
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"base_url":     baseURL,
		"default_url":  fmt.Sprintf("http://localhost:%d/v1", s.cfg.Port),
	})
}

func (s *Server) handleSetBaseURL(w http.ResponseWriter, r *http.Request) {
	var input struct {
		BaseURL string `json:"base_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := s.db.SetSetting("base_url", input.BaseURL); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	accounts, _ := s.db.ListAccounts("")
	keys, _ := s.db.ListAPIKeys()

	active := 0
	byProvider := map[string]int{}
	for _, a := range accounts {
		if a.Status == "active" {
			active++
		}
		byProvider[a.Provider]++
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"accounts_total":      len(accounts),
		"accounts_active":     active,
		"accounts_by_provider": byProvider,
		"api_keys_total":      len(keys),
	})
}

func resolveProvider(model string) string {
	if strings.HasPrefix(model, "ag/") {
		return "antigravity"
	}
	if strings.HasPrefix(model, "kr/") || strings.HasPrefix(model, "kiro/") {
		return "kiro"
	}
	return "antigravity"
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]interface{}{
		"error": map[string]interface{}{
			"message": message,
			"type":    "error",
		},
	})
}
