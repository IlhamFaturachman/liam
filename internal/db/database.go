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
	ConsecutiveErrors int             `json:"consecutive_errors"`
	LastError         string          `json:"last_error,omitempty"`
	CooldownUntil     *time.Time      `json:"cooldown_until,omitempty"`
	LastUsedAt        *time.Time      `json:"last_used_at,omitempty"`
	LastLoginAt       *time.Time      `json:"last_login_at,omitempty"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
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
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Models     []string `json:"models"`
	Strategy   string   `json:"strategy"`    // "fallback" | "round-robin"
	StickyLimit int     `json:"sticky_limit"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// APIKey represents a consumer API key
type APIKey struct {
	ID        string    `json:"id"`
	KeyHash   string    `json:"key_hash"`
	KeyPrefix string    `json:"key_prefix"`
	Name      string    `json:"name"`
	IsActive  bool      `json:"is_active"`
	RateLimitRPM int    `json:"rate_limit_rpm"`
	RateLimitRPD int    `json:"rate_limit_rpd"`
	TotalRequests int64 `json:"total_requests"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
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
	}
	for _, stmt := range alterStatements {
		d.db.Exec(stmt) // Ignore errors (column already exists)
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

	_, err := d.db.Exec(`
		INSERT INTO accounts (id, provider, email, status, credentials, quota_total, quota_remaining, 
			quota_reset_at, consecutive_errors, last_error, cooldown_until, last_used_at, last_login_at, metadata, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider, email) DO UPDATE SET
			status=excluded.status, credentials=excluded.credentials, 
			quota_total=excluded.quota_total, quota_remaining=excluded.quota_remaining,
			quota_reset_at=excluded.quota_reset_at, consecutive_errors=excluded.consecutive_errors,
			last_error=excluded.last_error, cooldown_until=excluded.cooldown_until,
			last_used_at=excluded.last_used_at, last_login_at=excluded.last_login_at,
			metadata=excluded.metadata, updated_at=excluded.updated_at`,
		a.ID, a.Provider, a.Email, a.Status, string(a.Credentials),
		a.QuotaTotal, a.QuotaRemaining, timePtr(a.QuotaResetAt),
		a.ConsecutiveErrors, a.LastError, timePtr(a.CooldownUntil),
		timePtr(a.LastUsedAt), timePtr(a.LastLoginAt), string(a.Metadata),
		now, now,
	)
	return err
}

// ListAccounts returns accounts filtered by provider (empty = all)
func (d *Database) ListAccounts(provider string) ([]Account, error) {
	var rows *sql.Rows
	var err error

	if provider == "" {
		rows, err = d.db.Query("SELECT id, provider, email, status, credentials, quota_total, quota_remaining, consecutive_errors, last_error, created_at, updated_at FROM accounts ORDER BY provider, email")
	} else {
		rows, err = d.db.Query("SELECT id, provider, email, status, credentials, quota_total, quota_remaining, consecutive_errors, last_error, created_at, updated_at FROM accounts WHERE provider = ? ORDER BY email", provider)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var accounts []Account
	for rows.Next() {
		var a Account
		var creds, createdAt, updatedAt string
		var lastErr sql.NullString
		err := rows.Scan(&a.ID, &a.Provider, &a.Email, &a.Status, &creds,
			&a.QuotaTotal, &a.QuotaRemaining, &a.ConsecutiveErrors, &lastErr, &createdAt, &updatedAt)
		if err != nil {
			return nil, err
		}
		a.Credentials = json.RawMessage(creds)
		if lastErr.Valid {
			a.LastError = lastErr.String
		}
		a.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		a.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
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
		var creds string
		var lastUsed, cooldown, createdAt, updatedAt sql.NullString
		err := rows.Scan(&a.ID, &a.Provider, &a.Email, &a.Status, &creds,
			&a.QuotaTotal, &a.QuotaRemaining, &a.ConsecutiveErrors,
			&lastUsed, &cooldown, &createdAt, &updatedAt)
		if err != nil {
			return nil, err
		}
		a.Credentials = json.RawMessage(creds)
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

// MarkAccountError increments error count and optionally sets cooldown
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
			status = CASE WHEN consecutive_errors + 1 >= 10 THEN 'disabled' ELSE status END,
			updated_at = ?
		WHERE id = ?`, errMsg, cooldown, now, id)
	return err
}

// MarkAccountSuccess resets error count
func (d *Database) MarkAccountSuccess(id string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := d.db.Exec(`
		UPDATE accounts SET consecutive_errors = 0, last_error = NULL, status = 'active', updated_at = ?
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
	// We check credentials JSON for expires_at field
	threshold := time.Now().UTC().Add(time.Duration(withinMinutes) * time.Minute).Format(time.RFC3339Nano)

	rows, err := d.db.Query(`
		SELECT id, provider, email, status, credentials, created_at, updated_at
		FROM accounts 
		WHERE provider = ? AND status = 'active'
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
		var creds, createdAt, updatedAt string
		err := rows.Scan(&a.ID, &a.Provider, &a.Email, &a.Status, &creds, &createdAt, &updatedAt)
		if err != nil {
			return nil, err
		}
		a.Credentials = json.RawMessage(creds)
		a.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		accounts = append(accounts, a)
	}
	return accounts, nil
}

// --- API Key Operations ---

// CreateAPIKey generates a new API key and returns the key object + raw key
func (d *Database) CreateAPIKey(name string) (*APIKey, string, error) {
	// Generate random key: li-<32 random bytes hex>
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", err
	}
	rawKey := "li-" + hex.EncodeToString(raw)

	// Hash for storage
	hash := sha256.Sum256([]byte(rawKey))
	keyHash := hex.EncodeToString(hash[:])

	key := &APIKey{
		ID:        uuid.New().String(),
		KeyHash:   keyHash,
		KeyPrefix: rawKey[:16],
		Name:      name,
		IsActive:  true,
		RateLimitRPM: 60,
		RateLimitRPD: 1000,
		CreatedAt: time.Now().UTC(),
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

// EnsureInternalTestKey ensures an internal test API key exists for self-testing
// Returns the raw key (only set once on first boot, then retrieved from settings)
func (d *Database) EnsureInternalTestKey() (string, error) {
	existing := d.GetSetting("internal_test_key", "")
	if existing != "" {
		return existing, nil
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

// UsageStats holds aggregated usage statistics
type UsageStats struct {
	TotalRequests int     `json:"total_requests"`
	TotalTokens   int     `json:"total_tokens"`
	AvgLatencyMs  int     `json:"avg_latency_ms"`
	SuccessRate   float64 `json:"success_rate"`
	ErrorCount    int     `json:"error_count"`
}

// GetUsageStats returns aggregated stats for today
func (d *Database) GetUsageStats() (*UsageStats, error) {
	today := time.Now().UTC().Format("2006-01-02")

	var stats UsageStats
	var avgLat sql.NullFloat64
	var totalReqs, successReqs int

	err := d.db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(tokens_in + tokens_out), 0), AVG(latency_ms),
			SUM(CASE WHEN status_code >= 200 AND status_code < 400 THEN 1 ELSE 0 END)
		FROM usage_logs WHERE created_at >= ?`, today+"T00:00:00Z").Scan(
		&totalReqs, &stats.TotalTokens, &avgLat, &successReqs)
	if err != nil {
		return &UsageStats{}, nil
	}

	stats.TotalRequests = totalReqs
	if avgLat.Valid {
		stats.AvgLatencyMs = int(avgLat.Float64)
	}
	if totalReqs > 0 {
		stats.SuccessRate = float64(successReqs) / float64(totalReqs) * 100
	}
	stats.ErrorCount = totalReqs - successReqs

	return &stats, nil
}

// ChartBucket holds data for one time bucket
type ChartBucket struct {
	Time     string `json:"time"`
	Requests int    `json:"requests"`
	Tokens   int    `json:"tokens"`
}

// GetUsageChart returns hourly bucketed data for the last N hours
func (d *Database) GetUsageChart(hours int) ([]ChartBucket, error) {
	buckets := make([]ChartBucket, hours)
	now := time.Now().UTC()

	for i := hours - 1; i >= 0; i-- {
		t := now.Add(-time.Duration(i) * time.Hour)
		start := t.Format("2006-01-02T15") + ":00:00Z"
		end := t.Format("2006-01-02T15") + ":59:59Z"

		var reqs int
		var tokens int
		d.db.QueryRow(`
			SELECT COUNT(*), COALESCE(SUM(tokens_in + tokens_out), 0)
			FROM usage_logs WHERE created_at >= ? AND created_at <= ?`,
			start, end).Scan(&reqs, &tokens)

		buckets[hours-1-i] = ChartBucket{
			Time:     t.Format("15:00"),
			Requests: reqs,
			Tokens:   tokens,
		}
	}

	return buckets, nil
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

// SetModelLock locks an account for a specific model until a given time
func (d *Database) SetModelLock(accountID, model string, until time.Time) error {
	// Read current locks
	var locksJSON string
	d.db.QueryRow("SELECT model_locks FROM accounts WHERE id = ?", accountID).Scan(&locksJSON)

	locks := map[string]string{}
	if locksJSON != "" {
		json.Unmarshal([]byte(locksJSON), &locks)
	}

	locks[model] = until.Format(time.RFC3339Nano)

	newJSON, _ := json.Marshal(locks)
	_, err := d.db.Exec("UPDATE accounts SET model_locks = ? WHERE id = ?", string(newJSON), accountID)
	return err
}

// GetModelLocks returns current model locks for an account
func (d *Database) GetModelLocks(accountID string) map[string]time.Time {
	var locksJSON string
	d.db.QueryRow("SELECT model_locks FROM accounts WHERE id = ?", accountID).Scan(&locksJSON)

	raw := map[string]string{}
	if locksJSON != "" {
		json.Unmarshal([]byte(locksJSON), &raw)
	}

	result := map[string]time.Time{}
	now := time.Now().UTC()
	for model, untilStr := range raw {
		t, err := time.Parse(time.RFC3339Nano, untilStr)
		if err == nil && t.After(now) {
			result[model] = t
		}
	}
	return result
}

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
