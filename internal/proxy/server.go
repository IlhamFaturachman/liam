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
	"github.com/liam-auto/liam/internal/providers/elevenlabs"
	liamsync "github.com/liam-auto/liam/internal/sync"
)

// Server holds the proxy server state
type Server struct {
	cfg           *config.Config
	db            *db.Database
	pool          *AccountPool
	ag            *antigravity.Executor
	kiro          *kiro.Executor
	el            *elevenlabs.Executor
	providers     *providerRegistry
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
		el:           elevenlabs.NewExecutor(),
		harvest:      harvest.NewHarvestService(cfg, database),
		registry:     registry,
		aliases:      aliases,
		integrations: integrations.NewService(registry),
		combo:        NewComboHandler(database),
	}

	// Wire up harvest quota fetching immediately upon import
	s.harvest.OnAccountImported = func(account *db.Account) {
		if account.Provider == "antigravity" {
			var creds struct {
				AccessToken string `json:"access_token"`
			}
			if err := json.Unmarshal(account.Credentials, &creds); err == nil && creds.AccessToken != "" {
				if qr, qErr := antigravity.FetchQuota(creds.AccessToken); qErr == nil && qr != nil {
					account.QuotaTotal = qr.Total
					account.QuotaRemaining = qr.Total - qr.Used
					account.Plan = qr.Plan
					if qr.ResetAt != "" {
						if t, parseErr := time.Parse(time.RFC3339, qr.ResetAt); parseErr == nil {
							account.QuotaResetAt = &t
						}
					}
					if len(qr.Breakdown) > 0 {
						if b, mErr := json.Marshal(qr.Breakdown); mErr == nil {
							account.QuotaBreakdown = b
						}
					}
				}
			}
		} else if account.Provider == "kiro" {
			var creds struct {
				AccessToken string `json:"access_token"`
				ProfileARN  string `json:"profile_arn"`
			}
			if err := json.Unmarshal(account.Credentials, &creds); err == nil && creds.AccessToken != "" {
				if qr, qErr := kiro.FetchQuota(creds.AccessToken, creds.ProfileARN); qErr == nil && qr != nil {
					account.QuotaTotal = qr.Total
					account.QuotaRemaining = qr.Total - qr.Used
					account.Plan = qr.Plan
					if qr.ResetAt != "" {
						if t, parseErr := time.Parse(time.RFC3339, qr.ResetAt); parseErr == nil {
							account.QuotaResetAt = &t
						}
					}
					if len(qr.Breakdown) > 0 {
						if b, mErr := json.Marshal(qr.Breakdown); mErr == nil {
							account.QuotaBreakdown = b
						}
					}
				}
			}
		}

		// Fallback to UpsertAccount with or without quota data
		if err := database.UpsertAccount(account); err != nil {
			log.Printf("IMPORT ERROR: %s - %v", account.Email, err)
		}
	}

	// Build the provider registry. New backends drop in here without
	// touching server.go's request flow — the chat handler, refresh
	// loop, stats endpoints, and dashboard now all source their list
	// from this single registry.
	s.providers = newProviderRegistry()
	s.providers.Register(&ProviderInfo{
		ID:             "antigravity",
		Aliases:        []string{"ag", "antigravity"},
		Label:          "Antigravity",
		Icon:           "rocket_launch",
		SupportsImport: true,
		Executor:       s.ag,
		Refresh:        RefreshIfNeeded,
	})
	s.providers.Register(&ProviderInfo{
		ID:             "kiro",
		Aliases:        []string{"kr", "kiro"},
		Label:          "Kiro",
		Icon:           "cloud",
		SupportsImport: true,
		Executor:       s.kiro,
		Refresh:        RefreshKiroIfNeeded,
	})
	s.providers.Register(&ProviderInfo{
		ID:             "elevenlabs",
		Aliases:        []string{"el", "elevenlabs"},
		Label:          "ElevenLabs",
		Icon:           "graphic_eq",
		SupportsImport: false,
		Executor:       s.el,
	})

	// Initialize Supabase sync first so the models handler can wire it
	// for auto-sync of custom-model CRUD.
	syncClient := liamsync.NewClient(cfg.SupabaseURL, cfg.SupabaseKey)
	s.syncer = liamsync.NewSyncer(syncClient, database)

	s.modelsHandler = NewModelsHandler(s, database, registry, aliases, s.pool, s.ag, s.syncer)

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
	// NOTE: we deliberately do NOT install a global `middleware.Timeout`
	// here. Chi's timeout middleware cancels the request context after a
	// fixed duration, which is fine for short management calls but
	// catastrophic for streaming endpoints — `/v1/chat/completions` keeps
	// the connection open while the upstream tokens trickle in, and a
	// 120s cap there manifests as "stuck" responses + the
	// `superfluous response.WriteHeader call from … Timeout` warning we
	// were seeing in the log. The chat handler already enforces its own
	// per-attempt budgets via the upstream HTTP client + retry loop, so
	// the global timeout is redundant safety on top of fragility.
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
		r.Post("/audio/speech", s.handleAudioSpeech)
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
			r.Get("/settings/token-saver", s.handleGetTokenSaver)
			r.Post("/settings/token-saver", s.handleSetTokenSaver)

			// Stats & Usage
			r.Get("/stats", s.handleStats)
			r.Get("/usage/recent", s.HandleUsageRecent)
			r.Get("/usage/stats", s.HandleUsageStats)
			r.Get("/usage/chart", s.HandleUsageChart)
			r.Get("/usage/{id}", s.HandleUsageDetail)
			r.Get("/providers/stats", s.HandleProviderStats)
			r.Get("/overview", s.HandleOverview)

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
			r.Post("/accounts/{id}/excluded-models", s.handleSetExcludedModels)
			r.Post("/accounts/{id}/test", s.handleTestAccount)
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
			writeError(w, http.StatusUnauthorized, "Invalid Authorization format. Use: Bearer lyd-xxx")
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

	// Apply the operator-configured Kiro thinking default (LIAM_KIRO_THINKING_DEFAULT)
	// when the request didn't bring its own DSL suffix. Out-of-the-box
	// LIAM ships with the default set to "max" so every Kiro Claude
	// call gets the upstream's full reasoning budget without the user
	// having to remember the (max) syntax in every model id. Operators
	// who want lighter / disabled defaults set the env var.
	//
	// Note: only applied to Kiro routes. Antigravity / other providers
	// keep their existing behaviour because their reasoning_effort
	// semantics are different (-thinking suffix path below).
	if s.cfg.KiroThinkingDefault != "" && strings.ToLower(s.cfg.KiroThinkingDefault) != "off" {
		if strings.HasPrefix(model, "kr/") || strings.HasPrefix(model, "kiro/") {
			if _, ok := req["reasoning_effort"]; !ok {
				req["reasoning_effort"] = s.cfg.KiroThinkingDefault
				body, _ = json.Marshal(req)
			}
		}
	}

	// Option C Thinking: handle -thinking suffix (backward compat).
	//
	// For Antigravity we map the suffix to reasoning_effort=high and route
	// to the base model — that's how their generateContent API exposes
	// thinking budgets.
	//
	// For Kiro we forward the suffixed modelId as-is to the upstream
	// (codewhisperer.us-east-1.amazonaws.com): based on observation,
	// Kiro accepts model SKUs like "claude-sonnet-4.5-thinking"
	// directly. Stripping the suffix would silently downgrade thinking
	// requests to the non-thinking variant.
	if strings.HasSuffix(model, "-thinking") {
		isKiro := strings.HasPrefix(model, "kr/") || strings.HasPrefix(model, "kiro/")
		baseModel := strings.TrimSuffix(model, "-thinking")
		// Check if base model exists in registry
		_, baseErr := s.registry.Get(baseModel)

		if isKiro {
			// Pass-through: keep the suffix on the upstream modelId so
			// Kiro can dispatch to the thinking-enabled SKU. Don't set
			// reasoning_effort because Kiro ignores it on the wire.
			// We still log the routing decision below for debugging.
			_ = baseErr
		} else if baseErr == nil {
			// Antigravity (or any other registered base model): strip
			// suffix and inject reasoning_effort.
			model = baseModel
			req["model"] = baseModel
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

	// Determine provider from model. The registry resolves alias prefixes
	// (`kr/`, `ag/`, …) into a single canonical id so the rest of the
	// chat handler stays provider-agnostic.
	provider := s.resolveProviderFromModel(model)
	providerInfo := s.providers.ByID(provider)

	// Token savers (RTK + Caveman) — run AFTER thinking DSL has been
	// stripped from the model name and BEFORE combo dispatch / any
	// provider's translateRequest. This keeps both savers
	// provider-agnostic by operating on the OpenAI canonical shape
	// that all clients speak. Combo paths inherit the savings via
	// `body` and `req` (we always re-marshal in lockstep).
	// See internal/proxy/tokensaver.go for design notes on safety
	// w.r.t. Kiro overlay, tool calls, and thinking config.
	policy := loadTokenSaverPolicy(s.db, r.Header)
	body = applyTokenSavers(req, body, policy)

	// Extract session ID for session affinity
	sessionID := extractSessionID(r, req)

	// Check if model is a combo
	comboModels := s.combo.ResolveCombo(model)
	if comboModels != nil {
		// Combo mode: try each model in order until one succeeds
		s.handleComboRequest(w, r, req, body, comboModels, stream, startTime)
		return
	}

	// Pick best account (with session affinity). Per-account cooldown
	// is enforced by GetActiveAccounts via cooldown_until; we no longer
	// per-(account, model) lock since that turned single-account setups
	// into instant 503 storms.
	var lastErr error
	for attempt := 0; attempt < s.cfg.MaxRetriesPerRequest; attempt++ {
		account, err := s.pool.PickForSession(provider, model, sessionID)
		if err != nil {
			// All accounts in cooldown. Wait one backoff base unit and
			// try again so we don't immediately 503 the caller — the
			// next iteration will pick up whichever account exits
			// cooldown first.
			if attempt < s.cfg.MaxRetriesPerRequest-1 {
				wait := time.Duration(BackoffBaseMs) * time.Millisecond
				log.Printf("[RETRY %d] No active accounts (%v), sleeping %v", attempt+1, err, wait)
				time.Sleep(wait)
				continue
			}
			writeError(w, http.StatusServiceUnavailable, fmt.Sprintf("No available accounts: %v", err))
			return
		}

		// Inline token refresh if the provider registered a refresher.
		if providerInfo != nil && providerInfo.Refresh != nil {
			if refreshErr := providerInfo.Refresh(s.cfg, s.db, account); refreshErr != nil {
				log.Printf("[REFRESH] Warning for %s: %v", account.Email, refreshErr)
			}
		}

		// Get stable session ID for this account
		sessionID := s.pool.GetSessionID(account.ID)

		// Execute the request via the registered executor. Unknown
		// providers fall through to a clear 400 error so the operator
		// can spot the misconfiguration rather than silently routing
		// to the wrong backend.
		var resp *http.Response
		if providerInfo == nil || providerInfo.Executor == nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("Unsupported provider: %s", provider))
			return
		}
		resp, err = providerInfo.Executor.ExecuteWithSession(account, model, body, stream, sessionID)

		// --- Network/transport error ---
		if err != nil {
			lastErr = err
			cooldown, msg := s.applyAccountError(account, 0, []byte(err.Error()), nil)
			log.Printf("[RETRY %d] Account %s transport error: %v -> %s", attempt+1, account.Email, err, msg)
			if s.pool.Count(provider) <= 1 {
				time.Sleep(cooldown)
			}
			continue
		}

		// --- Non-retryable input errors (don't burn other accounts) ---
		if resp.StatusCode == 400 || resp.StatusCode == 404 || resp.StatusCode == 422 {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if IsNonRetryable(resp.StatusCode, respBody) {
				log.Printf("[NON-RETRYABLE] %s: %d — %s", account.Email, resp.StatusCode, string(respBody[:min(len(respBody), 100)]))
				// Persist failed request so the dashboard surfaces what
				// went wrong; previously these vanished silently and
				// users couldn't see the offending payload.
				s.logFailedRequest(r, account, provider, model, body, resp.StatusCode, string(respBody), startTime)
				writeError(w, resp.StatusCode, ExtractErrorMessage(respBody))
				return
			}
			// Generic 4xx (not in non-retryable list): treat as account error
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 100)]))
			cooldown, msg := s.applyAccountError(account, resp.StatusCode, respBody, resp.Header)
			log.Printf("[RETRY %d] Account %s HTTP %d -> %s | body: %s", attempt+1, account.Email, resp.StatusCode, msg, string(respBody[:min(len(respBody), 200)]))
			s.logFailedRequest(r, account, provider, model, body, resp.StatusCode, string(respBody), startTime)
			if s.pool.Count(provider) <= 1 {
				time.Sleep(cooldown)
			}
			continue
		}

		// --- 401/403/429 + 5xx — unified fallback path ---
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

		// Stream or return response
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

		// Save usage log after response is sent. Persist first so the SSE
		// payload carries the canonical id/created_at — the dashboard can
		// then upsert by id without waiting for /api/usage/recent.
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

	// All retries exhausted
	writeError(w, http.StatusBadGateway, fmt.Sprintf("All retries failed: %v", lastErr))
}

// streamResponse pipes SSE from upstream to client. While forwarding, we
// also tee the body into an in-memory buffer so the caller can extract
// usage metadata (prompt/completion tokens) from the final chunk after the
// stream finishes. Buffer is capped at 1 MB to avoid blowing memory on
// long generations — the usage chunk always lands at the very end and the
// last 64 KB is more than enough to recover it.
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

	const maxCapture = 1 * 1024 * 1024 // 1 MB rolling capture
	captured := make([]byte, 0, 64*1024)

	// Read line-by-line so model rewriting is never split across chunk boundaries.
	// bufio.Scanner strips the line terminator; we re-add \n on output (SSE is LF).
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

		// Append to capture, then trim to keep only the tail. The
		// usage record is always emitted last so we just need the
		// final ~64 KB to find it.
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

// forwardResponseCapture returns non-streaming response and captures body for logging
func (s *Server) forwardResponseCapture(w http.ResponseWriter, resp *http.Response, model string) string {
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	body = rewriteModelInBody(body, model)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
	return string(body)
}

// extractTokenUsage walks a captured response body (either a JSON object
// for non-streaming responses, or an SSE chat.completion.chunk stream for
// streamed responses) and returns the (input, output) token counts found
// in the final usage record. Returns (0, 0) when no usage data is
// present — the dashboard then renders the row as "0 in / 0 out" which
// is acceptable for tool-only or aborted requests, but for normal
// completions the providers always emit a metricsEvent / usage field
// that this function picks up.
func extractTokenUsage(body string) (in int, out int) {
	if body == "" {
		return 0, 0
	}
	trimmed := strings.TrimSpace(body)

	// Non-streaming JSON: {"usage": {"prompt_tokens": N, "completion_tokens": M}}
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

	// SSE stream: scan every "data: ..." line, keep the LAST chunk that
	// carries a usage record. Providers emit usage on the final chunk
	// (right before [DONE]) so iterating to the end is correct.
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
		// Capture the most recent non-zero usage. Some providers emit
		// running totals before the final chunk; others only emit once.
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

// logFailedRequest persists a failed upstream request to usage_logs so the
// dashboard surfaces the offending payload + error response. Without this,
// 4xx/5xx requests vanished silently and operators couldn't see what
// triggered "improperly formed request" / 429 / etc. Best-effort: errors
// here are logged but never propagate to the caller.
//
// We use a much larger truncation budget (100KB vs 5KB for successful
// requests) because the whole point of this row is to debug malformed
// payloads — a 5KB cap would clip OpenCode-class requests right at the
// tool definitions section and hide the actual schema bug.
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

// applyAccountError classifies an upstream failure and applies the
// resulting cooldown / backoff to the account. Returns the cooldown
// duration the caller should sleep when only one account exists for
// the provider, plus a short human-readable message for logging.
//
// Mirrors 9router's accountFallback flow: 429-class errors bump the
// per-account backoff_level (exponential 2/4/8/…/300 s), other failure
// modes use a fixed cooldown from ERROR_RULES, and unmatched errors
// fall back to TRANSIENT_COOLDOWN_MS (30 s). The previous per-(account,
// model) lock is gone — it locked single-account setups out within
// minutes for any unrecognised 429.
func (s *Server) applyAccountError(account *db.Account, status int, body []byte, headers http.Header) (time.Duration, string) {
	decision := ClassifyError(status, body, headers)
	if decision.UseBackoff {
		level, err := s.db.BumpBackoff(account.ID, BackoffMaxLevel, BackoffBaseMs/1000, BackoffMaxMs/1000)
		if err != nil {
			log.Printf("[BACKOFF] BumpBackoff failed for %s: %v", account.ID, err)
		}
		cooldown := BackoffCooldown(level)
		// We log the error message alongside the bumped level so the
		// dashboard's last_error column still surfaces something useful.
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

// handleComboRequest tries each model in a combo until one succeeds
func (s *Server) handleComboRequest(w http.ResponseWriter, r *http.Request, req map[string]interface{}, originalBody []byte, comboModels []string, stream bool, startTime time.Time) {
	var lastErr error

	for _, comboModel := range comboModels {
		// Update model in request
		req["model"] = comboModel
		body, _ := json.Marshal(req)

		provider := s.resolveProviderFromModel(comboModel)
		providerInfo := s.providers.ByID(provider)
		account, err := s.pool.PickForModel(provider, comboModel)
		if err != nil {
			lastErr = err
			log.Printf("[COMBO] No accounts for %s: %v", comboModel, err)
			continue // Try next model in combo
		}

		// Inline token refresh via the registered hook (no-op when the
		// provider doesn't need one).
		if providerInfo != nil && providerInfo.Refresh != nil {
			providerInfo.Refresh(s.cfg, s.db, account)
		}

		sessionID := s.pool.GetSessionID(account.ID)

		// Execute. Unknown providers in a combo are skipped — the
		// outer loop falls through to the next combo entry.
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
		// Re-broadcast with the canonical id/created_at so the dashboard
		// row carries the same identifier returned by /api/usage/recent.
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
	// Never leak raw access/refresh tokens to the dashboard. The derived
	// fields (HasCredentials, TokenExpiresAt) are enough to drive the UI.
	for i := range accounts {
		accounts[i].Credentials = nil
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

	// Validate credentials BEFORE writing to DB. The harvest pipeline hits
	// this endpoint for every imported account, and silently accepting
	// rows with missing refresh_token / unparseable expires_at is what
	// makes the dashboard fill up with accounts that "look fine" but die
	// the moment their access_token expires (refresh.go can't recover
	// without a refresh_token).
	//
	// We intentionally only enforce this for OAuth providers (AG/Kiro);
	// other providers may carry session cookies or API keys with no
	// expiry concept.
	switch input.Provider {
	case "antigravity":
		var c db.AGCredentials
		if err := json.Unmarshal(input.Credentials, &c); err != nil {
			writeError(w, http.StatusBadRequest, "credentials JSON invalid: "+err.Error())
			return
		}
		if strings.TrimSpace(c.RefreshToken) == "" {
			writeError(w, http.StatusBadRequest, "refresh_token is required for antigravity accounts (without it the worker can't keep the access_token alive past 1 hour)")
			return
		}
		// expires_at is best-effort: if absent, we set "now" so the
		// background worker picks the row up on its first tick. Don't
		// reject — some upstream callers may not compute it.
		if strings.TrimSpace(c.ExpiresAt) == "" {
			c.ExpiresAt = time.Now().UTC().Format(time.RFC3339)
			if patched, err := json.Marshal(c); err == nil {
				input.Credentials = patched
			}
		} else if _, err := time.Parse(time.RFC3339, c.ExpiresAt); err != nil {
			if _, err2 := time.Parse(time.RFC3339Nano, c.ExpiresAt); err2 != nil {
				writeError(w, http.StatusBadRequest, "expires_at must be RFC3339 timestamp, got: "+c.ExpiresAt)
				return
			}
		}
	case "kiro":
		var c db.KiroCredentials
		if err := json.Unmarshal(input.Credentials, &c); err != nil {
			writeError(w, http.StatusBadRequest, "credentials JSON invalid: "+err.Error())
			return
		}
		if strings.TrimSpace(c.RefreshToken) == "" {
			writeError(w, http.StatusBadRequest, "refresh_token is required for kiro accounts")
			return
		}
		if strings.TrimSpace(c.ExpiresAt) == "" {
			c.ExpiresAt = time.Now().UTC().Format(time.RFC3339)
			if patched, err := json.Marshal(c); err == nil {
				input.Credentials = patched
			}
		}
	case "elevenlabs":
		s.handleAddElevenLabsAccount(w, input.Credentials)
		return
	}

	account := &db.Account{
		Provider:    input.Provider,
		Email:       input.Email,
		Status:      "active",
		Credentials: input.Credentials,
		AuthMethod:  "imported",
	}

	if err := s.db.UpsertAccount(account); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if s.syncer != nil {
		s.syncer.PushAccountAsync(account)
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
	// Mirror the delete to Supabase + record a tombstone so the next pull
	// doesn't resurrect this row. Safe to call when sync is disabled.
	if s.syncer != nil {
		s.syncer.DeleteAccountAsync(id)
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
		"base_url":    baseURL,
		"default_url": fmt.Sprintf("http://localhost:%d/v1", s.cfg.Port),
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
		"accounts_total":       len(accounts),
		"accounts_active":      active,
		"accounts_by_provider": byProvider,
		"api_keys_total":       len(keys),
	})
}

// resolveProviderFromModel turns a model id ("kr/claude-sonnet-4.5",
// "ag/gemini-3-flash") into the canonical provider id ("kiro",
// "antigravity"). Built on top of the provider registry so adding a new
// backend automatically picks up its alias here without further code
// changes. Falls back to the first registered provider for ambiguous
// model strings (preserves the legacy behaviour where unknown models
// went to Antigravity).
func (s *Server) resolveProviderFromModel(model string) string {
	if s == nil || s.providers == nil {
		return "antigravity"
	}
	info, _ := s.providers.ResolveModel(model)
	if info != nil {
		return info.ID
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
