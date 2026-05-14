package config

import (
	"os"
	"path/filepath"
	"strconv"
)

type Config struct {
	// Server
	Port int

	// Database
	SupabaseURL string
	SupabaseKey string
	DBPath      string

	// Antigravity OAuth
	AGClientID     string
	AGClientSecret string
	AGScopes       []string

	// Anti-ban settings
	MaxRetriesPerRequest int // Max retries before returning error to client
	AccountRPM           int // Max requests per minute per account
	AccountMinGapSec     int // Minimum seconds between uses of same account
	CooldownBaseSec      int // Base cooldown on first error (exponential: base * 2^errors)
	CooldownMaxSec       int // Maximum cooldown duration
	DisableAfterErrors   int // Disable account after N consecutive errors
	StickyRequests       int // Use same account for N requests before rotating (0=pure LRU)

	// Token refresh
	RefreshLeadMin int // Refresh token N minutes before expiry
}

func Load() *Config {
	port, _ := strconv.Atoi(getEnv("LIAM_PORT", "666"))
	accountRPM, _ := strconv.Atoi(getEnv("LIAM_ACCOUNT_RPM", "10"))
	accountMinGap, _ := strconv.Atoi(getEnv("LIAM_ACCOUNT_MIN_GAP", "6"))
	cooldownBase, _ := strconv.Atoi(getEnv("LIAM_COOLDOWN_BASE", "60"))
	cooldownMax, _ := strconv.Atoi(getEnv("LIAM_COOLDOWN_MAX", "1800"))
	disableAfter, _ := strconv.Atoi(getEnv("LIAM_DISABLE_AFTER_ERRORS", "10"))
	stickyReqs, _ := strconv.Atoi(getEnv("LIAM_STICKY_REQUESTS", "3"))

	homeDir, _ := os.UserHomeDir()
	defaultDBPath := filepath.Join(homeDir, ".liam", "data.db")

	return &Config{
		Port: port,

		// Database
		SupabaseURL: getEnv("SUPABASE_URL", ""),
		SupabaseKey: getEnv("SUPABASE_KEY", ""),
		DBPath:      getEnv("LIAM_DB_PATH", defaultDBPath),

		// Antigravity OAuth (Google)
		// These are the public Antigravity IDE OAuth credentials, used by all
		// Antigravity users worldwide. Safe to ship — they identify the app
		// (Antigravity IDE), not individual users. Account-level credentials
		// (access_token, refresh_token) are stored per-account in the DB.
		AGClientID:     getEnv("LIAM_AG_CLIENT_ID", "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"),
		AGClientSecret: getEnv("LIAM_AG_CLIENT_SECRET", "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"),
		AGScopes: []string{
			"https://www.googleapis.com/auth/cloud-platform",
			"https://www.googleapis.com/auth/userinfo.email",
			"https://www.googleapis.com/auth/userinfo.profile",
			"https://www.googleapis.com/auth/cclog",
			"https://www.googleapis.com/auth/experimentsandconfigs",
		},

		// Anti-ban (conservative defaults for longevity)
		MaxRetriesPerRequest: 3,
		AccountRPM:           accountRPM,   // 10 req/min per account (safe: AG allows ~20-30)
		AccountMinGapSec:     accountMinGap, // 6 seconds minimum between uses
		CooldownBaseSec:      cooldownBase,  // 60s first cooldown
		CooldownMaxSec:       cooldownMax,   // 30 min max cooldown
		DisableAfterErrors:   disableAfter,  // Disable after 10 consecutive errors
		StickyRequests:       stickyReqs,    // Use same account for 3 requests then rotate

		// Token refresh
		RefreshLeadMin: 5,
	}
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
