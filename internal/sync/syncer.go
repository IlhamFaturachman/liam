package sync

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	gosync "sync"
	"time"

	"github.com/liam-auto/liam/internal/db"
)

// Syncer handles bidirectional sync between local SQLite and Supabase
type Syncer struct {
	client   *Client
	database *db.Database
	lastSync time.Time
	enabled  bool

	// tombstones records accounts the user just deleted locally so the
	// next pull doesn't resurrect them while the remote DELETE is still
	// propagating (or while the sync worker pulls before pushing). The
	// map key is the account ID and the value is the wallclock time the
	// tombstone was created. Entries older than tombstoneTTL are GC'd.
	tombMu     gosync.Mutex
	tombstones map[string]time.Time
}

const tombstoneTTL = 10 * time.Minute

// NewSyncer creates a new syncer
func NewSyncer(client *Client, database *db.Database) *Syncer {
	return &Syncer{
		client:     client,
		database:   database,
		enabled:    client.IsConfigured(),
		tombstones: make(map[string]time.Time),
	}
}

// IsEnabled returns true if sync is configured and active
func (s *Syncer) IsEnabled() bool {
	return s.enabled
}

// LastSyncTime returns when the last successful sync happened
func (s *Syncer) LastSyncTime() time.Time {
	return s.lastSync
}

// InitialSync runs a full pull from Supabase on startup
func (s *Syncer) InitialSync() error {
	if !s.enabled {
		return nil
	}

	log.Println("[SYNC] Running initial sync from Supabase...")

	if err := s.PullAccounts(); err != nil {
		return fmt.Errorf("pull accounts: %w", err)
	}
	if err := s.PullAPIKeys(); err != nil {
		return fmt.Errorf("pull keys: %w", err)
	}
	if err := s.PullCustomModels(); err != nil {
		return fmt.Errorf("pull models: %w", err)
	}

	s.lastSync = time.Now()
	log.Println("[SYNC] Initial sync complete")
	return nil
}

// Sync runs a bidirectional sync cycle (pull remote changes, push local changes)
func (s *Syncer) Sync() error {
	if !s.enabled {
		return nil
	}

	// Pull remote → merge locally
	if err := s.PullAccounts(); err != nil {
		log.Printf("[SYNC] Pull accounts error: %v", err)
	}
	if err := s.PullAPIKeys(); err != nil {
		log.Printf("[SYNC] Pull keys error: %v", err)
	}

	// Push local → remote
	if err := s.PushAccounts(); err != nil {
		log.Printf("[SYNC] Push accounts error: %v", err)
	}
	if err := s.PushAPIKeys(); err != nil {
		log.Printf("[SYNC] Push keys error: %v", err)
	}

	s.lastSync = time.Now()
	return nil
}

// --- Pull (Remote → Local) ---

// PullAccounts fetches accounts from Supabase and merges into local DB
func (s *Syncer) PullAccounts() error {
	rows, err := s.client.List("accounts", nil)
	if err != nil {
		return err
	}

	// Load all local accounts into map ONCE (O(n) instead of O(n²))
	localAccounts, _ := s.database.ListAccounts("")
	localMap := make(map[string]*db.Account, len(localAccounts))
	for i := range localAccounts {
		localMap[localAccounts[i].ID] = &localAccounts[i]
	}

	for _, row := range rows {
		remoteUpdated := parseTime(row["updated_at"])
		id, _ := row["id"].(string)
		if id == "" {
			continue
		}

		// Skip rows that the user just deleted locally — without this
		// the next pull would resurrect the account before the remote
		// DELETE has had a chance to propagate.
		if s.isTombstoned(id) {
			continue
		}

		// Check if local version is newer
		localAccount := localMap[id]

		// If local is newer, skip (will be pushed later)
		if localAccount != nil && localAccount.UpdatedAt.After(remoteUpdated) {
			continue
		}

		// Merge remote into local
		credsJSON, _ := json.Marshal(row["credentials"])
		metaJSON, _ := json.Marshal(row["metadata"])

		account := &db.Account{
			ID:                id,
			Provider:          strVal(row["provider"]),
			Email:             strVal(row["email"]),
			Status:            strVal(row["status"]),
			Credentials:       credsJSON,
			QuotaTotal:        intVal(row["quota_total"]),
			QuotaRemaining:    intVal(row["quota_remaining"]),
			ConsecutiveErrors: intVal(row["consecutive_errors"]),
			LastError:         strVal(row["last_error"]),
			Metadata:          metaJSON,
		}

		s.database.UpsertAccount(account)
	}

	return nil
}

// PullAPIKeys fetches API keys from Supabase and merges into local DB
func (s *Syncer) PullAPIKeys() error {
	rows, err := s.client.List("api_keys", nil)
	if err != nil {
		return err
	}

	for _, row := range rows {
		id, _ := row["id"].(string)
		if id == "" {
			continue
		}

		// Check if key already exists locally
		localKeys, _ := s.database.ListAPIKeys()
		exists := false
		for _, k := range localKeys {
			if k.ID == id {
				exists = true
				break
			}
		}
		if exists {
			continue // Don't overwrite local keys
		}

		// Insert remote key into local DB
		keyHash := strVal(row["key_hash"])
		keyPrefix := strVal(row["key_prefix"])
		name := strVal(row["name"])
		isActive := boolVal(row["is_active"])
		createdAt := parseTime(row["created_at"])

		s.database.ImportAPIKey(id, keyHash, keyPrefix, name, isActive, createdAt)
	}

	return nil
}

// PullCustomModels fetches custom models from Supabase
func (s *Syncer) PullCustomModels() error {
	rows, err := s.client.List("model_registry", map[string]string{
		"is_custom": "eq.true",
	})
	if err != nil {
		return err
	}

	for _, row := range rows {
		id, _ := row["id"].(string)
		if id == "" {
			continue
		}

		// Upsert into local model registry via raw SQL
		providerAlias := strVal(row["provider_alias"])
		modelID := strVal(row["model_id"])
		displayName := strVal(row["display_name"])
		modelType := strVal(row["type"])
		if modelType == "" {
			modelType = "llm"
		}

		metaJSON, _ := json.Marshal(row["metadata"])
		var metadata map[string]interface{}
		json.Unmarshal(metaJSON, &metadata)

		conn := s.database.Conn()
		now := time.Now().UTC().Format(time.RFC3339Nano)
		conn.Exec(`
			INSERT OR IGNORE INTO model_registry (id, provider_alias, model_id, display_name, type, is_custom, is_enabled, metadata, created_at)
			VALUES (?, ?, ?, ?, ?, 1, 1, ?, ?)`,
			id, providerAlias, modelID, displayName, modelType, string(metaJSON), now)
	}

	return nil
}

// --- Push (Local → Remote) ---

// safeUnmarshalJSON parses raw JSON bytes into a map. If the input is empty
// or invalid, it returns an empty map so Supabase NOT NULL columns never get
// a null value pushed.
func safeUnmarshalJSON(raw json.RawMessage) map[string]interface{} {
	out := map[string]interface{}{}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return out
	}
	_ = json.Unmarshal([]byte(trimmed), &out)
	return out
}

// PushAccounts pushes local accounts to Supabase
func (s *Syncer) PushAccounts() error {
	accounts, err := s.database.ListAccounts("")
	if err != nil {
		return err
	}

	if len(accounts) == 0 {
		return nil
	}

	rows := make([]map[string]interface{}, 0, len(accounts))
	for _, a := range accounts {
		creds := safeUnmarshalJSON(a.Credentials)
		meta := safeUnmarshalJSON(a.Metadata)

		rows = append(rows, map[string]interface{}{
			"id":                 a.ID,
			"provider":           a.Provider,
			"email":              a.Email,
			"status":             a.Status,
			"credentials":        creds,
			"quota_total":        a.QuotaTotal,
			"quota_remaining":    a.QuotaRemaining,
			"consecutive_errors": a.ConsecutiveErrors,
			"last_error":         a.LastError,
			"metadata":           meta,
			"updated_at":         time.Now().UTC().Format(time.RFC3339),
		})
	}

	return s.client.Upsert("accounts", rows)
}

// PushAPIKeys pushes local API keys to Supabase
func (s *Syncer) PushAPIKeys() error {
	keys, err := s.database.ListAPIKeys()
	if err != nil {
		return err
	}

	if len(keys) == 0 {
		return nil
	}

	rows := make([]map[string]interface{}, 0, len(keys))
	for _, k := range keys {
		// Skip internal test key and keys with empty hash
		if k.KeyHash == "" || k.Name == "_internal_test" {
			continue
		}
		rows = append(rows, map[string]interface{}{
			"id":             k.ID,
			"key_hash":       k.KeyHash,
			"key_prefix":     k.KeyPrefix,
			"name":           k.Name,
			"is_active":      k.IsActive,
			"rate_limit_rpm": k.RateLimitRPM,
			"rate_limit_rpd": k.RateLimitRPD,
			"total_requests": k.TotalRequests,
			"created_at":     k.CreatedAt.Format(time.RFC3339),
		})
	}

	if len(rows) == 0 {
		return nil
	}

	return s.client.Upsert("api_keys", rows)
}

// PushAccount pushes a single account to Supabase (called after mutations)
func (s *Syncer) PushAccount(account *db.Account) {
	if !s.enabled || account == nil {
		return
	}

	creds := safeUnmarshalJSON(account.Credentials)
	meta := safeUnmarshalJSON(account.Metadata)

	row := map[string]interface{}{
		"id":                 account.ID,
		"provider":           account.Provider,
		"email":              account.Email,
		"status":             account.Status,
		"credentials":        creds,
		"quota_total":        account.QuotaTotal,
		"quota_remaining":    account.QuotaRemaining,
		"consecutive_errors": account.ConsecutiveErrors,
		"last_error":         account.LastError,
		"metadata":           meta,
		"updated_at":         time.Now().UTC().Format(time.RFC3339),
	}

	if err := s.client.UpsertOne("accounts", row); err != nil {
		log.Printf("[SYNC] Push account %s error: %v", account.Email, err)
	}
}

// PushCustomModel pushes a single custom model row upstream. Mirrors the
// behaviour of PushAccount: best-effort, fire-and-forget so handlers stay
// fast and a Supabase outage doesn't block dashboard interactions.
func (s *Syncer) PushCustomModel(id, providerAlias, modelID, displayName, modelType string, isEnabled bool, metadata map[string]interface{}) {
	if !s.enabled || strings.TrimSpace(id) == "" {
		return
	}
	if modelType == "" {
		modelType = "llm"
	}
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	row := map[string]interface{}{
		"id":             id,
		"provider_alias": providerAlias,
		"model_id":       modelID,
		"display_name":   displayName,
		"type":           modelType,
		"is_custom":      true,
		"is_enabled":     isEnabled,
		"metadata":       metadata,
		"created_at":     time.Now().UTC().Format(time.RFC3339),
	}
	if err := s.client.UpsertOne("model_registry", row); err != nil {
		log.Printf("[SYNC] Push custom model %s error: %v", id, err)
	}
}

// PushCustomModelAsync wraps PushCustomModel in a goroutine so HTTP handlers
// can return immediately.
func (s *Syncer) PushCustomModelAsync(id, providerAlias, modelID, displayName, modelType string, isEnabled bool, metadata map[string]interface{}) {
	if !s.enabled {
		return
	}
	go s.PushCustomModel(id, providerAlias, modelID, displayName, modelType, isEnabled, metadata)
}

// DeleteCustomModel removes a custom model row from Supabase by id.
func (s *Syncer) DeleteCustomModel(id string) {
	id = strings.TrimSpace(id)
	if id == "" || !s.enabled {
		return
	}
	if err := s.client.Delete("model_registry", "id=eq."+id); err != nil {
		log.Printf("[SYNC] Delete custom model %s error: %v", id, err)
	}
}

// DeleteCustomModelAsync mirrors DeleteCustomModel for goroutine fire-and-forget.
func (s *Syncer) DeleteCustomModelAsync(id string) {
	if strings.TrimSpace(id) == "" || !s.enabled {
		return
	}
	go s.DeleteCustomModel(id)
}

// DeleteAccount removes an account from Supabase and records a tombstone so
// the next pull doesn't resurrect it. Safe to call even when sync is
// disabled — it then just records the tombstone for an eventual reconnect.
func (s *Syncer) DeleteAccount(id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	s.markTombstone(id)
	if !s.enabled {
		return
	}
	// PostgREST filter syntax: ?id=eq.<uuid>
	if err := s.client.Delete("accounts", "id=eq."+id); err != nil {
		log.Printf("[SYNC] Delete account %s error: %v", id, err)
	}
}

// PushAccountAsync runs PushAccount in a goroutine so HTTP handlers can
// return immediately without blocking the response on a Supabase round trip.
func (s *Syncer) PushAccountAsync(account *db.Account) {
	if !s.enabled || account == nil {
		return
	}
	go s.PushAccount(account)
}

// DeleteAccountAsync mirrors PushAccountAsync for deletes.
func (s *Syncer) DeleteAccountAsync(id string) {
	if strings.TrimSpace(id) == "" {
		return
	}
	go s.DeleteAccount(id)
}

// --- Tombstone helpers ---

func (s *Syncer) markTombstone(id string) {
	s.tombMu.Lock()
	defer s.tombMu.Unlock()
	if s.tombstones == nil {
		s.tombstones = make(map[string]time.Time)
	}
	s.tombstones[id] = time.Now().Add(tombstoneTTL)
	s.gcTombstonesLocked()
}

func (s *Syncer) isTombstoned(id string) bool {
	s.tombMu.Lock()
	defer s.tombMu.Unlock()
	if s.tombstones == nil {
		return false
	}
	exp, ok := s.tombstones[id]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.tombstones, id)
		return false
	}
	return true
}

// gcTombstonesLocked drops expired entries. The caller must hold tombMu.
func (s *Syncer) gcTombstonesLocked() {
	now := time.Now()
	for id, exp := range s.tombstones {
		if now.After(exp) {
			delete(s.tombstones, id)
		}
	}
}

// --- Status ---

// Status returns current sync status for dashboard
func (s *Syncer) Status() map[string]interface{} {
	status := map[string]interface{}{
		"enabled":      s.enabled,
		"connected":    false,
		"last_sync":    "",
		"supabase_url": "",
	}

	if !s.enabled {
		return status
	}

	status["supabase_url"] = s.client.baseURL
	if !s.lastSync.IsZero() {
		status["last_sync"] = s.lastSync.Format(time.RFC3339)
		status["connected"] = true
	}

	return status
}

// --- Helpers ---

func strVal(v interface{}) string {
	if v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

func intVal(v interface{}) int {
	if v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

func boolVal(v interface{}) bool {
	if v == nil {
		return false
	}
	b, _ := v.(bool)
	return b
}

func parseTime(v interface{}) time.Time {
	if v == nil {
		return time.Time{}
	}
	s, _ := v.(string)
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t, _ = time.Parse(time.RFC3339Nano, s)
	}
	return t
}
