package proxy

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/liam-auto/liam/internal/db"
	"github.com/liam-auto/liam/internal/providers/antigravity"
	"github.com/liam-auto/liam/internal/providers/kiro"
)

// handleImportAG imports an AG account from pasted credentials
func (s *Server) handleImportAG(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email        string `json:"email"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ProjectID    string `json:"project_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if req.RefreshToken == "" {
		writeError(w, 400, "refresh_token is required")
		return
	}
	if req.Email == "" {
		req.Email = "imported-" + uuid.New().String()[:8] + "@manual"
	}

	// If no access_token, try to refresh
	if req.AccessToken == "" && req.RefreshToken != "" {
		// Build temp account to refresh
		tempCreds := &db.AGCredentials{
			RefreshToken: req.RefreshToken,
			ProjectID:    req.ProjectID,
		}
		credsJSON, _ := json.Marshal(tempCreds)
		tempAccount := &db.Account{Credentials: credsJSON}

		newCreds, err := antigravity.RefreshToken(s.cfg, tempAccount)
		if err != nil {
			writeError(w, 400, fmt.Sprintf("Failed to refresh token: %v", err))
			return
		}
		req.AccessToken = newCreds.AccessToken
		if newCreds.ProjectID != "" {
			req.ProjectID = newCreds.ProjectID
		}
	}

	// If no project_id, try to fetch
	if req.ProjectID == "" && req.AccessToken != "" {
		projectID, _, err := antigravity.LoadCodeAssist(req.AccessToken)
		if err == nil {
			req.ProjectID = projectID
		}
	}

	// Build credentials
	creds := db.AGCredentials{
		AccessToken:  req.AccessToken,
		RefreshToken: req.RefreshToken,
		ExpiresAt:    time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339),
		ProjectID:    req.ProjectID,
		TierID:       "legacy-tier",
	}
	credsJSON, _ := json.Marshal(creds)

	account := &db.Account{
		Provider:    "antigravity",
		Email:       req.Email,
		Status:      "active",
		Credentials: credsJSON,
	}

	if err := s.db.UpsertAccount(account); err != nil {
		writeError(w, 500, fmt.Sprintf("Failed to save: %v", err))
		return
	}
	if s.syncer != nil {
		s.syncer.PushAccountAsync(account)
	}

	writeJSON(w, 201, map[string]interface{}{
		"success":    true,
		"email":      req.Email,
		"project_id": req.ProjectID,
	})
}

// handleImportKiro imports a Kiro account from a refresh token. When
// account_id is provided the existing account row is updated in place — this
// is what the dashboard "Re-import" button uses so corrupted accounts (e.g.
// pulled from sync without credentials) are healed instead of duplicated.
func (s *Server) handleImportKiro(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if req.RefreshToken == "" {
		writeError(w, 400, "refresh_token is required")
		return
	}

	// Validate format
	if !strings.HasPrefix(req.RefreshToken, "aorAAAAAG") {
		writeError(w, 400, "Invalid Kiro refresh token format (must start with 'aorAAAAAG')")
		return
	}

	// Call Kiro refresh endpoint to validate + get access token
	tempCreds := kiro.KiroCredentials{
		RefreshToken: req.RefreshToken,
		Region:       "us-east-1",
	}
	credsJSON, _ := json.Marshal(tempCreds)
	tempAccount := &db.Account{
		ID:          uuid.New().String(),
		Credentials: credsJSON,
	}

	newCreds, err := kiro.RefreshToken(tempAccount)
	if err != nil {
		writeError(w, 400, fmt.Sprintf("Failed to validate token: %v", err))
		return
	}

	// Extract email from JWT (access token)
	jwtEmail := extractEmailFromJWT(newCreds.AccessToken)

	// Resolve target account: explicit account_id wins over email-based
	// upsert so the user can deliberately heal a specific row.
	var targetAccount *db.Account
	if req.AccountID != "" {
		existing, lookupErr := s.findAccountByID(req.AccountID)
		if lookupErr == nil && existing != nil && existing.Provider == "kiro" {
			targetAccount = existing
		}
	}

	email := jwtEmail
	if targetAccount != nil {
		// Preserve the original email so dashboards/aliases keep working.
		// If the row had a placeholder ("kiro-…@imported") and the JWT
		// gave us a real address, prefer the real one.
		if strings.HasSuffix(targetAccount.Email, "@imported") && jwtEmail != "" {
			email = jwtEmail
		} else {
			email = targetAccount.Email
		}
	}
	if email == "" {
		email = "kiro-" + uuid.New().String()[:8] + "@imported"
	}

	// Save account
	finalCreds := db.KiroCredentials{
		AccessToken:  newCreds.AccessToken,
		RefreshToken: newCreds.RefreshToken,
		ExpiresAt:    newCreds.ExpiresAt,
		Region:       "us-east-1",
		ProfileARN:   newCreds.ProfileARN,
	}
	finalCredsJSON, _ := json.Marshal(finalCreds)

	// Fetch quota from Kiro API. The default profile ARN is used internally
	// when newCreds.ProfileARN is empty (matches 9router behaviour).
	var quotaTotal, quotaRemaining int
	var quotaResetAt, plan string
	var breakdownJSON json.RawMessage
	if qr, qErr := kiro.FetchQuota(newCreds.AccessToken, newCreds.ProfileARN); qErr == nil && qr != nil {
		quotaTotal = qr.Total
		quotaRemaining = qr.Total - qr.Used
		quotaResetAt = qr.ResetAt
		plan = qr.Plan
		if len(qr.Breakdown) > 0 {
			if b, mErr := json.Marshal(qr.Breakdown); mErr == nil {
				breakdownJSON = b
			}
		}
	}

	account := &db.Account{
		Provider:       "kiro",
		Email:          email,
		Status:         "active",
		Credentials:    finalCredsJSON,
		QuotaTotal:     quotaTotal,
		QuotaRemaining: quotaRemaining,
		Plan:           plan,
		AuthMethod:     "imported",
		QuotaBreakdown: breakdownJSON,
	}
	if quotaResetAt != "" {
		t, _ := time.Parse(time.RFC3339, quotaResetAt)
		account.QuotaResetAt = &t
	}
	if targetAccount != nil {
		// Reuse the existing primary key so SSE feeds, usage logs, and
		// model locks linked to this row stay valid.
		account.ID = targetAccount.ID
	}

	if err := s.db.UpsertAccount(account); err != nil {
		writeError(w, 500, fmt.Sprintf("Failed to save: %v", err))
		return
	}
	if s.syncer != nil {
		s.syncer.PushAccountAsync(account)
	}

	writeJSON(w, 201, map[string]interface{}{
		"success":  true,
		"email":    email,
		"id":       account.ID,
		"replaced": targetAccount != nil,
	})
}

// handleRefreshQuota refreshes quota info for an account (on-demand)
func (s *Server) handleRefreshQuota(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, 400, "missing id")
		return
	}

	// Get account
	accounts, _ := s.db.ListAccounts("")
	var account *db.Account
	for i := range accounts {
		if accounts[i].ID == id {
			account = &accounts[i]
			break
		}
	}
	if account == nil {
		writeError(w, 404, "account not found")
		return
	}

	if account.Provider == "antigravity" {
		var creds db.AGCredentials
		json.Unmarshal(account.Credentials, &creds)

		if creds.AccessToken == "" {
			writeError(w, 400, "no access token")
			return
		}

		_, _, err := antigravity.LoadCodeAssist(creds.AccessToken)
		if err != nil {
			writeError(w, 502, fmt.Sprintf("Failed to fetch quota: %v", err))
			return
		}

		writeJSON(w, 200, map[string]interface{}{
			"status":  "refreshed",
			"message": "Token validated successfully",
		})
	} else if account.Provider == "kiro" {
		var creds db.KiroCredentials
		if err := json.Unmarshal(account.Credentials, &creds); err != nil {
			writeError(w, 500, fmt.Sprintf("parse credentials: %v", err))
			return
		}

		if creds.AccessToken == "" && creds.RefreshToken == "" {
			writeError(w, 400, "no access or refresh token")
			return
		}

		// Try once with the current access token. If the upstream rejects
		// it as expired/forbidden, refresh first and retry once.
		qr, qErr := kiro.FetchQuota(creds.AccessToken, creds.ProfileARN)

		var quotaErr *kiro.QuotaError
		if qErr != nil && errors.As(qErr, &quotaErr) && quotaErr.Reason == kiro.QuotaErrorAuthExpired && creds.RefreshToken != "" {
			// Force a refresh, persist the new creds, retry once.
			refreshed, rErr := kiro.RefreshToken(account)
			if rErr != nil {
				writeError(w, 502, fmt.Sprintf("token refresh failed: %v", rErr))
				return
			}
			newCredsJSON, _ := json.Marshal(refreshed)
			if err := s.db.UpdateAccountCredentials(account.ID, newCredsJSON); err != nil {
				log.Printf("[REFRESH-QUOTA] persist refreshed creds: %v", err)
			}
			account.Credentials = newCredsJSON
			// kiro.KiroCredentials and db.KiroCredentials share the
			// same JSON shape — copy field-by-field to avoid the
			// type mismatch and keep ProfileARN/Region/etc intact.
			creds = db.KiroCredentials{
				AccessToken:  refreshed.AccessToken,
				RefreshToken: refreshed.RefreshToken,
				ExpiresAt:    refreshed.ExpiresAt,
				ClientID:     refreshed.ClientID,
				ClientSecret: refreshed.ClientSecret,
				Region:       refreshed.Region,
				ProfileARN:   refreshed.ProfileARN,
			}
			qr, qErr = kiro.FetchQuota(creds.AccessToken, creds.ProfileARN)
		}

		if qErr != nil {
			writeError(w, 502, fmt.Sprintf("Failed to fetch quota: %v", qErr))
			return
		}
		if qr == nil {
			writeError(w, 502, "quota response was empty")
			return
		}

		// Persist quota + plan + breakdown to DB so the dashboard
		// reflects the latest state without another roundtrip.
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
		if err := s.db.UpsertAccount(account); err != nil {
			log.Printf("[REFRESH-QUOTA] persist account: %v", err)
		}
		if s.syncer != nil {
			s.syncer.PushAccountAsync(account)
		}

		writeJSON(w, 200, map[string]interface{}{
			"status":    "refreshed",
			"used":      qr.Used,
			"total":     qr.Total,
			"remaining": qr.Total - qr.Used,
			"reset_at":  qr.ResetAt,
			"plan":      qr.Plan,
			"breakdown": qr.Breakdown,
		})
	} else {
		writeError(w, 400, "quota refresh not supported for this provider")
	}
}

// handleSetExcludedModels sets excluded model patterns for an account
func (s *Server) handleSetExcludedModels(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, 400, "missing id")
		return
	}

	var req struct {
		Patterns []string `json:"patterns"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}

	// Get current account
	accounts, _ := s.db.ListAccounts("")
	var account *db.Account
	for i := range accounts {
		if accounts[i].ID == id {
			account = &accounts[i]
			break
		}
	}
	if account == nil {
		writeError(w, 404, "account not found")
		return
	}

	// Update metadata with excluded_models
	var meta map[string]interface{}
	if account.Metadata != nil {
		json.Unmarshal(account.Metadata, &meta)
	}
	if meta == nil {
		meta = map[string]interface{}{}
	}
	meta["excluded_models"] = req.Patterns

	metaJSON, _ := json.Marshal(meta)
	account.Metadata = metaJSON
	if err := s.db.UpsertAccount(account); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if s.syncer != nil {
		s.syncer.PushAccountAsync(account)
	}

	writeJSON(w, 200, map[string]string{"status": "saved"})
}

// --- Helpers ---

// findAccountByID returns the account matching the given ID, or nil if not
// found. ListAccounts is used to keep credential decoding consistent (it
// fills TokenExpiresAt / HasCredentials).
func (s *Server) findAccountByID(id string) (*db.Account, error) {
	if id == "" {
		return nil, fmt.Errorf("missing id")
	}
	accounts, err := s.db.ListAccounts("")
	if err != nil {
		return nil, err
	}
	for i := range accounts {
		if accounts[i].ID == id {
			return &accounts[i], nil
		}
	}
	return nil, fmt.Errorf("account not found")
}

// extractEmailFromJWT extracts email from a JWT access token (best effort)
func extractEmailFromJWT(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	// Decode payload (base64url)
	payload := parts[1]
	// Add padding
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}
	// Replace URL-safe chars
	payload = strings.ReplaceAll(payload, "-", "+")
	payload = strings.ReplaceAll(payload, "_", "/")

	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return ""
	}

	var claims map[string]interface{}
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return ""
	}

	if email, ok := claims["email"].(string); ok {
		return email
	}
	if email, ok := claims["sub"].(string); ok && strings.Contains(email, "@") {
		return email
	}
	return ""
}

// Suppress unused imports

// handleAGAuthorize generates an OAuth URL for AG account addition
func (s *Server) handleAGAuthorize(w http.ResponseWriter, r *http.Request) {
	state := uuid.New().String()
	redirectURI := fmt.Sprintf("http://localhost:%d/callback", s.cfg.Port)

	scopes := strings.Join(s.cfg.AGScopes, " ")
	authURL := fmt.Sprintf(
		"https://accounts.google.com/o/oauth2/v2/auth?client_id=%s&response_type=code&redirect_uri=%s&scope=%s&state=%s&access_type=offline&prompt=consent",
		s.cfg.AGClientID,
		redirectURI,
		strings.ReplaceAll(scopes, " ", "+"),
		state,
	)

	writeJSON(w, 200, map[string]interface{}{
		"auth_url":     authURL,
		"state":        state,
		"redirect_uri": redirectURI,
	})
}

// handleAGExchange exchanges a callback URL for tokens and saves the account
func (s *Server) handleAGExchange(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CallbackURL string `json:"callback_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if req.CallbackURL == "" {
		writeError(w, 400, "callback_url required")
		return
	}

	// Parse code from callback URL
	callbackURL := req.CallbackURL
	// Handle case where user pastes just the query params
	if !strings.Contains(callbackURL, "?") {
		writeError(w, 400, "Invalid callback URL — must contain ?code=...")
		return
	}

	parts := strings.SplitN(callbackURL, "?", 2)
	params := map[string]string{}
	for _, pair := range strings.Split(parts[1], "&") {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) == 2 {
			params[kv[0]] = kv[1]
		}
	}

	code := params["code"]
	if code == "" {
		if errMsg := params["error"]; errMsg != "" {
			writeError(w, 400, fmt.Sprintf("OAuth error: %s", errMsg))
			return
		}
		writeError(w, 400, "No authorization code found in URL")
		return
	}

	redirectURI := fmt.Sprintf("http://localhost:%d/callback", s.cfg.Port)

	// Exchange code for tokens
	tokenResp, err := http.Post("https://oauth2.googleapis.com/token",
		"application/x-www-form-urlencoded",
		strings.NewReader(fmt.Sprintf(
			"grant_type=authorization_code&code=%s&client_id=%s&client_secret=%s&redirect_uri=%s",
			code, s.cfg.AGClientID, s.cfg.AGClientSecret, redirectURI,
		)))
	if err != nil {
		writeError(w, 502, fmt.Sprintf("Token exchange failed: %v", err))
		return
	}
	defer tokenResp.Body.Close()

	if tokenResp.StatusCode != 200 {
		body, _ := io.ReadAll(tokenResp.Body)
		writeError(w, 400, fmt.Sprintf("Token exchange error: %s", string(body[:min(len(body), 200)])))
		return
	}

	var tokens struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	json.NewDecoder(tokenResp.Body).Decode(&tokens)

	if tokens.AccessToken == "" {
		writeError(w, 400, "No access token in response")
		return
	}

	// Fetch email
	email := ""
	userResp, err := http.Get("https://www.googleapis.com/oauth2/v1/userinfo?alt=json&access_token=" + tokens.AccessToken)
	if err == nil && userResp.StatusCode == 200 {
		var userInfo struct {
			Email string `json:"email"`
		}
		json.NewDecoder(userResp.Body).Decode(&userInfo)
		userResp.Body.Close()
		email = userInfo.Email
	}
	if email == "" {
		email = "ag-" + uuid.New().String()[:8] + "@oauth"
	}

	// Fetch projectId
	projectID, _, _ := antigravity.LoadCodeAssist(tokens.AccessToken)
	if projectID == "" {
		projectID = "auto-" + uuid.New().String()[:5]
	}

	// Save account
	creds := db.AGCredentials{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresAt:    time.Now().UTC().Add(time.Duration(tokens.ExpiresIn) * time.Second).Format(time.RFC3339),
		ProjectID:    projectID,
		TierID:       "legacy-tier",
		Scope:        tokens.Scope,
	}
	credsJSON, _ := json.Marshal(creds)

	account := &db.Account{
		Provider:    "antigravity",
		Email:       email,
		Status:      "active",
		Credentials: credsJSON,
	}

	if err := s.db.UpsertAccount(account); err != nil {
		writeError(w, 500, fmt.Sprintf("Failed to save: %v", err))
		return
	}
	if s.syncer != nil {
		s.syncer.PushAccountAsync(account)
	}

	writeJSON(w, 201, map[string]interface{}{
		"success":    true,
		"email":      email,
		"project_id": projectID,
	})
}

// handleEditAccount updates account fields (name/email)
func (s *Server) handleEditAccount(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, 400, "missing id")
		return
	}

	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}

	if req.Email == "" {
		writeError(w, 400, "email/name required")
		return
	}

	// Direct UPDATE (not upsert — avoids UNIQUE constraint on provider+email)
	if err := s.db.UpdateAccountEmail(id, req.Email); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	// Mirror the rename to Supabase. We re-read the account so the row
	// pushed upstream reflects the new email + updated_at.
	if s.syncer != nil {
		if updated, err := s.findAccountByID(id); err == nil && updated != nil {
			s.syncer.PushAccountAsync(updated)
		}
	}

	writeJSON(w, 200, map[string]string{"status": "updated", "email": req.Email})
}

// handleTestAccount validates an account's credentials by checking token
// expiry and refreshing if needed. Adopted from 9router pattern
// (`testOAuthConnection` with `checkExpiry: true, refreshable: true`):
//
//  1. If access_token is empty -> invalid
//  2. If token expired and refresh_token available -> refresh, persist, return valid+refreshed
//  3. If token still valid -> return valid (provider-specific liveness probes
//     are intentionally skipped to avoid burning quota on noisy auth APIs)
//
// Provider-agnostic: works for both Antigravity and Kiro since both expose
// the same shape (access_token + refresh_token + expires_at).
func (s *Server) handleTestAccount(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, 400, "missing id")
		return
	}

	account, err := s.findAccountByID(id)
	if err != nil || account == nil {
		writeError(w, 404, "account not found")
		return
	}

	start := time.Now()
	result := map[string]interface{}{
		"valid":      false,
		"refreshed":  false,
		"latency_ms": 0,
		"error":      nil,
	}

	switch account.Provider {
	case "antigravity":
		var creds db.AGCredentials
		_ = json.Unmarshal(account.Credentials, &creds)
		if creds.AccessToken == "" && creds.RefreshToken == "" {
			result["error"] = "No access or refresh token. Please re-import the account."
			result["latency_ms"] = int(time.Since(start).Milliseconds())
			writeJSON(w, 200, result)
			return
		}

		expired := antigravity.IsTokenExpired(&creds, 5)
		if expired && creds.RefreshToken != "" {
			refreshed, rErr := antigravity.RefreshToken(s.cfg, account)
			if rErr != nil {
				result["error"] = "Token expired and refresh failed: " + rErr.Error()
				result["latency_ms"] = int(time.Since(start).Milliseconds())
				writeJSON(w, 200, result)
				return
			}
			credsJSON, _ := json.Marshal(refreshed)
			_ = s.db.UpdateAccountCredentials(account.ID, credsJSON)
			account.Credentials = credsJSON
			if s.syncer != nil {
				s.syncer.PushAccountAsync(account)
			}
			result["refreshed"] = true
			result["valid"] = true
		} else if expired {
			result["error"] = "Token expired (no refresh token)"
		} else {
			result["valid"] = true
		}

	case "kiro":
		var creds db.KiroCredentials
		_ = json.Unmarshal(account.Credentials, &creds)
		if creds.AccessToken == "" && creds.RefreshToken == "" {
			result["error"] = "No access or refresh token. Please re-import the account."
			result["latency_ms"] = int(time.Since(start).Milliseconds())
			writeJSON(w, 200, result)
			return
		}

		// kiro.IsTokenExpired expects *kiro.KiroCredentials, but the DB
		// version has the same JSON shape — copy what we need.
		kCreds := &kiro.KiroCredentials{
			AccessToken:  creds.AccessToken,
			RefreshToken: creds.RefreshToken,
			ExpiresAt:    creds.ExpiresAt,
			ProfileARN:   creds.ProfileARN,
			Region:       creds.Region,
		}
		expired := kiro.IsTokenExpired(kCreds, 5)

		if expired && creds.RefreshToken != "" {
			refreshed, rErr := kiro.RefreshToken(account)
			if rErr != nil {
				result["error"] = "Token expired and refresh failed: " + rErr.Error()
				result["latency_ms"] = int(time.Since(start).Milliseconds())
				writeJSON(w, 200, result)
				return
			}
			persisted := db.KiroCredentials{
				AccessToken:  refreshed.AccessToken,
				RefreshToken: refreshed.RefreshToken,
				ExpiresAt:    refreshed.ExpiresAt,
				ClientID:     refreshed.ClientID,
				ClientSecret: refreshed.ClientSecret,
				Region:       refreshed.Region,
				ProfileARN:   refreshed.ProfileARN,
			}
			credsJSON, _ := json.Marshal(persisted)
			_ = s.db.UpdateAccountCredentials(account.ID, credsJSON)
			account.Credentials = credsJSON
			if s.syncer != nil {
				s.syncer.PushAccountAsync(account)
			}
			result["refreshed"] = true
			result["valid"] = true
		} else if expired {
			result["error"] = "Token expired (no refresh token)"
		} else {
			result["valid"] = true
		}

	default:
		result["error"] = "Provider test not supported: " + account.Provider
	}

	result["latency_ms"] = int(time.Since(start).Milliseconds())
	writeJSON(w, 200, result)
}
