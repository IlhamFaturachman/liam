package integrations

import (
	"fmt"
	"os"
	"path/filepath"
)

type Cline struct{}

func (c *Cline) Name() string            { return "cline" }
func (c *Cline) DisplayName() string     { return "Cline" }
func (c *Cline) Description() string     { return "Cline AI Coding Assistant (VSCode)" }
func (c *Cline) Icon() string            { return "extension" }
func (c *Cline) ConfigPath() string      { return "" }
func (c *Cline) BinaryName() string      { return "" }
func (c *Cline) SupportsAutoApply() bool { return false }

func (c *Cline) ModelSlots() []ModelSlot {
	return []ModelSlot{
		{Key: "primary", Label: "Default Model", Description: "Model to use in Cline", Default: "kr/claude-opus-4.7"},
	}
}

// detectInstalled checks for Cline VSCode extension installation
func (c *Cline) detectInstalled() bool {
	// VSCode extension paths - look for any folder starting with "saoudrizwan.claude-dev-"
	extensionDirs := []string{
		ExpandHome("~/.vscode/extensions"),
		ExpandHome("~/.vscode-insiders/extensions"),
		ExpandHome("~/.cursor/extensions"),
		ExpandHome("~/.windsurf/extensions"),
	}
	for _, dir := range extensionDirs {
		if entries, err := os.ReadDir(dir); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					name := e.Name()
					// Cline extension ID
					if len(name) > 21 && name[:21] == "saoudrizwan.claude-dev" {
						return true
					}
				}
			}
		}
	}
	// Also check Cline data directory (older versions)
	dataDir := ExpandHome("~/.cline")
	if _, err := os.Stat(filepath.Join(dataDir, "data")); err == nil {
		return true
	}
	return false
}

func (c *Cline) Status() (*ToolStatus, error) {
	installed := c.detectInstalled()
	configPath := "Manual (VSCode extension)"
	if !installed {
		configPath = "Not installed"
	}
	return &ToolStatus{
		Name: c.Name(), DisplayName: c.DisplayName(), Description: c.Description(), Icon: c.Icon(),
		Installed: installed, HasLiam: false,
		ConfigPath: configPath, ConfigExists: false,
		SupportsAutoApply: false, ModelSlots: c.ModelSlots(),
	}, nil
}

func (c *Cline) Apply(cfg ToolConfig) error {
	return fmt.Errorf("Cline requires manual setup")
}

func (c *Cline) Reset() error {
	return nil
}

func (c *Cline) Snippet(cfg ToolConfig) string {
	model := cfg.Models["primary"]
	if model == "" {
		model = "kr/claude-opus-4.7"
	}
	return fmt.Sprintf(`Cline (VSCode extension) → Settings:

  API Provider: OpenAI Compatible
  Base URL:     %s
  API Key:      %s
  Model ID:     %s`, cfg.BaseURL, cfg.APIKey, model)
}
