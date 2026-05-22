package proxy

import (
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/liam-auto/liam/internal/dashboard"
)

// buildRouter constructs the chi router with all route registrations.
// Extracted from Start() to keep server.go focused on initialization.
func (s *Server) buildRouter() *chi.Mux {
	r := chi.NewRouter()

	// Middleware
	r.Use(quietLogger)
	r.Use(middleware.Recoverer)
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

	// Dashboard
	dash := dashboard.NewHandler(s.db)
	r.Get("/dashboard", dash.ServeStatic)
	r.Get("/dashboard/*", dash.ServeStatic)

	// Management API
	r.Route("/api", func(r chi.Router) {
		r.Post("/auth/login", dash.HandleLogin)
		r.Post("/auth/verify", dash.HandleVerify)
		r.Post("/auth/password", dash.HandleChangePassword)

		r.Group(func(r chi.Router) {
			r.Use(apiAuthMiddleware(s.db))

			// Accounts
			r.Get("/accounts", s.handleListAccounts)
			r.Post("/accounts", s.handleAddAccount)
			r.Delete("/accounts/{id}", s.handleDeleteAccount)
			r.Post("/accounts/reorder", s.handleReorderAccounts)
			r.Post("/accounts/import/ag", s.handleImportAG)
			r.Post("/accounts/import/kiro", s.handleImportKiro)
			r.Get("/oauth/ag/authorize", s.handleAGAuthorize)
			r.Post("/oauth/ag/exchange", s.handleAGExchange)
			r.Post("/accounts/{id}/refresh-quota", s.handleRefreshQuota)
			r.Post("/accounts/{id}/excluded-models", s.handleSetExcludedModels)
			r.Post("/accounts/{id}/test", s.handleTestAccount)
			r.Patch("/accounts/{id}", s.handleEditAccount)

			// Keys
			r.Get("/keys", s.handleListKeys)
			r.Post("/keys", s.handleCreateKey)
			r.Delete("/keys/{id}", s.handleDeleteKey)

			// Settings
			r.Get("/settings/base-url", s.handleGetBaseURL)
			r.Post("/settings/base-url", s.handleSetBaseURL)
			r.Get("/settings/token-saver", s.handleGetTokenSaver)
			r.Post("/settings/token-saver", s.handleSetTokenSaver)
			r.Get("/settings/routing", s.handleGetRouting)
			r.Post("/settings/routing", s.handleSetRouting)

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

			// Harvest
			r.Post("/harvest/start", s.harvest.HandleStart)
			r.Get("/harvest/status", s.harvest.HandleStatus)
			r.Post("/harvest/stop", s.harvest.HandleStop)
		})
	})

	// SSE live feed
	r.Get("/sse/requests", s.HandleSSE)

	return r
}
