package sync

import (
	"fmt"
	"strings"
)

// SQL schemas for Supabase tables (auto-created on first connect)
var createTableSQL = []string{
	`CREATE TABLE IF NOT EXISTS accounts (
		id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		provider TEXT NOT NULL,
		email TEXT NOT NULL,
		status TEXT DEFAULT 'active',
		credentials JSONB NOT NULL DEFAULT '{}',
		quota_total INTEGER DEFAULT 0,
		quota_remaining INTEGER DEFAULT 0,
		quota_reset_at TIMESTAMPTZ,
		consecutive_errors INTEGER DEFAULT 0,
		last_error TEXT,
		cooldown_until TIMESTAMPTZ,
		last_used_at TIMESTAMPTZ,
		metadata JSONB DEFAULT '{}',
		created_at TIMESTAMPTZ DEFAULT NOW(),
		updated_at TIMESTAMPTZ DEFAULT NOW(),
		UNIQUE(provider, email)
	)`,
	`CREATE TABLE IF NOT EXISTS api_keys (
		id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		key_hash TEXT NOT NULL UNIQUE,
		key_prefix TEXT NOT NULL,
		name TEXT NOT NULL,
		is_active BOOLEAN DEFAULT true,
		rate_limit_rpm INTEGER DEFAULT 60,
		rate_limit_rpd INTEGER DEFAULT 1000,
		total_requests BIGINT DEFAULT 0,
		created_at TIMESTAMPTZ DEFAULT NOW()
	)`,
	`CREATE TABLE IF NOT EXISTS model_registry (
		id TEXT PRIMARY KEY,
		provider_alias TEXT NOT NULL,
		model_id TEXT NOT NULL,
		display_name TEXT NOT NULL,
		type TEXT DEFAULT 'llm',
		is_custom BOOLEAN DEFAULT false,
		is_enabled BOOLEAN DEFAULT true,
		metadata JSONB DEFAULT '{}',
		created_at TIMESTAMPTZ DEFAULT NOW(),
		UNIQUE(provider_alias, model_id, type)
	)`,
}

// EnsureTables creates tables in Supabase if they don't exist
// Uses Supabase's RPC endpoint to execute raw SQL
func EnsureTables(client *Client) error {
	if !client.IsConfigured() {
		return fmt.Errorf("supabase not configured")
	}

	// Try to query accounts table — if it works, tables exist
	_, err := client.List("accounts", map[string]string{"limit": "1"})
	if err == nil {
		return nil // Tables already exist
	}

	// If error contains "relation" or "does not exist", create tables
	errStr := err.Error()
	if !strings.Contains(errStr, "42P01") && !strings.Contains(errStr, "does not exist") && !strings.Contains(errStr, "relation") {
		// Different error — maybe auth issue
		return fmt.Errorf("supabase connection error: %w", err)
	}

	// Create tables via RPC (requires pg_net or direct SQL execution)
	// PostgREST doesn't support DDL directly, so we use the /rpc endpoint
	// with a custom function, OR we just document that user needs to create tables manually.
	//
	// For now: return instructions to user
	return fmt.Errorf("supabase tables not found. Please create them via Supabase dashboard SQL editor. Schema available at: liam --help-schema")
}

// GetCreateTableSQL returns the SQL for manual table creation
func GetCreateTableSQL() string {
	return strings.Join(createTableSQL, ";\n\n") + ";"
}
