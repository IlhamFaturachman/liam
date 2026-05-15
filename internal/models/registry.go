package models

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Model represents an entry in the model registry
type Model struct {
	ID            string                 `json:"id"`             // "ag/claude-opus-4-6-thinking"
	ProviderAlias string                 `json:"provider_alias"` // "ag"
	ModelID       string                 `json:"model_id"`       // "claude-opus-4-6-thinking"
	DisplayName   string                 `json:"display_name"`   // "Claude Opus 4.6 Thinking"
	Type          string                 `json:"type"`           // "llm"
	IsCustom      bool                   `json:"is_custom"`
	IsEnabled     bool                   `json:"is_enabled"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt     time.Time              `json:"created_at"`
}

// Provider represents a known provider
type Provider struct {
	ID    string `json:"id"`    // "antigravity"
	Alias string `json:"alias"` // "ag"
	Name  string `json:"name"`  // "Antigravity"
}

// KnownProviders returns the registry of known providers
func KnownProviders() []Provider {
	return []Provider{
		{ID: "antigravity", Alias: "ag", Name: "Antigravity"},
		{ID: "kiro", Alias: "kr", Name: "Kiro"},
	}
}

// ProviderByAlias returns the provider by alias
func ProviderByAlias(alias string) *Provider {
	for _, p := range KnownProviders() {
		if p.Alias == alias {
			return &p
		}
	}
	return nil
}

// BuiltInModels returns the hardcoded built-in models seeded on first boot
func BuiltInModels() []Model {
	return []Model{
		// Antigravity
		{ProviderAlias: "ag", ModelID: "gemini-3.1-pro-high", DisplayName: "Gemini 3 Pro High", Type: "llm", IsCustom: false, IsEnabled: true, Metadata: map[string]interface{}{}},
		{ProviderAlias: "ag", ModelID: "gemini-3.1-pro-low", DisplayName: "Gemini 3 Pro Low", Type: "llm", IsCustom: false, IsEnabled: true, Metadata: map[string]interface{}{}},
		{ProviderAlias: "ag", ModelID: "gemini-3-flash", DisplayName: "Gemini 3 Flash", Type: "llm", IsCustom: false, IsEnabled: true, Metadata: map[string]interface{}{"thinking": false}},
		{ProviderAlias: "ag", ModelID: "claude-sonnet-4-6", DisplayName: "Claude Sonnet 4.6", Type: "llm", IsCustom: false, IsEnabled: true, Metadata: map[string]interface{}{}},
		{ProviderAlias: "ag", ModelID: "claude-opus-4-6-thinking", DisplayName: "Claude Opus 4.6 Thinking", Type: "llm", IsCustom: false, IsEnabled: true, Metadata: map[string]interface{}{}},
		{ProviderAlias: "ag", ModelID: "gpt-oss-120b-medium", DisplayName: "GPT OSS 120B Medium", Type: "llm", IsCustom: false, IsEnabled: true, Metadata: map[string]interface{}{}},

		// Kiro
		{ProviderAlias: "kr", ModelID: "claude-sonnet-4.5", DisplayName: "Claude Sonnet 4.5", Type: "llm", IsCustom: false, IsEnabled: true, Metadata: map[string]interface{}{}},
		{ProviderAlias: "kr", ModelID: "claude-haiku-4.5", DisplayName: "Claude Haiku 4.5", Type: "llm", IsCustom: false, IsEnabled: true, Metadata: map[string]interface{}{}},
		{ProviderAlias: "kr", ModelID: "claude-opus-4.6", DisplayName: "Claude Opus 4.6", Type: "llm", IsCustom: false, IsEnabled: true, Metadata: map[string]interface{}{}},
		{ProviderAlias: "kr", ModelID: "claude-opus-4.7", DisplayName: "Claude Opus 4.7", Type: "llm", IsCustom: false, IsEnabled: true, Metadata: map[string]interface{}{}},
		{ProviderAlias: "kr", ModelID: "deepseek-3.2", DisplayName: "DeepSeek 3.2", Type: "llm", IsCustom: false, IsEnabled: true, Metadata: map[string]interface{}{}},
		{ProviderAlias: "kr", ModelID: "qwen3-coder-next", DisplayName: "Qwen3 Coder Next", Type: "llm", IsCustom: false, IsEnabled: true, Metadata: map[string]interface{}{}},
		{ProviderAlias: "kr", ModelID: "glm-5", DisplayName: "GLM 5", Type: "llm", IsCustom: false, IsEnabled: true, Metadata: map[string]interface{}{}},
		{ProviderAlias: "kr", ModelID: "MiniMax-M2.5", DisplayName: "MiniMax M2.5", Type: "llm", IsCustom: false, IsEnabled: true, Metadata: map[string]interface{}{}},
		// NOTE on thinking: Kiro upstream rejects model SKUs with the
		// `-thinking` suffix ("Invalid model" error from CodeWhisperer).
		// Models like deepseek-3.2 emit reasoningContentEvent natively
		// without any client-side flag, so we don't expose `-thinking`
		// variants for Kiro at all — see internal/proxy/server.go where
		// the suffix is preserved literally only if the user adds a
		// custom Kiro model with that name.
	}
}

// Registry handles model storage and retrieval
type Registry struct {
	db *sql.DB
}

// NewRegistry creates a new model registry backed by the database
func NewRegistry(db *sql.DB) *Registry {
	return &Registry{db: db}
}

// SeedBuiltIn seeds built-in models if they don't exist
func (r *Registry) SeedBuiltIn() error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, m := range BuiltInModels() {
		id := m.ProviderAlias + "/" + m.ModelID
		metaJSON, _ := json.Marshal(m.Metadata)
		// Insert only if not exists (preserves user's enable/disable state)
		_, err := r.db.Exec(`
			INSERT OR IGNORE INTO model_registry 
			(id, provider_alias, model_id, display_name, type, is_custom, is_enabled, metadata, created_at)
			VALUES (?, ?, ?, ?, ?, 0, 1, ?, ?)`,
			id, m.ProviderAlias, m.ModelID, m.DisplayName, m.Type, string(metaJSON), now)
		if err != nil {
			return fmt.Errorf("seed model %s: %w", id, err)
		}
	}
	return nil
}

// List returns all models, optionally filtered by provider
func (r *Registry) List(providerAlias string) ([]Model, error) {
	var rows *sql.Rows
	var err error

	if providerAlias != "" {
		rows, err = r.db.Query(`
			SELECT id, provider_alias, model_id, display_name, type, is_custom, is_enabled, metadata, created_at
			FROM model_registry WHERE provider_alias = ? ORDER BY is_custom, model_id`, providerAlias)
	} else {
		rows, err = r.db.Query(`
			SELECT id, provider_alias, model_id, display_name, type, is_custom, is_enabled, metadata, created_at
			FROM model_registry ORDER BY provider_alias, is_custom, model_id`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var models []Model
	for rows.Next() {
		var m Model
		var isCustom, isEnabled int
		var metadataJSON, createdAt string
		err := rows.Scan(&m.ID, &m.ProviderAlias, &m.ModelID, &m.DisplayName, &m.Type,
			&isCustom, &isEnabled, &metadataJSON, &createdAt)
		if err != nil {
			continue
		}
		m.IsCustom = isCustom == 1
		m.IsEnabled = isEnabled == 1
		m.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		json.Unmarshal([]byte(metadataJSON), &m.Metadata)
		if m.Metadata == nil {
			m.Metadata = map[string]interface{}{}
		}
		models = append(models, m)
	}
	return models, nil
}

// Get returns a single model by full ID (provider/model)
func (r *Registry) Get(id string) (*Model, error) {
	var m Model
	var isCustom, isEnabled int
	var metadataJSON, createdAt string
	err := r.db.QueryRow(`
		SELECT id, provider_alias, model_id, display_name, type, is_custom, is_enabled, metadata, created_at
		FROM model_registry WHERE id = ?`, id).Scan(
		&m.ID, &m.ProviderAlias, &m.ModelID, &m.DisplayName, &m.Type,
		&isCustom, &isEnabled, &metadataJSON, &createdAt)
	if err != nil {
		return nil, err
	}
	m.IsCustom = isCustom == 1
	m.IsEnabled = isEnabled == 1
	m.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	json.Unmarshal([]byte(metadataJSON), &m.Metadata)
	if m.Metadata == nil {
		m.Metadata = map[string]interface{}{}
	}
	return &m, nil
}

// AddCustom adds a user-defined custom model
func (r *Registry) AddCustom(providerAlias, modelID, displayName, modelType string, metadata map[string]interface{}) (*Model, error) {
	if modelType == "" {
		modelType = "llm"
	}
	if displayName == "" {
		displayName = modelID
	}
	if metadata == nil {
		metadata = map[string]interface{}{}
	}

	id := providerAlias + "/" + modelID
	metaJSON, _ := json.Marshal(metadata)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	_, err := r.db.Exec(`
		INSERT INTO model_registry 
		(id, provider_alias, model_id, display_name, type, is_custom, is_enabled, metadata, created_at)
		VALUES (?, ?, ?, ?, ?, 1, 1, ?, ?)`,
		id, providerAlias, modelID, displayName, modelType, string(metaJSON), now)
	if err != nil {
		return nil, err
	}

	return r.Get(id)
}

// Remove removes a model from the registry
func (r *Registry) Remove(id string) error {
	_, err := r.db.Exec("DELETE FROM model_registry WHERE id = ?", id)
	return err
}

// Toggle enables/disables a model
func (r *Registry) Toggle(id string, enabled bool) error {
	val := 0
	if enabled {
		val = 1
	}
	_, err := r.db.Exec("UPDATE model_registry SET is_enabled = ? WHERE id = ?", val, id)
	return err
}

// SetEnabledForProvider toggles all models for a provider (Disable All button)
func (r *Registry) SetEnabledForProvider(providerAlias string, enabled bool) error {
	val := 0
	if enabled {
		val = 1
	}
	_, err := r.db.Exec("UPDATE model_registry SET is_enabled = ? WHERE provider_alias = ?", val, providerAlias)
	return err
}

// GetEnabledIDs returns all enabled model IDs (for /v1/models endpoint)
func (r *Registry) GetEnabledIDs() ([]string, error) {
	rows, err := r.db.Query("SELECT id FROM model_registry WHERE is_enabled = 1 ORDER BY provider_alias, model_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// IsThinkingDisabled returns true if the model has metadata.thinking == false
func (r *Registry) IsThinkingDisabled(id string) bool {
	m, err := r.Get(id)
	if err != nil {
		return false
	}
	if v, ok := m.Metadata["thinking"]; ok {
		if b, ok := v.(bool); ok {
			return !b
		}
	}
	return false
}

// SplitID splits "provider/model" into alias and modelID
func SplitID(id string) (string, string) {
	parts := strings.SplitN(id, "/", 2)
	if len(parts) != 2 {
		return "", id
	}
	return parts[0], parts[1]
}
