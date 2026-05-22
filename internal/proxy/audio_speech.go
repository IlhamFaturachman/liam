package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/liam-auto/liam/internal/db"
	"github.com/liam-auto/liam/internal/providers/elevenlabs"
)

// handleAudioSpeech handles POST /v1/audio/speech (OpenAI-compatible TTS).
func (s *Server) handleAudioSpeech(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Failed to read request body")
		return
	}
	defer r.Body.Close()

	var req elevenlabs.SpeechRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Input == "" {
		writeError(w, http.StatusBadRequest, "Missing 'input' field")
		return
	}

	model := req.Model
	if model == "" {
		model = "el/eleven_flash_v2_5"
	}
	// Ensure el/ prefix for pool routing
	if !strings.HasPrefix(model, "el/") && !strings.HasPrefix(model, "elevenlabs/") {
		model = "el/" + model
	}

	var lastErr error
	for attempt := 0; attempt < s.cfg.MaxRetriesPerRequest; attempt++ {
		account, err := s.pool.PickForModel("elevenlabs", model)
		if err != nil {
			if attempt < s.cfg.MaxRetriesPerRequest-1 {
				time.Sleep(time.Duration(BackoffBaseMs) * time.Millisecond)
				continue
			}
			writeError(w, http.StatusServiceUnavailable, fmt.Sprintf("No available accounts: %v", err))
			return
		}

		resp, err := s.el.ExecuteWithSession(account, model, body, true, "")
		if err != nil {
			lastErr = err
			cooldown, _ := s.applyAccountError(account, 0, []byte(err.Error()), nil)
			log.Printf("[TTS RETRY %d] %s transport error: %v", attempt+1, account.Email, err)
			if s.pool.Count("elevenlabs") <= 1 {
				time.Sleep(cooldown)
			}
			continue
		}

		// Non-retryable (422 with EL-specific errors)
		if resp.StatusCode == 422 {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if IsNonRetryable(resp.StatusCode, respBody) {
				writeError(w, resp.StatusCode, ExtractErrorMessage(respBody))
				return
			}
			lastErr = fmt.Errorf("HTTP 422: %s", string(respBody[:min(len(respBody), 100)]))
			s.applyAccountError(account, resp.StatusCode, respBody, resp.Header)
			continue
		}

		// 401/429/5xx — rotate
		if resp.StatusCode == 401 || resp.StatusCode == 429 || resp.StatusCode >= 500 {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			cooldown, _ := s.applyAccountError(account, resp.StatusCode, respBody, resp.Header)
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			log.Printf("[TTS RETRY %d] %s HTTP %d", attempt+1, account.Email, resp.StatusCode)
			if s.pool.Count("elevenlabs") <= 1 {
				time.Sleep(cooldown)
			}
			continue
		}

		// Success — stream binary audio to client
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		resp.Body.Close()

		// Decrement quota from character-cost header (best-effort)
		if costStr := resp.Header.Get("character-cost"); costStr != "" {
			if cost, err := strconv.Atoi(costStr); err == nil && cost > 0 {
				s.db.DecrementAccountQuota(account.ID, cost)
			}
		}

		s.db.MarkAccountSuccess(account.ID)
		return
	}

	writeError(w, http.StatusBadGateway, fmt.Sprintf("All TTS retries failed: %v", lastErr))
}

// handleAddElevenLabsAccount validates and adds an ElevenLabs account.
// Called from handleAddAccount when provider == "elevenlabs".
func (s *Server) handleAddElevenLabsAccount(w http.ResponseWriter, creds json.RawMessage) {
	var c elevenlabs.ELCredentials
	if err := json.Unmarshal(creds, &c); err != nil {
		writeError(w, http.StatusBadRequest, "credentials JSON invalid: "+err.Error())
		return
	}
	if strings.TrimSpace(c.APIKey) == "" {
		writeError(w, http.StatusBadRequest, "api_key is required for elevenlabs accounts")
		return
	}

	// Synthesize email from key hash to avoid UNIQUE constraint collision
	hash := sha256.Sum256([]byte(c.APIKey))
	email := "el-" + hex.EncodeToString(hash[:4]) + "@imported"

	account := &db.Account{
		Provider:    "elevenlabs",
		Email:       email,
		Status:      "active",
		Credentials: creds,
		AuthMethod:  "imported",
	}

	// Fetch subscription quota
	if total, remaining, resetAt, err := elevenlabs.FetchSubscription(c.APIKey); err == nil {
		account.QuotaTotal = total
		account.QuotaRemaining = remaining
		if resetAt != "" {
			if t, parseErr := time.Parse(time.RFC3339, resetAt); parseErr == nil {
				account.QuotaResetAt = &t
			}
		}
	}

	if err := s.db.UpsertAccount(account); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, account)
}
