package models

import (
	"database/sql"
	"time"
)

// Alias represents a user-defined model alias
type Alias struct {
	Alias     string    `json:"alias"`
	Target    string    `json:"target"`
	CreatedAt time.Time `json:"created_at"`
}

// AliasStore handles alias storage
type AliasStore struct {
	db *sql.DB
}

// NewAliasStore creates a new alias store
func NewAliasStore(db *sql.DB) *AliasStore {
	return &AliasStore{db: db}
}

// List returns all aliases
func (a *AliasStore) List() ([]Alias, error) {
	rows, err := a.db.Query("SELECT alias, target, created_at FROM model_aliases ORDER BY alias")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var aliases []Alias
	for rows.Next() {
		var al Alias
		var createdAt string
		if err := rows.Scan(&al.Alias, &al.Target, &createdAt); err == nil {
			al.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
			aliases = append(aliases, al)
		}
	}
	return aliases, nil
}

// Set creates or updates an alias
func (a *AliasStore) Set(alias, target string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := a.db.Exec(`
		INSERT INTO model_aliases (alias, target, created_at) VALUES (?, ?, ?)
		ON CONFLICT(alias) DO UPDATE SET target = excluded.target`,
		alias, target, now)
	return err
}

// Delete removes an alias
func (a *AliasStore) Delete(alias string) error {
	_, err := a.db.Exec("DELETE FROM model_aliases WHERE alias = ?", alias)
	return err
}

// Resolve returns the target for an alias, or empty string if not found
func (a *AliasStore) Resolve(alias string) string {
	var target string
	a.db.QueryRow("SELECT target FROM model_aliases WHERE alias = ?", alias).Scan(&target)
	return target
}
