package integrations

// ToolConfig holds the configuration for applying a tool
type ToolConfig struct {
	APIKey  string            `json:"api_key"`
	BaseURL string            `json:"base_url"`
	Models  map[string]string `json:"models"`            // slot name -> model id (e.g. "primary": "ag/claude-opus-4-6-thinking")
	AgentModels map[string]string `json:"agent_models"`  // for tools with per-agent overrides (OpenClaw)
}

// ModelSlot describes a model configuration slot for a tool
type ModelSlot struct {
	Key         string `json:"key"`         // "primary", "subagent", "opus", "sonnet", "haiku"
	Label       string `json:"label"`       // "Primary Model", "Opus Model"
	Description string `json:"description"` // "Used as default for chat"
	Default     string `json:"default"`     // Default model for this slot
}

// Tool is the interface implemented by every CLI tool adapter
type Tool interface {
	Name() string                              // "claude-code"
	DisplayName() string                       // "Claude Code"
	Description() string                       // "Anthropic Claude Code CLI"
	Icon() string                              // Material symbol icon name
	ConfigPath() string                        // ~/.claude/settings.json
	BinaryName() string                        // "claude" or "" if not a CLI binary
	SupportsAutoApply() bool                   // false for Cursor/Cline
	ModelSlots() []ModelSlot                   // Per-tool model slots
	Status() (*ToolStatus, error)              // {installed, has_liam, config_exists}
	Apply(cfg ToolConfig) error                // Auto-apply config
	Reset() error                              // Remove LIAM config
	Snippet(cfg ToolConfig) string             // For Copy button display
}

// ToolStatus describes the current state of a tool
type ToolStatus struct {
	Name              string `json:"name"`
	DisplayName       string `json:"display_name"`
	Description       string `json:"description"`
	Icon              string `json:"icon"`
	Installed         bool   `json:"installed"`
	HasLiam           bool   `json:"has_liam"`
	ConfigPath        string `json:"config_path"`
	ConfigExists      bool   `json:"config_exists"`
	SupportsAutoApply bool   `json:"supports_auto_apply"`
	BinaryName        string `json:"binary_name,omitempty"`
	ModelSlots        []ModelSlot `json:"model_slots"`
}
