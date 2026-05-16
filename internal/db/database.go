package db

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/liam-auto/liam/internal/config"
	_ "modernc.org/sqlite"
)

// Database wraps SQLite connection with LIAM operations
type Database struct {
	db  *sql.DB
	cfg *config.Config
}

// Account represents a provider account
type Account struct {
	ID                string          `json:"id"`
	Provider          string          `json:"provider"`
	Email             string          `json:"email"`
	Status            string          `json:"status"`
	Credentials       json.RawMessage `json:"credentials"`
	QuotaTotal        int             `json:"quota_total"`
	QuotaRemaining    int             `json:"quota_remaining"`
	QuotaResetAt      *time.Time      `json:"quota_reset_at,omitempty"`
	Plan              string          `json:"plan,omitempty"`            // e.g. "Kiro Pro", "Kiro Free"
	AuthMethod        string          `json:"auth_method,omitempty"`     // "imported" | "builder-id" | "idc" | "google" | "github"
	QuotaBreakdown    json.RawMessage `json:"quota_breakdown,omitempty"` // map[resourceType]{used,total,reset_at} JSON
	ConsecutiveErrors int             `json:"consecutive_errors"`
	LastError         string          `json:"last_error,omitempty"`
	CooldownUntil     *time.Time      `json:"cooldown_until,omitempty"`
	BackoffLevel      int             `json:"backoff_level,omitempty"`
	LastUsedAt        *time.Time      `json:"last_used_at,omitempty"`
	LastLoginAt       *time.Time      `json:"last_login_at,omitempty"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`

	// Derived fields populated when listing accounts. Not persisted.
	TokenExpiresAt string `json:"token_expires_at,omitempty"` // RFC3339 timestamp from credentials.expires_at
	HasCredentials bool   `json:"has_credentials"`            // true when credentials contain a usable token
}

// AGCredentials for Antigravity accounts
type AGCredentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    string `json:"expires_at"`
	ProjectID    string `json:"project_id"`
	TierID       string `json:"tier_id"`
	Scope        string `json:"scope"`
}

// KiroCredentials for Kiro accounts
type KiroCredentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    string `json:"expires_at"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	Region       string `json:"region"`
	ProfileARN   string `json:"profile_arn,omitempty"`
}

// Combo represents a named model group with fallback/round-robin
type Combo struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Models      []string  `json:"models"`
	Strategy    string    `json:"strategy"` // "fallback" | "round-robin"
	StickyLimit int       `json:"sticky_limit"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// APIKey represents a consumer API key
type APIKey struct {
	ID            string     `json:"id"`
	KeyHash       string     `json:"key_hash"`
	KeyPrefix     string     `json:"key_prefix"`
	Name          string     `json:"name"`
	IsActive      bool       `json:"is_active"`
	RateLimitRPM  int        `json:"rate_limit_rpm"`
	RateLimitRPD  int        `json:"rate_limit_rpd"`
	TotalRequests int64      `json:"total_requests"`
	LastUsedAt    *time.Time `json:"last_used_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

// UsageLog represents a single request log
type UsageLog struct {
	ID           string    `json:"id"`
	APIKeyID     string    `json:"api_key_id"`
	AccountID    string    `json:"account_id"`
	AccountEmail string    `json:"account_email"`
	Provider     string    `json:"provider"`
	Model        string    `json:"model"`
	TokensIn     int       `json:"tokens_in"`
	TokensOut    int       `json:"tokens_out"`
	StatusCode   int       `json:"status_code"`
	LatencyMs    int       `json:"latency_ms"`
	Error        string    `json:"error,omitempty"`
	RequestBody  string    `json:"request_body,omitempty"`
	ResponseBody string    `json:"response_body,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// New creates a new Database instance with auto-migration
func New(cfg *config.Config) (*Database, error) {
	// Ensure directory exists
	dir := filepath.Dir(cfg.DBPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	db, err := sql.Open("sqlite", cfg.DBPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Verify connection
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping database: %w", err)
	}

	database := &Database{db: db, cfg: cfg}

	// Auto-migrate
	if err := database.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return database, nil
}

// Close closes the database connection
func (d *Database) Close() error {
	return d.db.Close()
}

// Conn returns the underlying *sql.DB for use by other packages (models, etc.)
func (d *Database) Conn() *sql.DB {
	return d.db
}

// migrate creates tables if they don't exist
func (d *Database) migrate() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS accounts (
			id TEXT PRIMARY KEY,
			provider TEXT NOT NULL,
			email TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active',
			credentials TEXT NOT NULL DEFAULT '{}',
			quota_total INTEGER DEFAULT 0,
			quota_remaining INTEGER DEFAULT 0,
			quota_reset_at TEXT,
			consecutive_errors INTEGER DEFAULT 0,
			last_error TEXT,
			cooldown_until TEXT,
			last_used_at TEXT,
			last_login_at TEXT,
			metadata TEXT DEFAULT '{}',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_provider_email ON accounts(provider, email)`,
		`CREATE INDEX IF NOT EXISTS idx_accounts_status ON accounts(provider, status)`,

		`CREATE TABLE IF NOT EXISTS api_keys (
			id TEXT PRIMARY KEY,
			key_hash TEXT NOT NULL UNIQUE,
			key_prefix TEXT NOT NULL,
			name TEXT NOT NULL,
			is_active INTEGER DEFAULT 1,
			rate_limit_rpm INTEGER DEFAULT 60,
			rate_limit_rpd INTEGER DEFAULT 1000,
			total_requests INTEGER DEFAULT 0,
			last_used_at TEXT,
			created_at TEXT NOT NULL
		)`,

		`CREATE TABLE IF NOT EXISTS usage_logs (
			id TEXT PRIMARY KEY,
			api_key_id TEXT,
			account_id TEXT,
			account_email TEXT,
			provider TEXT,
			model TEXT,
			tokens_in INTEGER DEFAULT 0,
			tokens_out INTEGER DEFAULT 0,
			status_code INTEGER,
			latency_ms INTEGER,
			error TEXT,
			request_body TEXT,
			response_body TEXT,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_created ON usage_logs(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_apikey ON usage_logs(api_key_id, created_at)`,

		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,

		`CREATE TABLE IF NOT EXISTS model_registry (
			id TEXT PRIMARY KEY,
			provider_alias TEXT NOT NULL,
			model_id TEXT NOT NULL,
			display_name TEXT NOT NULL,
			type TEXT DEFAULT 'llm',
			is_custom INTEGER DEFAULT 0,
			is_enabled INTEGER DEFAULT 1,
			metadata TEXT DEFAULT '{}',
			created_at TEXT NOT NULL,
			UNIQUE(provider_alias, model_id, type)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_model_provider ON model_registry(provider_alias, is_enabled)`,

		`CREATE TABLE IF NOT EXISTS model_aliases (
			alias TEXT PRIMARY KEY,
			target TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,

		`CREATE TABLE IF NOT EXISTS combos (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			models TEXT NOT NULL DEFAULT '[]',
			strategy TEXT DEFAULT 'fallback',
			sticky_limit INTEGER DEFAULT 1,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
	}

	for _, m := range migrations {
		if _, err := d.db.Exec(m); err != nil {
			return fmt.Errorf("migration failed: %w\nSQL: %s", err, m)
		}
	}

	// Safe column additions (ALTER TABLE — ignore errors if column already exists)
	alterStatements := []string{
		"ALTER TABLE accounts ADD COLUMN priority INTEGER DEFAULT 0",
		"ALTER TABLE accounts ADD COLUMN consecutive_use_count INTEGER DEFAULT 0",
		"ALTER TABLE accounts ADD COLUMN model_locks TEXT DEFAULT '{}'",
		"ALTER TABLE accounts ADD COLUMN plan TEXT DEFAULT ''",
		"ALTER TABLE accounts ADD COLUMN auth_method TEXT DEFAULT ''",
		"ALTER TABLE accounts ADD COLUMN quota_breakdown TEXT DEFAULT '{}'",
		// Tracks the per-account 429 backoff level (matches 9router):
		// every rate-limit hit bumps the level (cooldown 2s, 4s, 8s, …
		// up to 5 min); a successful request resets it to 0. Replaces
		// the much harsher per-(account, model) lockout the proxy used
		// to do which made single-account users 503 within minutes.
		"ALTER TABLE accounts ADD COLUMN backoff_level INTEGER DEFAULT 0",
	}
	for _, stmt := range alterStatements {
		d.db.Exec(stmt) // Ignore errors (column already exists)
	}

	// Heal accounts written by older builds: empty/NULL credentials and
	// metadata break json_extract() and Supabase NOT NULL pushes. Replace
	// them with valid empty objects.
	repairStatements := []string{
		"UPDATE accounts SET credentials = '{}' WHERE credentials IS NULL OR credentials = '' OR json_valid(credentials) = 0",
		"UPDATE accounts SET metadata = '{}' WHERE metadata IS NULL OR metadata = '' OR json_valid(metadata) = 0",
		"UPDATE accounts SET quota_breakdown = '{}' WHERE quota_breakdown IS NULL OR quota_breakdown = '' OR json_valid(quota_breakdown) = 0",
	}
	for _, stmt := range repairStatements {
		d.db.Exec(stmt) // best-effort, log nothing
	}

	return nil
}

// --- Account Operations ---

// UpsertAccount creates or updates an account
func (d *Database) UpsertAccount(a *Account) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if a.ID == "" {
		a.ID = uuid.New().String()
	}

	// Normalize JSON columns: SQLite + JSONB downstream both expect a real
	// JSON object string, never empty string. An empty Credentials/Metadata
	// would make json_extract() and Supabase NOT NULL fail later.
	credsStr := strings.TrimSpace(string(a.Credentials))
	if credsStr == "" {
		credsStr = "{}"
	}
	metaStr := strings.TrimSpace(string(a.Metadata))
	if metaStr == "" {
		metaStr = "{}"
	}
	breakdown := strings.TrimSpace(string(a.QuotaBreakdown))
	if breakdown == "" {
		breakdown = "{}"
	}

	_, err := d.db.Exec(`
		INSERT INTO accounts (id, provider, email, status, credentials, quota_total, quota_remaining, 
			quota_reset_at, plan, auth_method, quota_breakdown,
			consecutive_errors, last_error, cooldown_until, last_used_at, last_login_at, metadata, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider, email) DO UPDATE SET
			status=excluded.status, credentials=excluded.credentials, 
			quota_total=excluded.quota_total, quota_remaining=excluded.quota_remaining,
			quota_reset_at=excluded.quota_reset_at,
			plan=excluded.plan, auth_method=excluded.auth_method, quota_breakdown=excluded.quota_breakdown,
			consecutive_errors=excluded.consecutive_errors,
			last_error=excluded.last_error, cooldown_until=excluded.cooldown_until,
			last_used_at=excluded.last_used_at, last_login_at=excluded.last_login_at,
			metadata=excluded.metadata, updated_at=excluded.updated_at`,
		a.ID, a.Provider, a.Email, a.Status, credsStr,
		a.QuotaTotal, a.QuotaRemaining, timePtr(a.QuotaResetAt),
		a.Plan, a.AuthMethod, breakdown,
		a.ConsecutiveErrors, a.LastError, timePtr(a.CooldownUntil),
		timePtr(a.LastUsedAt), timePtr(a.LastLoginAt), metaStr,
		now, now,
	)
	return err
}

// ListAccounts returns accounts filtered by provider (empty = all)
func (d *Database) ListAccounts(provider string) ([]Account, error) {
	var rows *sql.Rows
	var err error

	const baseSelect = `SELECT id, provider, email, status, credentials, quota_total, quota_remaining,
		quota_reset_at, plan, auth_method, quota_breakdown,
		consecutive_errors, last_error, cooldown_until,
		COALESCE(backoff_level, 0), created_at, updated_at FROM accounts`

	if provider == "" {
		rows, err = d.db.Query(baseSelect + " ORDER BY provider, email")
	} else {
		rows, err = d.db.Query(baseSelect+" WHERE provider = ? ORDER BY email", provider)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var accounts []Account
	for rows.Next() {
		var a Account
		var creds, lastErr, plan, authMethod, breakdown, quotaResetAt, cooldownUntil sql.NullString
		var createdAt, updatedAt sql.NullString
		var backoffLevel int
		err := rows.Scan(&a.ID, &a.Provider, &a.Email, &a.Status, &creds,
			&a.QuotaTotal, &a.QuotaRemaining,
			&quotaResetAt, &plan, &authMethod, &breakdown,
			&a.ConsecutiveErrors, &lastErr, &cooldownUntil,
			&backoffLevel, &createdAt, &updatedAt)
		if err != nil {
			return nil, err
		}
		if creds.Valid && creds.String != "" {
			a.Credentials = json.RawMessage(creds.String)
		} else {
			a.Credentials = json.RawMessage("{}")
		}
		// Derive token metadata from credentials JSON so the dashboard can
		// surface expiry + a "no credentials, please re-import" hint
		// without leaking the raw token.
		var credsMap map[string]interface{}
		if jErr := json.Unmarshal(a.Credentials, &credsMap); jErr == nil {
			if exp, ok := credsMap["expires_at"].(string); ok && exp != "" {
				a.TokenExpiresAt = exp
			}
			access, _ := credsMap["access_token"].(string)
			refresh, _ := credsMap["refresh_token"].(string)
			a.HasCredentials = strings.TrimSpace(access) != "" || strings.TrimSpace(refresh) != ""
		}
		if lastErr.Valid {
			a.LastError = lastErr.String
		}
		if plan.Valid {
			a.Plan = plan.String
		}
		if authMethod.Valid {
			a.AuthMethod = authMethod.String
		}
		if breakdown.Valid && breakdown.String != "" {
			a.QuotaBreakdown = json.RawMessage(breakdown.String)
		}
		if quotaResetAt.Valid && quotaResetAt.String != "" {
			t, errParse := time.Parse(time.RFC3339Nano, quotaResetAt.String)
			if errParse == nil {
				a.QuotaResetAt = &t
			}
		}
		// cooldown_until drives the per-account "in cooldown" badge on
		// the dashboard. Stale rows (cooldown_until in the past) are
		// still surfaced — the UI hides the badge once the timestamp
		// lapses without us needing to clear the column.
		if cooldownUntil.Valid && cooldownUntil.String != "" {
			t, errParse := time.Parse(time.RFC3339Nano, cooldownUntil.String)
			if errParse == nil {
				a.CooldownUntil = &t
			}
		}
		a.BackoffLevel = backoffLevel
		if createdAt.Valid {
			a.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt.String)
		}
		if updatedAt.Valid {
			a.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt.String)
		}
		accounts = append(accounts, a)
	}
	return accounts, nil
}

// GetActiveAccounts returns accounts available for proxying
func (d *Database) GetActiveAccounts(provider string) ([]Account, error) {
	rows, err := d.db.Query(`
		SELECT id, provider, email, status, credentials, quota_total, quota_remaining, 
			consecutive_errors, last_used_at, cooldown_until, created_at, updated_at
		FROM accounts 
		WHERE provider = ? AND status = 'active' 
			AND (cooldown_until IS NULL OR cooldown_until < ?)
		ORDER BY last_used_at ASC NULLS FIRST`,
		provider, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var accounts []Account
	for rows.Next() {
		var a Account
		var creds sql.NullString
		var lastUsed, cooldown, createdAt, updatedAt sql.NullString
		err := rows.Scan(&a.ID, &a.Provider, &a.Email, &a.Status, &creds,
			&a.QuotaTotal, &a.QuotaRemaining, &a.ConsecutiveErrors,
			&lastUsed, &cooldown, &createdAt, &updatedAt)
		if err != nil {
			return nil, err
		}
		if creds.Valid && creds.String != "" {
			a.Credentials = json.RawMessage(creds.String)
		} else {
			a.Credentials = json.RawMessage("{}")
		}
		if lastUsed.Valid {
			t, _ := time.Parse(time.RFC3339Nano, lastUsed.String)
			a.LastUsedAt = &t
		}
		if createdAt.Valid {
			a.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt.String)
		}
		accounts = append(accounts, a)
	}
	return accounts, nil
}

// MarkAccountUsed updates last_used_at
func (d *Database) MarkAccountUsed(id string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := d.db.Exec("UPDATE accounts SET last_used_at = ?, updated_at = ? WHERE id = ?", now, now, id)
	return err
}

// MarkAccountError records the latest failure on an account and optionally
// puts it on cooldown. Auto-disable is intentionally NOT applied here —
// 9router's reference implementation never auto-disables, and our previous
// "disable after 10 consecutive errors" rule turned routine 429s into
// permanent blackouts for users with one or two accounts. Operators can
// still mark accounts disabled manually from the dashboard.
func (d *Database) MarkAccountError(id string, errMsg string, cooldownSecs int) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)

	var cooldown *string
	if cooldownSecs > 0 {
		t := time.Now().UTC().Add(time.Duration(cooldownSecs) * time.Second).Format(time.RFC3339Nano)
		cooldown = &t
	}

	_, err := d.db.Exec(`
		UPDATE accounts SET 
			consecutive_errors = consecutive_errors + 1,
			last_error = ?,
			cooldown_until = COALESCE(?, cooldown_until),
			updated_at = ?
		WHERE id = ?`, errMsg, cooldown, now, id)
	return err
}

// SetAccountStatus updates an account's status (e.g. "active", "disabled",
// "cooldown") and optionally records the reason in last_error. Used by the
// refresh worker to quarantine accounts whose refresh_token has been revoked
// upstream — leaving them as 'active' would have the worker keep hammering
// Google with a known-bad token every 5 minutes, which is exactly the
// behaviour that gets the outbound IP rate-limited.
func (d *Database) SetAccountStatus(id, status, reason string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if reason == "" {
		_, err := d.db.Exec(`UPDATE accounts SET status = ?, updated_at = ? WHERE id = ?`,
			status, now, id)
		return err
	}
	_, err := d.db.Exec(`UPDATE accounts SET status = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		status, reason, now, id)
	return err
}

// BumpBackoff bumps the per-account 429 backoff level by one (capped at
// `maxLevel`) and writes the resulting cooldown window to the account
// row. Returns the new level so callers can log it. Mirrors 9router's
// `getQuotaCooldown` schedule: 2s, 4s, 8s, 16s, …, capped at 5 min.
func (d *Database) BumpBackoff(id string, maxLevel int, baseSec int, maxSec int) (int, error) {
	if maxLevel <= 0 {
		maxLevel = 15
	}
	if baseSec <= 0 {
		baseSec = 2
	}
	if maxSec <= 0 {
		maxSec = 5 * 60
	}

	var current int
	if err := d.db.QueryRow(`SELECT COALESCE(backoff_level, 0) FROM accounts WHERE id = ?`, id).Scan(&current); err != nil {
		return 0, err
	}
	next := current + 1
	if next > maxLevel {
		next = maxLevel
	}

	// Exponential schedule capped at maxSec.
	cooldownSec := baseSec
	for i := 1; i < next; i++ {
		cooldownSec *= 2
		if cooldownSec >= maxSec {
			cooldownSec = maxSec
			break
		}
	}

	now := time.Now().UTC()
	cooldownUntil := now.Add(time.Duration(cooldownSec) * time.Second).Format(time.RFC3339Nano)
	_, err := d.db.Exec(`
		UPDATE accounts SET
			backoff_level = ?,
			cooldown_until = ?,
			updated_at = ?
		WHERE id = ?`, next, cooldownUntil, now.Format(time.RFC3339Nano), id)
	return next, err
}

// MarkAccountSuccess resets error count and the 429 backoff level. We
// always clear backoff on success so an account that recovers from a
// rough patch isn't permanently stuck at the longest cooldown step.
func (d *Database) MarkAccountSuccess(id string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := d.db.Exec(`
		UPDATE accounts SET
			consecutive_errors = 0,
			last_error = NULL,
			backoff_level = 0,
			status = CASE WHEN status = 'disabled' THEN status ELSE 'active' END,
			updated_at = ?
		WHERE id = ?`, now, id)
	return err
}

// UpdateAccountCredentials updates credentials and optionally status
func (d *Database) UpdateAccountCredentials(id string, creds json.RawMessage) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := d.db.Exec("UPDATE accounts SET credentials = ?, updated_at = ? WHERE id = ?",
		string(creds), now, id)
	return err
}

// GetAccountsNeedingRefresh returns accounts with tokens expiring soon
func (d *Database) GetAccountsNeedingRefresh(provider string, withinMinutes int) ([]Account, error) {
	// We check credentials JSON for expires_at field. Guard with json_valid()
	// so a single malformed row doesn't poison the whole query.
	threshold := time.Now().UTC().Add(time.Duration(withinMinutes) * time.Minute).Format(time.RFC3339Nano)

	rows, err := d.db.Query(`
		SELECT id, provider, email, status, credentials, created_at, updated_at
		FROM accounts 
		WHERE provider = ? AND status = 'active'
			AND credentials IS NOT NULL
			AND credentials != ''
			AND json_valid(credentials) = 1
			AND json_extract(credentials, '$.expires_at') IS NOT NULL
			AND json_extract(credentials, '$.expires_at') < ?
		ORDER BY json_extract(credentials, '$.expires_at') ASC`,
		provider, threshold)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var accounts []Account
	for rows.Next() {
		var a Account
		var creds sql.NullString
		var createdAt, updatedAt sql.NullString
		err := rows.Scan(&a.ID, &a.Provider, &a.Email, &a.Status, &creds, &createdAt, &updatedAt)
		if err != nil {
			return nil, err
		}
		if creds.Valid {
			a.Credentials = json.RawMessage(creds.String)
		}
		if createdAt.Valid {
			a.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt.String)
		}
		accounts = append(accounts, a)
	}
	return accounts, nil
}

// --- API Key Operations ---

// CreateAPIKey generates a new API key and returns the key object + raw key
func (d *Database) CreateAPIKey(name string) (*APIKey, string, error) {
	// Generate random key with the canonical lyd- prefix (32-byte hex).
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", err
	}
	rawKey := "lyd-" + hex.EncodeToString(raw)

	// Hash for storage
	hash := sha256.Sum256([]byte(rawKey))
	keyHash := hex.EncodeToString(hash[:])

	key := &APIKey{
		ID:           uuid.New().String(),
		KeyHash:      keyHash,
		KeyPrefix:    rawKey[:16],
		Name:         name,
		IsActive:     true,
		RateLimitRPM: 60,
		RateLimitRPD: 1000,
		CreatedAt:    time.Now().UTC(),
	}

	_, err := d.db.Exec(`
		INSERT INTO api_keys (id, key_hash, key_prefix, name, is_active, rate_limit_rpm, rate_limit_rpd, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		key.ID, key.KeyHash, key.KeyPrefix, key.Name, key.IsActive,
		key.RateLimitRPM, key.RateLimitRPD, key.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return nil, "", err
	}

	return key, rawKey, nil
}

// ValidateAPIKey checks if a raw key is valid and active
func (d *Database) ValidateAPIKey(rawKey string) (*APIKey, error) {
	hash := sha256.Sum256([]byte(rawKey))
	keyHash := hex.EncodeToString(hash[:])

	var key APIKey
	var createdAt string
	var lastUsed sql.NullString

	err := d.db.QueryRow(`
		SELECT id, key_hash, key_prefix, name, is_active, rate_limit_rpm, rate_limit_rpd, total_requests, last_used_at, created_at
		FROM api_keys WHERE key_hash = ?`, keyHash).Scan(
		&key.ID, &key.KeyHash, &key.KeyPrefix, &key.Name, &key.IsActive,
		&key.RateLimitRPM, &key.RateLimitRPD, &key.TotalRequests, &lastUsed, &createdAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("invalid API key")
		}
		return nil, err
	}

	if !key.IsActive {
		return nil, fmt.Errorf("API key is disabled")
	}

	key.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	if lastUsed.Valid {
		t, _ := time.Parse(time.RFC3339Nano, lastUsed.String)
		key.LastUsedAt = &t
	}

	// Update last_used and total_requests
	now := time.Now().UTC().Format(time.RFC3339Nano)
	d.db.Exec("UPDATE api_keys SET last_used_at = ?, total_requests = total_requests + 1 WHERE id = ?", now, key.ID)

	return &key, nil
}

// ListAPIKeys returns all API keys
func (d *Database) ListAPIKeys() ([]APIKey, error) {
	rows, err := d.db.Query("SELECT id, key_prefix, name, is_active, rate_limit_rpm, rate_limit_rpd, total_requests, created_at FROM api_keys ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []APIKey
	for rows.Next() {
		var k APIKey
		var createdAt string
		err := rows.Scan(&k.ID, &k.KeyPrefix, &k.Name, &k.IsActive, &k.RateLimitRPM, &k.RateLimitRPD, &k.TotalRequests, &createdAt)
		if err != nil {
			return nil, err
		}
		k.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		keys = append(keys, k)
	}
	return keys, nil
}

// DeleteAPIKey hard-deletes an API key by ID
func (d *Database) DeleteAPIKey(id string) error {
	_, err := d.db.Exec("DELETE FROM api_keys WHERE id = ?", id)
	return err
}

// DeleteAccount hard-deletes an account by ID
func (d *Database) DeleteAccount(id string) error {
	_, err := d.db.Exec("DELETE FROM accounts WHERE id = ?", id)
	return err
}

// ImportAPIKey imports a key from remote (Supabase sync) — inserts if not exists
func (d *Database) ImportAPIKey(id, keyHash, keyPrefix, name string, isActive bool, createdAt time.Time) error {
	active := 0
	if isActive {
		active = 1
	}
	_, err := d.db.Exec(`
		INSERT OR IGNORE INTO api_keys (id, key_hash, key_prefix, name, is_active, rate_limit_rpm, rate_limit_rpd, created_at)
		VALUES (?, ?, ?, ?, ?, 60, 1000, ?)`,
		id, keyHash, keyPrefix, name, active, createdAt.Format(time.RFC3339Nano))
	return err
}

// --- Usage Log Operations ---

// LogUsage records a request
func (d *Database) LogUsage(log *UsageLog) error {
	if log.ID == "" {
		log.ID = uuid.New().String()
	}
	log.CreatedAt = time.Now().UTC()

	_, err := d.db.Exec(`
		INSERT INTO usage_logs (id, api_key_id, account_id, account_email, provider, model, tokens_in, tokens_out, status_code, latency_ms, error, request_body, response_body, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		log.ID, log.APIKeyID, log.AccountID, log.AccountEmail, log.Provider, log.Model,
		log.TokensIn, log.TokensOut, log.StatusCode, log.LatencyMs, log.Error,
		log.RequestBody, log.ResponseBody,
		log.CreatedAt.Format(time.RFC3339Nano))
	return err
}

// --- Helpers ---

func timePtr(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return t.Format(time.RFC3339Nano)
}

// --- Settings ---

// GetSetting returns a setting value or the default
func (d *Database) GetSetting(key string, defaultVal string) string {
	var value string
	err := d.db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&value)
	if err != nil {
		return defaultVal
	}
	return value
}

// SetSetting creates or updates a setting
func (d *Database) SetSetting(key string, value string) error {
	_, err := d.db.Exec(
		"INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		key, value)
	return err
}

// EnsureInternalTestKey ensures an internal test API key exists for
// self-testing (used by /api/models/test). Returns the raw key.
//
// Defensive: the value cached in settings can become stale when the
// underlying api_keys row is hard-deleted or rotated (e.g. an old
// dashboard wiped all keys, or the key prefix scheme changed between
// LIAM versions). We re-validate against the api_keys table on every
// call and regenerate on miss so /api/models/test never starts hitting
// 401s on its own proxy.
func (d *Database) EnsureInternalTestKey() (string, error) {
	if existing := d.GetSetting("internal_test_key", ""); existing != "" {
		if _, err := d.ValidateAPIKey(existing); err == nil {
			return existing, nil
		}
		// Cached key no longer matches a live row — fall through and
		// recreate. The orphaned settings entry is overwritten below.
	}

	_, rawKey, err := d.CreateAPIKey("_internal_test")
	if err != nil {
		return "", err
	}

	if err := d.SetSetting("internal_test_key", rawKey); err != nil {
		return "", err
	}

	return rawKey, nil
}

// --- Usage Queries (for dashboard) ---

// GetRecentUsage returns last N usage logs
func (d *Database) GetRecentUsage(limit int) ([]UsageLog, error) {
	rows, err := d.db.Query(`
		SELECT id, api_key_id, account_id, account_email, provider, model, tokens_in, tokens_out, status_code, latency_ms, error, created_at
		FROM usage_logs ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []UsageLog
	for rows.Next() {
		var l UsageLog
		var createdAt string
		var errStr, apiKeyID, accountID, accountEmail sql.NullString
		err := rows.Scan(&l.ID, &apiKeyID, &accountID, &accountEmail, &l.Provider, &l.Model,
			&l.TokensIn, &l.TokensOut, &l.StatusCode, &l.LatencyMs, &errStr, &createdAt)
		if err != nil {
			continue
		}
		if apiKeyID.Valid {
			l.APIKeyID = apiKeyID.String
		}
		if accountID.Valid {
			l.AccountID = accountID.String
		}
		if accountEmail.Valid {
			l.AccountEmail = accountEmail.String
		}
		if errStr.Valid {
			l.Error = errStr.String
		}
		l.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		logs = append(logs, l)
	}
	return logs, nil
}

// GetUsageDetail returns a single usage log with full request/response body
func (d *Database) GetUsageDetail(id string) (*UsageLog, error) {
	var l UsageLog
	var createdAt string
	var errStr, apiKeyID, accountID, accountEmail, reqBody, respBody sql.NullString

	err := d.db.QueryRow(`
		SELECT id, api_key_id, account_id, account_email, provider, model, tokens_in, tokens_out, 
			status_code, latency_ms, error, request_body, response_body, created_at
		FROM usage_logs WHERE id = ?`, id).Scan(
		&l.ID, &apiKeyID, &accountID, &accountEmail, &l.Provider, &l.Model,
		&l.TokensIn, &l.TokensOut, &l.StatusCode, &l.LatencyMs, &errStr,
		&reqBody, &respBody, &createdAt)
	if err != nil {
		return nil, err
	}

	if apiKeyID.Valid {
		l.APIKeyID = apiKeyID.String
	}
	if accountID.Valid {
		l.AccountID = accountID.String
	}
	if accountEmail.Valid {
		l.AccountEmail = accountEmail.String
	}
	if errStr.Valid {
		l.Error = errStr.String
	}
	if reqBody.Valid {
		l.RequestBody = reqBody.String
	}
	if respBody.Valid {
		l.ResponseBody = respBody.String
	}
	l.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return &l, nil
}

// CleanOldLogs deletes usage logs older than the given duration
func (d *Database) CleanOldLogs(olderThan time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-olderThan).Format(time.RFC3339Nano)
	result, err := d.db.Exec("DELETE FROM usage_logs WHERE created_at < ?", cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// UsageStats summarises usage for a window. We expose input/output token
// counts and an estimated cost separately because the dashboard renders
// them as distinct cards (matching what users expect from 9router and
// other proxy dashboards).
type UsageStats struct {
	TotalRequests     int     `json:"total_requests"`
	TotalInputTokens  int     `json:"total_input_tokens"`
	TotalOutputTokens int     `json:"total_output_tokens"`
	TotalTokens       int     `json:"total_tokens"`
	EstimatedCost     float64 `json:"estimated_cost"`
	AvgLatencyMs      int     `json:"avg_latency_ms"`
	SuccessRate       float64 `json:"success_rate"`
	ErrorCount        int     `json:"error_count"`
	Period            string  `json:"period"`
}

// resolveUsagePeriod converts a human period token (today/24h/7d/30d/60d)
// into a SQL-friendly RFC3339 lower bound. Unknown values fall back to
// "today" so the dashboard never receives an empty payload.
func resolveUsagePeriod(period string) (string, string) {
	now := time.Now().UTC()
	period = strings.ToLower(strings.TrimSpace(period))
	if period == "" {
		period = "today"
	}
	switch period {
	case "today":
		return period, now.Format("2006-01-02") + "T00:00:00Z"
	case "24h":
		return period, now.Add(-24 * time.Hour).Format(time.RFC3339)
	case "7d":
		return period, now.Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	case "30d":
		return period, now.Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	case "60d":
		return period, now.Add(-60 * 24 * time.Hour).Format(time.RFC3339)
	default:
		return "today", now.Format("2006-01-02") + "T00:00:00Z"
	}
}

// GetUsageStats returns aggregated stats for the requested period.
// `period` accepts: today (default), 24h, 7d, 30d, 60d.
func (d *Database) GetUsageStats(period string) (*UsageStats, error) {
	resolvedPeriod, since := resolveUsagePeriod(period)

	stats := &UsageStats{Period: resolvedPeriod}
	var avgLat sql.NullFloat64
	var totalReqs, successReqs int

	err := d.db.QueryRow(`
		SELECT COUNT(*),
			COALESCE(SUM(tokens_in), 0),
			COALESCE(SUM(tokens_out), 0),
			AVG(latency_ms),
			SUM(CASE WHEN status_code >= 200 AND status_code < 400 THEN 1 ELSE 0 END)
		FROM usage_logs WHERE created_at >= ?`, since).Scan(
		&totalReqs, &stats.TotalInputTokens, &stats.TotalOutputTokens, &avgLat, &successReqs)
	if err != nil {
		return stats, nil
	}

	stats.TotalRequests = totalReqs
	stats.TotalTokens = stats.TotalInputTokens + stats.TotalOutputTokens
	if avgLat.Valid {
		stats.AvgLatencyMs = int(avgLat.Float64)
	}
	if totalReqs > 0 {
		stats.SuccessRate = float64(successReqs) / float64(totalReqs) * 100
	}
	stats.ErrorCount = totalReqs - successReqs

	// Per-model cost rollup. We average the per-1k-token prices for
	// input + output across the canonical Claude/Gemini SKUs that LIAM
	// proxies — see modelPricing below. Models not in the table are
	// treated as free so the estimate doesn't double-count unknown
	// custom models.
	rows, err := d.db.Query(`
		SELECT model, COALESCE(SUM(tokens_in), 0), COALESCE(SUM(tokens_out), 0)
		FROM usage_logs WHERE created_at >= ?
		GROUP BY model`, since)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var model string
			var in, out int
			if err := rows.Scan(&model, &in, &out); err != nil {
				continue
			}
			pIn, pOut := modelPricing(model)
			stats.EstimatedCost += float64(in)/1000.0*pIn + float64(out)/1000.0*pOut
		}
	}

	return stats, nil
}

// modelPricing returns (input $ / 1k tokens, output $ / 1k tokens) for a
// known model id. Numbers are an approximation of public Anthropic and
// Google list prices at the time of writing — they're displayed in the
// dashboard with a "Estimated, not actual billing" disclaimer.
func modelPricing(model string) (float64, float64) {
	// Strip provider prefix (kr/, ag/, kiro/) so we only care about the
	// SKU name itself.
	if idx := strings.Index(model, "/"); idx >= 0 {
		model = model[idx+1:]
	}
	model = strings.ToLower(model)
	switch {
	case strings.Contains(model, "opus-4.7") || strings.Contains(model, "opus-4-7"):
		return 0.015, 0.075 // Anthropic Opus pricing
	case strings.Contains(model, "opus-4.6") || strings.Contains(model, "opus-4-6"):
		return 0.015, 0.075
	case strings.Contains(model, "sonnet-4.6") || strings.Contains(model, "sonnet-4-6"):
		return 0.003, 0.015 // Anthropic Sonnet pricing
	case strings.Contains(model, "sonnet-4.5") || strings.Contains(model, "sonnet-4-5"):
		return 0.003, 0.015
	case strings.Contains(model, "haiku-4.5") || strings.Contains(model, "haiku-4-5"):
		return 0.0008, 0.004 // Anthropic Haiku pricing
	case strings.Contains(model, "gemini-3.1-pro") || strings.Contains(model, "gemini-3-pro"):
		return 0.00125, 0.005
	case strings.Contains(model, "gemini-3-flash") || strings.Contains(model, "gemini-3.1-flash"):
		return 0.000075, 0.0003
	case strings.Contains(model, "gpt-oss-120b"):
		return 0.0006, 0.0024
	case strings.Contains(model, "deepseek"):
		return 0.00027, 0.0011
	case strings.Contains(model, "qwen"):
		return 0.0004, 0.002
	case strings.Contains(model, "glm-5"):
		return 0.0003, 0.0015
	case strings.Contains(model, "minimax"):
		return 0.0002, 0.0011
	}
	return 0, 0
}

// ChartBucket holds data for one time bucket
type ChartBucket struct {
	Time      string `json:"time"`
	Requests  int    `json:"requests"`
	Tokens    int    `json:"tokens"`
	InTokens  int    `json:"in_tokens"`
	OutTokens int    `json:"out_tokens"`
}

// GetUsageChart returns time-series data sized to the requested period.
// `period` matches GetUsageStats: today / 24h / 7d / 30d / 60d.
//
// We pick the bucket granularity automatically:
//   - today, 24h     → 24 hourly buckets
//   - 7d             → 7 daily buckets
//   - 30d            → 30 daily buckets
//   - 60d            → 60 daily buckets
//
// One SQL query (with strftime grouping) populates the buckets in O(N)
// instead of N round trips.
func (d *Database) GetUsageChart(period string) ([]ChartBucket, error) {
	resolved, _ := resolveUsagePeriod(period)
	now := time.Now().UTC()

	type window struct {
		count    int
		key      func(t time.Time) string
		fmt      string // strftime format used to bucket rows in SQL
		labelFmt string // human-readable label per bucket
		stride   time.Duration
	}

	var w window
	switch resolved {
	case "today", "24h":
		w = window{count: 24, fmt: "%Y-%m-%d %H", labelFmt: "15:00", stride: time.Hour,
			key: func(t time.Time) string { return t.Format("2006-01-02 15") }}
	case "7d":
		w = window{count: 7, fmt: "%Y-%m-%d", labelFmt: "Mon 02", stride: 24 * time.Hour,
			key: func(t time.Time) string { return t.Format("2006-01-02") }}
	case "30d":
		w = window{count: 30, fmt: "%Y-%m-%d", labelFmt: "Jan 02", stride: 24 * time.Hour,
			key: func(t time.Time) string { return t.Format("2006-01-02") }}
	case "60d":
		w = window{count: 60, fmt: "%Y-%m-%d", labelFmt: "Jan 02", stride: 24 * time.Hour,
			key: func(t time.Time) string { return t.Format("2006-01-02") }}
	default:
		w = window{count: 24, fmt: "%Y-%m-%d %H", labelFmt: "15:00", stride: time.Hour,
			key: func(t time.Time) string { return t.Format("2006-01-02 15") }}
	}

	// Pre-allocate buckets so empty periods still render nicely.
	buckets := make([]ChartBucket, w.count)
	keyToIdx := make(map[string]int, w.count)
	for i := 0; i < w.count; i++ {
		t := now.Add(-time.Duration(w.count-1-i) * w.stride)
		buckets[i] = ChartBucket{Time: t.Format(w.labelFmt)}
		keyToIdx[w.key(t)] = i
	}

	startKey := now.Add(-time.Duration(w.count-1) * w.stride)
	rows, err := d.db.Query(`
		SELECT strftime(?, created_at) AS bucket,
			COUNT(*),
			COALESCE(SUM(tokens_in), 0),
			COALESCE(SUM(tokens_out), 0)
		FROM usage_logs
		WHERE created_at >= ?
		GROUP BY bucket
		ORDER BY bucket ASC`, w.fmt, startKey.Format(time.RFC3339))
	if err != nil {
		return buckets, nil
	}
	defer rows.Close()

	for rows.Next() {
		var bucket string
		var reqs, in, out int
		if err := rows.Scan(&bucket, &reqs, &in, &out); err != nil {
			continue
		}
		idx, ok := keyToIdx[bucket]
		if !ok {
			continue
		}
		buckets[idx].Requests = reqs
		buckets[idx].InTokens = in
		buckets[idx].OutTokens = out
		buckets[idx].Tokens = in + out
	}

	return buckets, nil
}

// TopModelStat is a compact summary of one model's traffic for use on the
// dashboard's overview page.
type TopModelStat struct {
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Requests int    `json:"requests"`
	Tokens   int    `json:"tokens"`
}

// GetTopModels returns the most-used models within the requested period
// (default "today"). Ordered by request count, capped at `limit`.
func (d *Database) GetTopModels(period string, limit int) ([]TopModelStat, error) {
	if limit <= 0 {
		limit = 5
	}
	_, since := resolveUsagePeriod(period)
	rows, err := d.db.Query(`
		SELECT model, provider, COUNT(*) AS reqs,
			COALESCE(SUM(tokens_in + tokens_out), 0) AS tokens
		FROM usage_logs
		WHERE created_at >= ? AND model != ''
		GROUP BY model, provider
		ORDER BY reqs DESC
		LIMIT ?`, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TopModelStat
	for rows.Next() {
		var stat TopModelStat
		if err := rows.Scan(&stat.Model, &stat.Provider, &stat.Requests, &stat.Tokens); err != nil {
			continue
		}
		out = append(out, stat)
	}
	return out, nil
}

// RecentErrorEntry surfaces the most recent failed requests so the
// overview page can call them out without forcing the user to dig
// through the full Usage table.
type RecentErrorEntry struct {
	ID         string `json:"id"`
	CreatedAt  string `json:"created_at"`
	Model      string `json:"model"`
	Provider   string `json:"provider"`
	Account    string `json:"account_email"`
	StatusCode int    `json:"status_code"`
	LatencyMs  int    `json:"latency_ms"`
	Error      string `json:"error,omitempty"`
}

// GetRecentErrors returns up to `limit` failed requests from the last
// 24h. We hard-bound the lookback to avoid showing stale failures from
// last week on the overview page.
func (d *Database) GetRecentErrors(limit int) ([]RecentErrorEntry, error) {
	if limit <= 0 {
		limit = 5
	}
	since := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	rows, err := d.db.Query(`
		SELECT id, created_at, model, provider, COALESCE(account_email, ''),
			status_code, latency_ms, COALESCE(error, '')
		FROM usage_logs
		WHERE created_at >= ?
			AND (status_code >= 400 OR error != '')
		ORDER BY created_at DESC
		LIMIT ?`, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RecentErrorEntry
	for rows.Next() {
		var e RecentErrorEntry
		if err := rows.Scan(&e.ID, &e.CreatedAt, &e.Model, &e.Provider, &e.Account,
			&e.StatusCode, &e.LatencyMs, &e.Error); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// CountAccountsInBackoff returns how many accounts currently have an
// active per-account cooldown (cooldown_until in the future). Used as
// a quick health signal on the dashboard overview page; replaces the
// older CountActiveModelLocks now that per-(account, model) locks are
// gone.
func (d *Database) CountAccountsInBackoff() (int, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var count int
	err := d.db.QueryRow(`
		SELECT COUNT(*) FROM accounts
		WHERE cooldown_until IS NOT NULL
			AND cooldown_until > ?`, now).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// --- Combo Operations ---

// ListCombos returns all combos
func (d *Database) ListCombos() ([]Combo, error) {
	rows, err := d.db.Query("SELECT id, name, models, strategy, sticky_limit, created_at, updated_at FROM combos ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var combos []Combo
	for rows.Next() {
		var c Combo
		var modelsJSON, createdAt, updatedAt string
		err := rows.Scan(&c.ID, &c.Name, &modelsJSON, &c.Strategy, &c.StickyLimit, &createdAt, &updatedAt)
		if err != nil {
			continue
		}
		json.Unmarshal([]byte(modelsJSON), &c.Models)
		c.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		c.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		combos = append(combos, c)
	}
	return combos, nil
}

// GetCombo returns a combo by name
func (d *Database) GetCombo(name string) (*Combo, error) {
	var c Combo
	var modelsJSON, createdAt, updatedAt string
	err := d.db.QueryRow("SELECT id, name, models, strategy, sticky_limit, created_at, updated_at FROM combos WHERE name = ?", name).Scan(
		&c.ID, &c.Name, &modelsJSON, &c.Strategy, &c.StickyLimit, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(modelsJSON), &c.Models)
	c.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	c.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &c, nil
}

// CreateCombo creates a new combo
func (d *Database) CreateCombo(name, strategy string, models []string, stickyLimit int) (*Combo, error) {
	id := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	modelsJSON, _ := json.Marshal(models)
	if strategy == "" {
		strategy = "fallback"
	}
	if stickyLimit <= 0 {
		stickyLimit = 1
	}

	_, err := d.db.Exec(`INSERT INTO combos (id, name, models, strategy, sticky_limit, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, name, string(modelsJSON), strategy, stickyLimit, now, now)
	if err != nil {
		return nil, err
	}
	return d.GetCombo(name)
}

// UpdateCombo updates an existing combo
func (d *Database) UpdateCombo(id string, name, strategy string, models []string, stickyLimit int) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	modelsJSON, _ := json.Marshal(models)
	_, err := d.db.Exec(`UPDATE combos SET name=?, models=?, strategy=?, sticky_limit=?, updated_at=? WHERE id=?`,
		name, string(modelsJSON), strategy, stickyLimit, now, id)
	return err
}

// DeleteCombo deletes a combo
func (d *Database) DeleteCombo(id string) error {
	_, err := d.db.Exec("DELETE FROM combos WHERE id = ?", id)
	return err
}

// --- Account Priority & Model Lock ---

// UpdateAccountPriority sets priority for an account
func (d *Database) UpdateAccountPriority(id string, priority int) error {
	_, err := d.db.Exec("UPDATE accounts SET priority = ? WHERE id = ?", priority, id)
	return err
}

// ReorderAccounts sets priorities based on ordered list of IDs
func (d *Database) ReorderAccounts(ids []string) error {
	for i, id := range ids {
		d.db.Exec("UPDATE accounts SET priority = ? WHERE id = ?", i+1, id)
	}
	return nil
}

// SetModelLock and GetModelLocks were removed in favour of per-account
// cooldown_until + backoff_level. The model_locks column is preserved
// in the schema for backward compatibility with older binaries but is
// no longer read or written.

// UpdateConsecutiveUseCount updates the consecutive use count for an account
func (d *Database) UpdateConsecutiveUseCount(id string, count int) error {
	_, err := d.db.Exec("UPDATE accounts SET consecutive_use_count = ? WHERE id = ?", count, id)
	return err
}

// GetAccountWithDetails returns an account with priority and consecutive_use_count
func (d *Database) GetAccountWithDetails(id string) (priority int, consecutiveUseCount int, err error) {
	err = d.db.QueryRow("SELECT COALESCE(priority, 0), COALESCE(consecutive_use_count, 0) FROM accounts WHERE id = ?", id).Scan(&priority, &consecutiveUseCount)
	return
}

// UpdateAccountEmail updates just the email/name field of an account
func (d *Database) UpdateAccountEmail(id, email string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := d.db.Exec("UPDATE accounts SET email = ?, updated_at = ? WHERE id = ?", email, now, id)
	return err
}
