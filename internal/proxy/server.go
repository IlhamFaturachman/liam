package proxy

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/liam-auto/liam/internal/config"
	"github.com/liam-auto/liam/internal/db"
	"github.com/liam-auto/liam/internal/harvest"
	"github.com/liam-auto/liam/internal/integrations"
	"github.com/liam-auto/liam/internal/models"
	"github.com/liam-auto/liam/internal/providers/antigravity"
	"github.com/liam-auto/liam/internal/providers/kiro"
	liamsync "github.com/liam-auto/liam/internal/sync"
)

// Server holds the proxy server state.
type Server struct {
	cfg           *config.Config
	db            *db.Database
	pool          *AccountPool
	ag            *antigravity.Executor
	kiro          *kiro.Executor
	providers     *providerRegistry
	harvest       *harvest.HarvestService
	registry      *models.Registry
	aliases       *models.AliasStore
	modelsHandler *ModelsHandler
	integrations  *integrations.Service
	syncer        *liamsync.Syncer
	combo         *ComboHandler
}

// GetPort implements ServerConfig for ModelsHandler.
func (s *Server) GetPort() int {
	return s.cfg.Port
}

// Start initializes and starts the HTTP server.
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
		integrations: integrations.NewService(registry),
		combo:        NewComboHandler(database),
	}

	// Wire up harvest quota fetching on import
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

		if err := database.UpsertAccount(account); err != nil {
			log.Printf("IMPORT ERROR: %s - %v", account.Email, err)
		}
	}

	// Provider registry
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

	// Supabase sync
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
		go s.syncWorker()
	}

	// Ensure internal test key exists
	if _, err := database.EnsureInternalTestKey(); err != nil {
		log.Printf("[INIT] Internal test key warning: %v", err)
	}

	r := s.buildRouter()

	addr := fmt.Sprintf(":%d", cfg.Port)
	return http.ListenAndServe(addr, r)
}
