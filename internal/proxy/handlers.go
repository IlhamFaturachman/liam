package proxy

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/liam-auto/liam/internal/db"
)

// --- Auth ---

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

		r.Header.Set("X-API-Key-ID", key.ID)
		next.ServeHTTP(w, r)
	})
}

// quietLogger skips logging for noisy polling endpoints.
func quietLogger(next http.Handler) http.Handler {
	logger := middleware.Logger(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

// --- Models ---

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
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

// --- Accounts ---

func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	provider := r.URL.Query().Get("provider")
	accounts, err := s.db.ListAccounts(provider)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
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
	if s.syncer != nil {
		s.syncer.DeleteAccountAsync(id)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// --- Keys ---

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

// --- Settings ---

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

// --- Sync ---

func (s *Server) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.syncer.Status())
}

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

// --- Provider resolution ---

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

// --- Helpers ---

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

// syncWorker runs bidirectional sync every 30 seconds.
func (s *Server) syncWorker() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if err := s.syncer.Sync(); err != nil {
			log.Printf("[SYNC] Error: %v", err)
		}
	}
}
