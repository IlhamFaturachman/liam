package integrations

import (
	"fmt"
	"os"
	"strings"
)

type Codex struct{}

func (c *Codex) Name() string            { return "codex" }
func (c *Codex) DisplayName() string     { return "Codex CLI" }
func (c *Codex) Description() string     { return "OpenAI Codex CLI" }
func (c *Codex) Icon() string            { return "code" }
func (c *Codex) ConfigPath() string      { return "~/.codex/config.toml" }
func (c *Codex) BinaryName() string      { return "codex" }
func (c *Codex) SupportsAutoApply() bool { return true }

func (c *Codex) ModelSlots() []ModelSlot {
	return []ModelSlot{
		{Key: "primary", Label: "Default Model", Description: "Used for all chat requests", Default: "ag/claude-opus-4-6-thinking"},
	}
}

func (c *Codex) Status() (*ToolStatus, error) {
	configPath := ExpandHome(c.ConfigPath())
	hasLiam := false
	if FileExists(configPath) {
		data, err := os.ReadFile(configPath)
		if err == nil && strings.Contains(string(data), "[model_providers.liam]") {
			hasLiam = true
		}
	}
	return &ToolStatus{
		Name: c.Name(), DisplayName: c.DisplayName(), Description: c.Description(), Icon: c.Icon(),
		Installed: IsToolInstalled(c.BinaryName(), c.ConfigPath()),
		HasLiam:   hasLiam, ConfigPath: configPath, ConfigExists: FileExists(configPath),
		SupportsAutoApply: true, BinaryName: c.BinaryName(), ModelSlots: c.ModelSlots(),
	}, nil
}

func (c *Codex) Apply(cfg ToolConfig) error {
	configPath := ExpandHome(c.ConfigPath())
	if err := EnsureDir(c.ConfigPath()); err != nil {
		return err
	}
	if err := BackupFile(c.ConfigPath()); err != nil {
		return err
	}

	model := cfg.Models["primary"]
	if model == "" {
		model = "ag/claude-opus-4-6-thinking"
	}

	existing := ""
	if data, err := os.ReadFile(configPath); err == nil {
		existing = string(data)
	}
	cleaned := stripLiamBlocks(existing)

	liamBlock := fmt.Sprintf(`model = "%s"
model_provider = "liam"

[model_providers.liam]
name = "LIAM"
base_url = "%s"
wire_api = "responses"
env_key = "LIAM_API_KEY"
`, model, cfg.BaseURL)

	var final string
	if strings.TrimSpace(cleaned) == "" {
		final = liamBlock
	} else {
		final = liamBlock + "\n" + cleaned
	}
	if err := os.WriteFile(configPath, []byte(final), 0644); err != nil {
		return err
	}

	envPath := strings.Replace(configPath, "config.toml", "auth.env", 1)
	envContent := fmt.Sprintf("export LIAM_API_KEY=%s\n", cfg.APIKey)
	os.WriteFile(envPath, []byte(envContent), 0644)
	return nil
}

func (c *Codex) Reset() error {
	configPath := ExpandHome(c.ConfigPath())
	if !FileExists(configPath) {
		return nil
	}
	if err := BackupFile(c.ConfigPath()); err != nil {
		return err
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, []byte(stripLiamBlocks(string(data))), 0644)
}

func (c *Codex) Snippet(cfg ToolConfig) string {
	model := cfg.Models["primary"]
	if model == "" {
		model = "ag/claude-opus-4-6-thinking"
	}
	return fmt.Sprintf(`# ~/.codex/config.toml
model = "%s"
model_provider = "liam"

[model_providers.liam]
name = "LIAM"
base_url = "%s"
wire_api = "responses"
env_key = "LIAM_API_KEY"

# Then run:
# export LIAM_API_KEY=%s`, model, cfg.BaseURL, cfg.APIKey)
}

func stripLiamBlocks(content string) string {
	lines := strings.Split(content, "\n")
	var out []string
	inLiamBlock := false
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "[model_providers.liam]" {
			inLiamBlock = true
			continue
		}
		if inLiamBlock {
			if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
				inLiamBlock = false
				out = append(out, line)
				continue
			}
			continue
		}
		if strings.HasPrefix(trimmed, "model =") || strings.HasPrefix(trimmed, "model=") {
			continue
		}
		if strings.HasPrefix(trimmed, "model_provider =") || strings.HasPrefix(trimmed, "model_provider=") {
			continue
		}
		out = append(out, line)
	}
	result := strings.TrimSpace(strings.Join(out, "\n"))
	if result != "" {
		result += "\n"
	}
	return result
}
