package sync

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// AutoCreateTables connects to Supabase PostgreSQL directly and creates tables
func AutoCreateTables(supabaseURL, dbPassword string) error {
	// Extract project ref from URL: https://[ref].supabase.co → ref
	ref := extractRef(supabaseURL)
	if ref == "" {
		return fmt.Errorf("could not extract project ref from URL: %s", supabaseURL)
	}

	// Build direct connection string
	// Format: postgresql://postgres.[ref]:[password]@db.[ref].supabase.co:5432/postgres
	connStr := fmt.Sprintf("postgresql://postgres.%s:%s@db.%s.supabase.co:5432/postgres",
		ref, url.QueryEscape(dbPassword), ref)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, connStr)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer conn.Close(ctx)

	// Run each CREATE TABLE statement
	for _, sql := range createTableSQL {
		if _, err := conn.Exec(ctx, sql); err != nil {
			return fmt.Errorf("create table: %w\nSQL: %s", err, sql[:min(len(sql), 100)])
		}
	}

	return nil
}

// VerifyTables checks if tables exist by querying each one
func VerifyTables(client *Client) error {
	tables := []string{"accounts", "api_keys", "model_registry"}
	for _, t := range tables {
		_, err := client.List(t, map[string]string{"limit": "1"})
		if err != nil {
			return fmt.Errorf("table '%s' not accessible: %w", t, err)
		}
	}
	return nil
}

// extractRef extracts project ref from Supabase URL
// https://zxbcplxkbbaxvovvkvcm.supabase.co → zxbcplxkbbaxvovvkvcm
func extractRef(supabaseURL string) string {
	supabaseURL = strings.TrimPrefix(supabaseURL, "https://")
	supabaseURL = strings.TrimPrefix(supabaseURL, "http://")
	parts := strings.Split(supabaseURL, ".")
	if len(parts) >= 1 {
		return parts[0]
	}
	return ""
}
