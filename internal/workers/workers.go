package workers

import (
	"encoding/json"
	"log"
	"time"

	"github.com/liam-auto/liam/internal/config"
	"github.com/liam-auto/liam/internal/db"
	"github.com/liam-auto/liam/internal/providers/antigravity"
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
	// Get AG accounts with tokens expiring within 10 minutes
	accounts, err := database.GetAccountsNeedingRefresh("antigravity", 10)
	if err != nil {
		log.Printf("[REFRESH] Error getting accounts: %v", err)
		return
	}

	if len(accounts) == 0 {
		return
	}

	log.Printf("[REFRESH] Refreshing %d expiring tokens", len(accounts))

	for _, account := range accounts {
		newCreds, err := antigravity.RefreshToken(cfg, &account)
		if err != nil {
			log.Printf("[REFRESH] Failed for %s: %v", account.Email, err)
			database.MarkAccountError(account.ID, "refresh_failed: "+err.Error(), 0)
			continue
		}

		// Save updated credentials
		credsJSON, _ := json.Marshal(newCreds)
		if err := database.UpdateAccountCredentials(account.ID, credsJSON); err != nil {
			log.Printf("[REFRESH] Failed to save creds for %s: %v", account.Email, err)
			continue
		}

		log.Printf("[REFRESH] Refreshed token for %s (expires: %s)", account.Email, newCreds.ExpiresAt)
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
	accounts, err := database.GetActiveAccounts("antigravity")
	if err != nil {
		log.Printf("[HEALTH] Error getting accounts: %v", err)
		return
	}

	for _, account := range accounts {
		var creds db.AGCredentials
		if err := json.Unmarshal(account.Credentials, &creds); err != nil {
			continue
		}

		// Quick health check: try loadCodeAssist
		_, _, err := antigravity.LoadCodeAssist(creds.AccessToken)
		if err != nil {
			log.Printf("[HEALTH] Account %s unhealthy: %v", account.Email, err)
			database.MarkAccountError(account.ID, "health_check_failed: "+err.Error(), 0)
		} else {
			database.MarkAccountSuccess(account.ID)
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
