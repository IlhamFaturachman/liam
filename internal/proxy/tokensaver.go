package proxy

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/liam-auto/liam/internal/caveman"
	"github.com/liam-auto/liam/internal/db"
	"github.com/liam-auto/liam/internal/rtk"
)

// Token-saver setting keys, kept namespaced to avoid colliding with future
// "settings" rows. Operators can edit these via the dashboard
// (Settings → Token Savers) or directly in SQLite.
const (
	settingRtkEnabled     = "token_saver.rtk_enabled"
	settingCavemanEnabled = "token_saver.caveman_enabled"
	settingCavemanLevel   = "token_saver.caveman_level"
)

// Token-saver default values. RTK ships ON because it's invisible to the
// user (just compresses request bytes); Caveman ships OFF because it
// changes the model's *reply style*, which surprises new users.
const (
	defaultRtkEnabled     = true
	defaultCavemanEnabled = false
	defaultCavemanLevel   = string(caveman.LevelLite)
)

// tokenSaverPolicy holds the merged setting state for one request.
// Built from DB defaults + per-request header overrides so an operator
// can flip behaviour ad-hoc with X-Liam-Rtk / X-Liam-Caveman without
// restarting.
type tokenSaverPolicy struct {
	RtkEnabled     bool
	CavemanEnabled bool
	CavemanLevel   caveman.Level
}

// loadTokenSaverPolicy reads the global defaults from DB and applies
// any per-request header overrides. Headers checked:
//
//	X-Liam-Rtk: on / off / true / false
//	X-Liam-Caveman: off / lite / full / ultra
//
// Any value the headers can't parse falls back to the DB setting.
func loadTokenSaverPolicy(database *db.Database, header http.Header) tokenSaverPolicy {
	rtkOn := parseBoolSetting(database.GetSetting(settingRtkEnabled, boolToStr(defaultRtkEnabled)), defaultRtkEnabled)
	cavemanOn := parseBoolSetting(database.GetSetting(settingCavemanEnabled, boolToStr(defaultCavemanEnabled)), defaultCavemanEnabled)
	cavemanLevel := caveman.Level(database.GetSetting(settingCavemanLevel, defaultCavemanLevel))
	if !caveman.IsValidLevel(string(cavemanLevel)) {
		cavemanLevel = caveman.Level(defaultCavemanLevel)
	}

	if h := strings.ToLower(strings.TrimSpace(header.Get("X-Liam-Rtk"))); h != "" {
		switch h {
		case "on", "true", "1", "yes":
			rtkOn = true
		case "off", "false", "0", "no":
			rtkOn = false
		}
	}
	if h := strings.ToLower(strings.TrimSpace(header.Get("X-Liam-Caveman"))); h != "" {
		switch h {
		case "off", "false", "0", "no":
			cavemanOn = false
		case "lite", "full", "ultra":
			cavemanOn = true
			cavemanLevel = caveman.Level(h)
		}
	}

	return tokenSaverPolicy{
		RtkEnabled:     rtkOn,
		CavemanEnabled: cavemanOn,
		CavemanLevel:   cavemanLevel,
	}
}

// applyTokenSavers runs RTK (compress tool_result content) and caveman
// (inject terse-style system message) on the request body in place,
// returning the re-marshalled body and an updated `req` map.
//
// The body is mutated AFTER thinking DSL handling and BEFORE provider
// translateRequest, which means:
//
//  1. Thinking suffixes have already been parsed off the model name.
//  2. The body is still in OpenAI canonical shape (LIAM never speaks
//     anything else to clients), so RTK and caveman are
//     provider-agnostic — Kiro, AG, or any future provider plugged
//     into LIAM gets the savings without knowing they exist.
//  3. The Kiro overlay system prompt is injected DOWNSTREAM (in the
//     Kiro translator), so caveman ends up nested inside the existing
//     system content, NOT replacing it. The overlay still wraps the
//     final user message; caveman just rides along as part of the
//     "Developer instructions:" block. Verified safe.
func applyTokenSavers(req map[string]any, body []byte, policy tokenSaverPolicy) []byte {
	mutated := false

	if policy.RtkEnabled {
		stats := rtk.Compress(req, true)
		if line := rtk.FormatLog(stats); line != "" {
			log.Println(line)
		}
		if stats != nil && len(stats.Hits) > 0 {
			mutated = true
		}
	}

	if policy.CavemanEnabled {
		if caveman.Inject(req, policy.CavemanLevel) {
			mutated = true
			log.Printf("[CAVEMAN] %s injected", policy.CavemanLevel)
		}
	}

	if !mutated {
		return body
	}
	out, err := json.Marshal(req)
	if err != nil {
		// Marshal failure is exceedingly unlikely (we just unmarshalled
		// the same map). If it happens, fall back to the original body
		// so the request still goes through — better degraded than
		// failed.
		log.Printf("[WARN] applyTokenSavers re-marshal failed: %v — using original body", err)
		return body
	}
	return out
}

func parseBoolSetting(s string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "on", "1", "yes":
		return true
	case "false", "off", "0", "no":
		return false
	}
	return fallback
}

func boolToStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// --- HTTP handlers --------------------------------------------------

// handleGetTokenSaver returns the current saved policy + the constants
// the dashboard needs to render the Settings → Token Savers panel.
//
// Shape:
//
//	{
//	  "rtk_enabled":     true,
//	  "caveman_enabled": false,
//	  "caveman_level":   "lite",
//	  "caveman_levels":  ["lite", "full", "ultra"]
//	}
func (s *Server) handleGetTokenSaver(w http.ResponseWriter, r *http.Request) {
	policy := loadTokenSaverPolicy(s.db, http.Header{}) // no header overrides for the read view
	resp := map[string]any{
		"rtk_enabled":     policy.RtkEnabled,
		"caveman_enabled": policy.CavemanEnabled,
		"caveman_level":   string(policy.CavemanLevel),
		"caveman_levels": []string{
			string(caveman.LevelLite),
			string(caveman.LevelFull),
			string(caveman.LevelUltra),
		},
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSetTokenSaver accepts a partial JSON object. Each field is
// optional — operators can flip RTK without touching caveman, etc.
//
// Body:
//
//	{
//	  "rtk_enabled":     true,            // optional bool
//	  "caveman_enabled": false,           // optional bool
//	  "caveman_level":   "lite|full|ultra" // optional, validated
//	}
//
// Unknown levels return 400 to make typos obvious instead of silently
// falling back to default.
func (s *Server) handleSetTokenSaver(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RtkEnabled     *bool   `json:"rtk_enabled,omitempty"`
		CavemanEnabled *bool   `json:"caveman_enabled,omitempty"`
		CavemanLevel   *string `json:"caveman_level,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	if input.RtkEnabled != nil {
		if err := s.db.SetSetting(settingRtkEnabled, boolToStr(*input.RtkEnabled)); err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to save rtk setting: "+err.Error())
			return
		}
	}
	if input.CavemanEnabled != nil {
		if err := s.db.SetSetting(settingCavemanEnabled, boolToStr(*input.CavemanEnabled)); err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to save caveman setting: "+err.Error())
			return
		}
	}
	if input.CavemanLevel != nil {
		if !caveman.IsValidLevel(*input.CavemanLevel) {
			writeError(w, http.StatusBadRequest, "Unknown caveman level (must be lite/full/ultra)")
			return
		}
		if err := s.db.SetSetting(settingCavemanLevel, *input.CavemanLevel); err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to save caveman level: "+err.Error())
			return
		}
	}

	// Echo the now-current state so the dashboard can refresh without a
	// second roundtrip.
	s.handleGetTokenSaver(w, r)
}
