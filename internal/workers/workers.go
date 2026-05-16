package workers

import (
	"encoding/json"
	"log"
	"math/rand"
	"sync"
	"time"

	"github.com/liam-auto/liam/internal/config"
	"github.com/liam-auto/liam/internal/db"
	"github.com/liam-auto/liam/internal/providers/antigravity"
	"github.com/liam-auto/liam/internal/providers/kiro"
)

// Tunables for the refresh worker. Centralised so the values are obvious
// when troubleshooting "why is account X dead".
const (
	// refreshTickInterval is how often the worker scans for soon-to-expire
	// tokens. With 5 min ticks and a 10 min lead time every account gets at
	// least one refresh chance before its access_token actually expires.
	refreshTickInterval = 5 * time.Minute

	// refreshLeadMinutes: select accounts whose access_token expires within
	// this many minutes. Must be >= refreshTickInterval (in minutes) plus a
	// margin so we never hit a tick where the token already expired.
	refreshLeadMinutes = 10

	// refreshBatchConcurrency caps how many AG accounts we refresh in
	// parallel. With 300 accounts under one outbound IP, hammering Google
	// with 300 simultaneous POSTs to oauth2.googleapis.com is the single
	// fastest way to get the IP rate-limited or the token family revoked
	// for "unusual activity". 8 mirrors what 9router does for the same
	// upstream and stays well under Google's per-IP soft cap.
	refreshBatchConcurrency = 8

	// refreshJitterMaxMs: spread requests in a batch over this window so
	// they don't arrive at Google as one synchronous burst at HH:00:00.
	// 30s is enough to look organic without delaying refresh meaningfully
	// (we still finish way before the 10 min lead time elapses).
	refreshJitterMaxMs = 30_000
)

// StartAll launches all background workers as goroutines
func StartAll(cfg *config.Config, database *db.Database) {
	go tokenRefresher(cfg, database)
	go healthChecker(cfg, database)
	go accountReaper(cfg, database)
	go logCleaner(database)

	log.Println("[WORKERS] All background workers started")
}

// tokenRefresher refreshes expiring tokens at a regular cadence.
func tokenRefresher(cfg *config.Config, database *db.Database) {
	ticker := time.NewTicker(refreshTickInterval)
	defer ticker.Stop()

	// Run once immediately so newly imported accounts don't have to wait
	// 5 minutes for their first refresh.
	refreshTokens(cfg, database)

	for range ticker.C {
		refreshTokens(cfg, database)
	}
}

func refreshTokens(cfg *config.Config, database *db.Database) {
	// AG and Kiro use distinct refresh paths (Google OAuth vs AWS Cognito-ish)
	// but the per-batch concurrency + jitter shape is identical, so we
	// drive them through the same scaffolding.

	if agAccounts, err := database.GetAccountsNeedingRefresh("antigravity", refreshLeadMinutes); err != nil {
		log.Printf("[REFRESH] Error getting AG accounts: %v", err)
	} else if len(agAccounts) > 0 {
		log.Printf("[REFRESH] AG: %d account(s) need refresh", len(agAccounts))
		runRefreshBatch(agAccounts, func(account db.Account) error {
			newCreds, err := antigravity.RefreshToken(cfg, &account)
			if err != nil {
				return err
			}
			credsJSON, _ := json.Marshal(newCreds)
			return database.UpdateAccountCredentials(account.ID, credsJSON)
		}, func(account db.Account, err error) {
			handleRefreshFailure(database, account, err, "AG", isAGUnrecoverable)
		}, "AG")
	}

	if kiroAccounts, err := database.GetAccountsNeedingRefresh("kiro", refreshLeadMinutes); err != nil {
		log.Printf("[REFRESH] Error getting Kiro accounts: %v", err)
	} else if len(kiroAccounts) > 0 {
		log.Printf("[REFRESH] Kiro: %d account(s) need refresh", len(kiroAccounts))
		runRefreshBatch(kiroAccounts, func(account db.Account) error {
			newCreds, err := kiro.RefreshToken(&account)
			if err != nil {
				return err
			}
			credsJSON, _ := json.Marshal(newCreds)
			return database.UpdateAccountCredentials(account.ID, credsJSON)
		}, func(account db.Account, err error) {
			handleRefreshFailure(database, account, err, "Kiro", isKiroUnrecoverable)
		}, "Kiro")
	}
}

// runRefreshBatch executes `do` for each account with bounded concurrency
// and per-account jitter so a 300-account batch doesn't fan out into 300
// simultaneous OAuth requests from the same IP.
func runRefreshBatch(
	accounts []db.Account,
	do func(db.Account) error,
	onErr func(db.Account, error),
	label string,
) {
	sem := make(chan struct{}, refreshBatchConcurrency)
	var wg sync.WaitGroup
	successCount := 0
	var mu sync.Mutex

	for _, account := range accounts {
		wg.Add(1)
		sem <- struct{}{}
		go func(account db.Account) {
			defer wg.Done()
			defer func() { <-sem }()

			// Per-account jitter. rand.Intn is fine here; this is anti-burst
			// shaping, not security. The sleep happens INSIDE the worker
			// pool so it doesn't block submission of the next account —
			// other workers are free to start immediately.
			if refreshJitterMaxMs > 0 {
				time.Sleep(time.Duration(rand.Intn(refreshJitterMaxMs)) * time.Millisecond)
			}

			if err := do(account); err != nil {
				onErr(account, err)
				return
			}
			mu.Lock()
			successCount++
			mu.Unlock()
			log.Printf("[REFRESH] %s OK: %s", label, account.Email)
		}(account)
	}

	wg.Wait()
	log.Printf("[REFRESH] %s batch done: %d/%d succeeded", label, successCount, len(accounts))
}

// handleRefreshFailure decides whether to keep the account active or
// quarantine it permanently based on the structured error from the
// provider's RefreshToken().
//
// Why quarantine: once Google says `invalid_grant` the refresh_token is dead
// — every retry from the same outbound IP is one more strike against our
// reputation. Disabling the row stops the worker from picking it up next
// tick (GetAccountsNeedingRefresh filters on status='active').
func handleRefreshFailure(
	database *db.Database,
	account db.Account,
	err error,
	label string,
	isUnrecoverable func(error) bool,
) {
	if isUnrecoverable(err) {
		log.Printf("[REFRESH] %s DEAD %s: %v — quarantining", label, account.Email, err)
		// Mark the account disabled so subsequent worker ticks skip it,
		// and record the failure reason in last_error for the dashboard.
		_ = database.SetAccountStatus(account.ID, "disabled", "refresh_unrecoverable: "+err.Error())
		return
	}
	// Transient — leave status active, just record the latest error.
	// Worker picks the row up again on the next tick.
	log.Printf("[REFRESH] %s transient fail %s: %v", label, account.Email, err)
	_ = database.MarkAccountError(account.ID, "refresh_failed: "+err.Error(), 0)
}

func isAGUnrecoverable(err error) bool {
	if re := antigravity.AsRefreshError(err); re != nil {
		return re.IsUnrecoverable()
	}
	return false
}

func isKiroUnrecoverable(err error) bool {
	if re := kiro.AsRefreshError(err); re != nil {
		return re.IsUnrecoverable()
	}
	return false
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
			// Try once with the cached access token. Most accounts pass here
			// because the refresh worker already topped them up.
			//
			// If we hit a 401/403 we DON'T fail the account immediately —
			// the access token may simply have expired between refresher
			// ticks (the refresh window is "expires_at - 10 min"; a token
			// that died at minute 11 would still be presented here).
			// We attempt one refresh + retry; only if THAT fails do we
			// record an error. Mirrors the on-demand `handleRefreshQuota`
			// flow so manual and automated paths agree.
			_, _, hcErr := antigravity.LoadCodeAssist(creds.AccessToken)
			if hcErr != nil && creds.RefreshToken != "" {
				if refreshed, rErr := antigravity.RefreshToken(cfg, &account); rErr == nil {
					credsJSON, _ := json.Marshal(refreshed)
					if uErr := database.UpdateAccountCredentials(account.ID, credsJSON); uErr != nil {
						log.Printf("[HEALTH] AG persist refreshed creds %s: %v", account.Email, uErr)
					}
					account.Credentials = credsJSON
					_, _, hcErr = antigravity.LoadCodeAssist(refreshed.AccessToken)
				} else if isAGUnrecoverable(rErr) {
					// Refresh-token revoked. Quarantine instead of leaving
					// the account active and re-failing every 15 minutes.
					log.Printf("[HEALTH] AG DEAD %s: %v — quarantining", account.Email, rErr)
					_ = database.SetAccountStatus(account.ID, "disabled", "health_refresh_unrecoverable: "+rErr.Error())
					continue
				} else {
					// Transient refresh failure (network, 5xx). Treat the
					// whole healthcheck as transient too.
					hcErr = rErr
				}
			}
			if hcErr != nil {
				log.Printf("[HEALTH] AG %s unhealthy: %v", account.Email, hcErr)
				_ = database.MarkAccountError(account.ID, "health_check_failed: "+hcErr.Error(), 0)
			} else {
				_ = database.MarkAccountSuccess(account.ID)
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
