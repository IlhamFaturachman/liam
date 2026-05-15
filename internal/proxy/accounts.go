package proxy

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
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

	writeJSON(w, 201, map[string]interface{}{
		"success":    true,
		"email":      req.Email,
		"project_id": req.ProjectID,
	})
}

// handleImportKiro imports a Kiro account from a refresh token
func (s *Server) handleImportKiro(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
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
	email := extractEmailFromJWT(newCreds.AccessToken)
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

	account := &db.Account{
		Provider:    "kiro",
		Email:       email,
		Status:      "active",
		Credentials: finalCredsJSON,
	}

	if err := s.db.UpsertAccount(account); err != nil {
		writeError(w, 500, fmt.Sprintf("Failed to save: %v", err))
		return
	}

	writeJSON(w, 201, map[string]interface{}{
		"success": true,
		"email":   email,
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

		// Call loadCodeAssist to get quota info
		// For now, just verify token works (quota tracking from response)
		_, _, err := antigravity.LoadCodeAssist(creds.AccessToken)
		if err != nil {
			writeError(w, 502, fmt.Sprintf("Failed to fetch quota: %v", err))
			return
		}

		// TODO: Parse actual quota from response when AG exposes it
		writeJSON(w, 200, map[string]interface{}{
			"status":  "refreshed",
			"message": "Token validated successfully",
		})
	} else {
		writeError(w, 400, "quota refresh not supported for this provider")
	}
}

// handleGetAccountLocks returns active model locks for an account
func (s *Server) handleGetAccountLocks(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, 400, "missing id")
		return
	}

	locks := s.db.GetModelLocks(id)
	result := []map[string]interface{}{}
	now := time.Now().UTC()
	for model, until := range locks {
		remaining := until.Sub(now)
		if remaining > 0 {
			result = append(result, map[string]interface{}{
				"model":     model,
				"until":     until.Format(time.RFC3339),
				"remaining": int(remaining.Seconds()),
			})
		}
	}
	writeJSON(w, 200, result)
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

	writeJSON(w, 200, map[string]string{"status": "saved"})
}

// --- Helpers ---

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
