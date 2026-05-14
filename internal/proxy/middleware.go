package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/liam-auto/liam/internal/config"
	"github.com/liam-auto/liam/internal/db"
	"github.com/liam-auto/liam/internal/providers/antigravity"
	"github.com/liam-auto/liam/internal/providers/kiro"
)

// TokenRefreshMiddleware checks if account token needs refresh before use
// Called inline during request processing (not background worker)
func RefreshIfNeeded(cfg *config.Config, database *db.Database, account *db.Account) error {
	var creds db.AGCredentials
	if err := json.Unmarshal(account.Credentials, &creds); err != nil {
		return fmt.Errorf("parse credentials: %w", err)
	}

	// Check if token is expired or expiring within lead time
	if !antigravity.IsTokenExpired(&creds, cfg.RefreshLeadMin) {
		return nil // Token still valid
	}

	// Refresh token
	newCreds, err := antigravity.RefreshToken(cfg, account)
	if err != nil {
		return fmt.Errorf("token refresh failed: %w", err)
	}

	// Update in DB
	credsJSON, _ := json.Marshal(newCreds)
	if err := database.UpdateAccountCredentials(account.ID, credsJSON); err != nil {
		return fmt.Errorf("save refreshed credentials: %w", err)
	}

	// Update in-memory account credentials for this request
	account.Credentials = credsJSON

	return nil
}

// RefreshKiroIfNeeded refreshes Kiro token if expired
func RefreshKiroIfNeeded(cfg *config.Config, database *db.Database, account *db.Account) error {
	var creds kiro.KiroCredentials
	if err := json.Unmarshal(account.Credentials, &creds); err != nil {
		return fmt.Errorf("parse kiro credentials: %w", err)
	}

	if !kiro.IsTokenExpired(&creds, cfg.RefreshLeadMin) {
		return nil
	}

	newCreds, err := kiro.RefreshToken(account)
	if err != nil {
		return fmt.Errorf("kiro refresh failed: %w", err)
	}

	credsJSON, _ := json.Marshal(newCreds)
	if err := database.UpdateAccountCredentials(account.ID, credsJSON); err != nil {
		return fmt.Errorf("save kiro credentials: %w", err)
	}

	account.Credentials = credsJSON
	return nil
}

// handleChatCompletionsWithRefresh wraps the chat handler with inline token refresh
func (s *Server) handleChatCompletionsWithRefresh(w http.ResponseWriter, r *http.Request) {
	// This is integrated into handleChatCompletions via the retry loop
	// When we pick an account, we refresh if needed before executing
	s.handleChatCompletions(w, r)
}

// EnhancedPick picks an account and ensures its token is fresh
func (s *Server) EnhancedPick(provider string) (*db.Account, error) {
	account, err := s.pool.Pick(provider)
	if err != nil {
		return nil, err
	}

	// Inline refresh if needed
	if provider == "antigravity" {
		if err := RefreshIfNeeded(s.cfg, s.db, account); err != nil {
			// Mark error but still return account (might work with old token)
			s.db.MarkAccountError(account.ID, "refresh_warning: "+err.Error(), 0)
		}
	}

	return account, nil
}

// --- Rate Limiting ---

type RateLimiter struct {
	database *db.Database
}

func NewRateLimiter(database *db.Database) *RateLimiter {
	return &RateLimiter{database: database}
}

// CheckRateLimit verifies the API key hasn't exceeded its limits
func (rl *RateLimiter) CheckRateLimit(key *db.APIKey) error {
	// For now, simple check based on key limits
	// TODO: implement sliding window with usage_logs table
	_ = key
	return nil
}

// --- Request/Response helpers ---

type ErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code,omitempty"`
	} `json:"error"`
}

func NewErrorResponse(message, errType string) *ErrorResponse {
	resp := &ErrorResponse{}
	resp.Error.Message = message
	resp.Error.Type = errType
	return resp
}

// --- Stats tracking ---

type Stats struct {
	TotalRequests   int64         `json:"total_requests"`
	SuccessRequests int64         `json:"success_requests"`
	FailedRequests  int64         `json:"failed_requests"`
	AvgLatencyMs    int64         `json:"avg_latency_ms"`
	Uptime          time.Duration `json:"uptime"`
	StartedAt       time.Time     `json:"started_at"`
}

var serverStats = &Stats{
	StartedAt: time.Now(),
}

func (s *Stats) RecordRequest(success bool, latencyMs int64) {
	s.TotalRequests++
	if success {
		s.SuccessRequests++
	} else {
		s.FailedRequests++
	}
	// Simple moving average
	if s.AvgLatencyMs == 0 {
		s.AvgLatencyMs = latencyMs
	} else {
		s.AvgLatencyMs = (s.AvgLatencyMs*9 + latencyMs) / 10
	}
}

func (s *Stats) ToJSON() map[string]interface{} {
	return map[string]interface{}{
		"total_requests":   s.TotalRequests,
		"success_requests": s.SuccessRequests,
		"failed_requests":  s.FailedRequests,
		"avg_latency_ms":   s.AvgLatencyMs,
		"uptime_seconds":   int(time.Since(s.StartedAt).Seconds()),
		"started_at":       s.StartedAt.Format(time.RFC3339),
	}
}

func init() {
	// Suppress unused import warnings
	_ = fmt.Sprintf
	_ = http.StatusOK
}
