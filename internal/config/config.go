package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func init() {
	// Auto-load ~/.liam/.env on startup (before any config reads)
	loadDotEnv()
}

// loadDotEnv reads ~/.liam/.env and sets env vars (doesn't override existing)
func loadDotEnv() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	envPath := filepath.Join(home, ".liam", ".env")
	data, err := os.ReadFile(envPath)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		// Don't override existing env vars (explicit env takes priority)
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
}

type Config struct {
	// Server
	Port int

	// Database
	SupabaseURL        string
	SupabaseKey        string
	SupabaseDBPassword string
	DBPath             string

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

	// Provider behaviour knobs
	// KiroThinkingDefault is the implicit thinking budget applied to
	// every Kiro request that doesn't carry an explicit DSL suffix
	// (e.g. `kr/claude-opus-4.7`). Accepts: "off", "low", "medium",
	// "high", "max", or a numeric token budget. The DSL suffix on the
	// model id always wins — `kr/claude-opus-4.7(none)` forces no
	// thinking even when the default is "max".
	KiroThinkingDefault string

	// Token refresh
	RefreshLeadMin int // Refresh token N minutes before expiry
}

func Load() *Config {
	port, _ := strconv.Atoi(getEnv("LIAM_PORT", "666"))
	// Pre-emptive throttle: 0 = disabled (default). Only opt-in users
	// who really want client-side rate limiting set these. The reactive
	// 429/401 cooldown still runs regardless.
	accountRPM, _ := strconv.Atoi(getEnv("LIAM_ACCOUNT_RPM", "0"))
	accountMinGap, _ := strconv.Atoi(getEnv("LIAM_ACCOUNT_MIN_GAP", "0"))
	cooldownBase, _ := strconv.Atoi(getEnv("LIAM_COOLDOWN_BASE", "60"))
	cooldownMax, _ := strconv.Atoi(getEnv("LIAM_COOLDOWN_MAX", "1800"))
	disableAfter, _ := strconv.Atoi(getEnv("LIAM_DISABLE_AFTER_ERRORS", "10"))
	stickyReqs, _ := strconv.Atoi(getEnv("LIAM_STICKY_REQUESTS", "3"))

	homeDir, _ := os.UserHomeDir()
	defaultDBPath := filepath.Join(homeDir, ".liam", "data.db")

	return &Config{
		Port: port,

		// Database
		SupabaseURL:        getEnv("SUPABASE_URL", ""),
		SupabaseKey:        getEnv("SUPABASE_KEY", ""),
		SupabaseDBPassword: getEnv("SUPABASE_DB_PASSWORD", ""),
		DBPath:             getEnv("LIAM_DB_PATH", defaultDBPath),

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
		MaxRetriesPerRequest: 25,
		// Reactive cooldown still runs unconditionally; pre-emptive
		// throttling is opt-in via these knobs. Default 0 = unlimited
		// (matches 9router's "non-stop coding" UX).
		AccountRPM:         accountRPM,
		AccountMinGapSec:   accountMinGap,
		CooldownBaseSec:    cooldownBase, // 60s first cooldown
		CooldownMaxSec:     cooldownMax,  // 30 min max cooldown
		DisableAfterErrors: disableAfter, // Disable after 10 consecutive errors
		StickyRequests:     stickyReqs,   // Use same account for 3 requests then rotate

		// Default Kiro thinking budget (applied when the request doesn't
		// supply its own DSL suffix). "max" gives every Opus/Sonnet
		// Claude on Kiro the upstream's full thinking budget out of
		// the box — operators who want lighter behaviour can set
		// LIAM_KIRO_THINKING_DEFAULT=off / low / medium / high.
		KiroThinkingDefault: getEnv("LIAM_KIRO_THINKING_DEFAULT", "max"),

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
