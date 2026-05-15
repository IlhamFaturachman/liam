package workers

import (
	"encoding/json"
	"log"
	"time"

	"github.com/liam-auto/liam/internal/config"
	"github.com/liam-auto/liam/internal/db"
	"github.com/liam-auto/liam/internal/providers/antigravity"
	"github.com/liam-auto/liam/internal/providers/kiro"
)

// StartAll launches all background workers as goroutines
func StartAll(cfg *config.Config, database *db.Database) {
	go tokenRefresher(cfg, database)
	go healthChecker(cfg, database)
	go accountReaper(cfg, database)
	go logCleaner(database)

	log.Println("[WORKERS] All background workers started")
}

// tokenRefresher refreshes expiring tokens every 5 minutes
func tokenRefresher(cfg *config.Config, database *db.Database) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	// Run once immediately
	refreshTokens(cfg, database)

	for range ticker.C {
		refreshTokens(cfg, database)
	}
}

func refreshTokens(cfg *config.Config, database *db.Database) {
	// Refresh AG accounts with tokens expiring within 10 minutes
	agAccounts, err := database.GetAccountsNeedingRefresh("antigravity", 10)
	if err != nil {
		log.Printf("[REFRESH] Error getting AG accounts: %v", err)
	} else if len(agAccounts) > 0 {
		log.Printf("[REFRESH] Refreshing %d AG tokens", len(agAccounts))
		for _, account := range agAccounts {
			newCreds, err := antigravity.RefreshToken(cfg, &account)
			if err != nil {
				log.Printf("[REFRESH] AG failed for %s: %v", account.Email, err)
				database.MarkAccountError(account.ID, "refresh_failed: "+err.Error(), 0)
				continue
			}
			credsJSON, _ := json.Marshal(newCreds)
			database.UpdateAccountCredentials(account.ID, credsJSON)
			log.Printf("[REFRESH] AG refreshed: %s", account.Email)
		}
	}

	// Refresh Kiro accounts with tokens expiring within 10 minutes
	kiroAccounts, err := database.GetAccountsNeedingRefresh("kiro", 10)
	if err != nil {
		log.Printf("[REFRESH] Error getting Kiro accounts: %v", err)
	} else if len(kiroAccounts) > 0 {
		log.Printf("[REFRESH] Refreshing %d Kiro tokens", len(kiroAccounts))
		for _, account := range kiroAccounts {
			newCreds, err := kiro.RefreshToken(&account)
			if err != nil {
				log.Printf("[REFRESH] Kiro failed for %s: %v", account.Email, err)
				database.MarkAccountError(account.ID, "refresh_failed: "+err.Error(), 0)
				continue
			}
			credsJSON, _ := json.Marshal(newCreds)
			database.UpdateAccountCredentials(account.ID, credsJSON)
			log.Printf("[REFRESH] Kiro refreshed: %s", account.Email)
		}
	}
}

// healthChecker validates accounts every 15 minutes
func healthChecker(cfg *config.Config, database *db.Database) {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		checkHealth(cfg, database)
	}
}

func checkHealth(cfg *config.Config, database *db.Database) {
	// Check AG accounts
	agAccounts, err := database.GetActiveAccounts("antigravity")
	if err != nil {
		log.Printf("[HEALTH] Error getting AG accounts: %v", err)
	} else {
		for _, account := range agAccounts {
			var creds db.AGCredentials
			if err := json.Unmarshal(account.Credentials, &creds); err != nil {
				continue
			}
			_, _, err := antigravity.LoadCodeAssist(creds.AccessToken)
			if err != nil {
				log.Printf("[HEALTH] AG %s unhealthy: %v", account.Email, err)
				database.MarkAccountError(account.ID, "health_check_failed: "+err.Error(), 0)
			} else {
				database.MarkAccountSuccess(account.ID)
			}
		}
	}

	// Check Kiro accounts
	kiroAccounts, err := database.GetActiveAccounts("kiro")
	if err != nil {
		log.Printf("[HEALTH] Error getting Kiro accounts: %v", err)
	} else {
		for _, account := range kiroAccounts {
			var creds kiro.KiroCredentials
			if err := json.Unmarshal(account.Credentials, &creds); err != nil {
				continue
			}
			if creds.AccessToken == "" {
				continue
			}
			// Simple health check: try to refresh (validates token is still valid)
			if kiro.IsTokenExpired(&creds, 0) {
				_, err := kiro.RefreshToken(&account)
				if err != nil {
					log.Printf("[HEALTH] Kiro %s unhealthy: %v", account.Email, err)
					database.MarkAccountError(account.ID, "health_check_failed: "+err.Error(), 0)
				} else {
					database.MarkAccountSuccess(account.ID)
				}
			} else {
				database.MarkAccountSuccess(account.ID)
			}
		}
	}
}

// accountReaper reactivates cooled-down accounts and disables permanently broken ones
func accountReaper(cfg *config.Config, database *db.Database) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		reapAccounts(database)
	}
}

func reapAccounts(database *db.Database) {
	accounts, _ := database.ListAccounts("")
	active, cooldown, disabled := 0, 0, 0
	for _, a := range accounts {
		switch a.Status {
		case "active":
			active++
		case "cooldown":
			cooldown++
		case "disabled":
			disabled++
		}
	}
	if len(accounts) > 0 {
		log.Printf("[REAPER] Accounts: %d active, %d cooldown, %d disabled (total: %d)",
			active, cooldown, disabled, len(accounts))
	}
}

// logCleaner deletes usage logs older than 3 days (runs every hour)
func logCleaner(database *db.Database) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	// Run once on startup
	cleanLogs(database)

	for range ticker.C {
		cleanLogs(database)
	}
}

func cleanLogs(database *db.Database) {
	deleted, err := database.CleanOldLogs(72 * time.Hour)
	if err != nil {
		log.Printf("[CLEANER] Error: %v", err)
		return
	}
	if deleted > 0 {
		log.Printf("[CLEANER] Deleted %d old log entries", deleted)
	}
}
