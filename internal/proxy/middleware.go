package proxy

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/liam-auto/liam/internal/config"
	"github.com/liam-auto/liam/internal/dashboard"
	"github.com/liam-auto/liam/internal/db"
	"github.com/liam-auto/liam/internal/providers/antigravity"
	"github.com/liam-auto/liam/internal/providers/kiro"
)

// RefreshIfNeeded refreshes AG token if expired (inline, before request).
// Also ensures the account is onboarded (has a projectId) — new accounts
// imported via harvest may not have one yet.
func RefreshIfNeeded(cfg *config.Config, database *db.Database, account *db.Account) error {
	var creds db.AGCredentials
	if err := json.Unmarshal(account.Credentials, &creds); err != nil {
		return err
	}
	if antigravity.IsTokenExpired(&creds, cfg.RefreshLeadMin) {
		newCreds, err := antigravity.RefreshToken(cfg, account)
		if err != nil {
			return err
		}
		credsJSON, _ := json.Marshal(newCreds)
		database.UpdateAccountCredentials(account.ID, credsJSON)
		account.Credentials = credsJSON
	}

	// Ensure onboarded (has projectId) — best-effort, don't fail the request
	if onboardErr := antigravity.EnsureOnboarded(cfg, database, account); onboardErr != nil {
		// Log but don't block — the executor can still work with a random projectId
		_ = onboardErr
	}
	return nil
}

// RefreshKiroIfNeeded refreshes Kiro token if expired (inline, before request)
func RefreshKiroIfNeeded(cfg *config.Config, database *db.Database, account *db.Account) error {
	var creds kiro.KiroCredentials
	if err := json.Unmarshal(account.Credentials, &creds); err != nil {
		return err
	}
	if !kiro.IsTokenExpired(&creds, cfg.RefreshLeadMin) {
		return nil
	}
	newCreds, err := kiro.RefreshToken(account)
	if err != nil {
		return err
	}
	credsJSON, _ := json.Marshal(newCreds)
	database.UpdateAccountCredentials(account.ID, credsJSON)
	account.Credentials = credsJSON
	return nil
}

// apiAuthMiddleware protects /api routes — allows localhost OR valid dashboard token
func apiAuthMiddleware(database *db.Database) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Allow localhost without auth
			if isLocalhost(r.RemoteAddr) {
				next.ServeHTTP(w, r)
				return
			}

			// Remote access requires dashboard token
			token := r.Header.Get("X-Dashboard-Token")
			if token == "" {
				token = r.URL.Query().Get("token")
			}
			if token == "" {
				// Also check Authorization header (Bearer token)
				auth := r.Header.Get("Authorization")
				if strings.HasPrefix(auth, "Bearer liam_") {
					token = strings.TrimPrefix(auth, "Bearer ")
				}
			}

			if token == "" {
				writeJSON(w, 401, map[string]interface{}{
					"error": map[string]string{"message": "Unauthorized — provide X-Dashboard-Token header or access from localhost", "type": "auth_error"},
				})
				return
			}

			// Validate token
			storedPassword := database.GetSetting("dashboard_password", "123456")
			if !dashboard.ValidateDashboardToken(token, storedPassword) {
				writeJSON(w, 401, map[string]interface{}{
					"error": map[string]string{"message": "Invalid or expired token", "type": "auth_error"},
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// isLocalhost checks if the request comes from localhost
func isLocalhost(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}
