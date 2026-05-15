package integrations

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type ClaudeCode struct{}

func (c *ClaudeCode) Name() string            { return "claude-code" }
func (c *ClaudeCode) DisplayName() string     { return "Claude Code" }
func (c *ClaudeCode) Description() string     { return "Anthropic Claude Code CLI" }
func (c *ClaudeCode) Icon() string            { return "auto_awesome" }
func (c *ClaudeCode) ConfigPath() string      { return "~/.claude/settings.json" }
func (c *ClaudeCode) BinaryName() string      { return "claude" }
func (c *ClaudeCode) SupportsAutoApply() bool { return true }

func (c *ClaudeCode) ModelSlots() []ModelSlot {
	return []ModelSlot{
		{Key: "opus", Label: "Opus Model", Description: "Default model for opus class requests", Default: "ag/claude-opus-4-6-thinking"},
		{Key: "sonnet", Label: "Sonnet Model", Description: "Default model for sonnet class requests", Default: "ag/claude-sonnet-4-6"},
		{Key: "haiku", Label: "Haiku Model", Description: "Default model for haiku class requests", Default: "ag/claude-sonnet-4-6"},
	}
}

func (c *ClaudeCode) Status() (*ToolStatus, error) {
	configPath := ExpandHome(c.ConfigPath())
	hasLiam := false
	if FileExists(configPath) {
		data, err := os.ReadFile(configPath)
		if err == nil {
			var cfg map[string]interface{}
			if json.Unmarshal(data, &cfg) == nil {
				if env, ok := cfg["env"].(map[string]interface{}); ok {
					if base, ok := env["ANTHROPIC_BASE_URL"].(string); ok {
						if strings.Contains(base, "localhost") || strings.Contains(base, "127.0.0.1") || strings.Contains(base, "/v1") {
							hasLiam = true
						}
					}
				}
			}
		}
	}
	return &ToolStatus{
		Name: c.Name(), DisplayName: c.DisplayName(), Description: c.Description(), Icon: c.Icon(),
		Installed: IsToolInstalled(c.BinaryName(), c.ConfigPath()),
		HasLiam:   hasLiam, ConfigPath: configPath, ConfigExists: FileExists(configPath),
		SupportsAutoApply: true, BinaryName: c.BinaryName(), ModelSlots: c.ModelSlots(),
	}, nil
}

func (c *ClaudeCode) Apply(cfg ToolConfig) error {
	configPath := ExpandHome(c.ConfigPath())
	if err := EnsureDir(c.ConfigPath()); err != nil {
		return err
	}
	if err := BackupFile(c.ConfigPath()); err != nil {
		return err
	}

	cfgMap := map[string]interface{}{}
	if data, err := os.ReadFile(configPath); err == nil {
		json.Unmarshal(data, &cfgMap)
	}

	env, _ := cfgMap["env"].(map[string]interface{})
	if env == nil {
		env = map[string]interface{}{}
	}
	env["ANTHROPIC_BASE_URL"] = cfg.BaseURL
	env["ANTHROPIC_AUTH_TOKEN"] = cfg.APIKey
	if m, ok := cfg.Models["opus"]; ok && m != "" {
		env["ANTHROPIC_DEFAULT_OPUS_MODEL"] = m
	}
	if m, ok := cfg.Models["sonnet"]; ok && m != "" {
		env["ANTHROPIC_DEFAULT_SONNET_MODEL"] = m
	}
	if m, ok := cfg.Models["haiku"]; ok && m != "" {
		env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = m
	}
	env["API_TIMEOUT_MS"] = "600000"
	cfgMap["env"] = env

	return SafeWriteJSON(configPath, cfgMap)
}

func (c *ClaudeCode) Reset() error {
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
	cfgMap := map[string]interface{}{}
	if err := json.Unmarshal(data, &cfgMap); err != nil {
		return err
	}
	if env, ok := cfgMap["env"].(map[string]interface{}); ok {
		delete(env, "ANTHROPIC_BASE_URL")
		delete(env, "ANTHROPIC_AUTH_TOKEN")
		delete(env, "ANTHROPIC_DEFAULT_OPUS_MODEL")
		delete(env, "ANTHROPIC_DEFAULT_SONNET_MODEL")
		delete(env, "ANTHROPIC_DEFAULT_HAIKU_MODEL")
		delete(env, "API_TIMEOUT_MS")
		if len(env) == 0 {
			delete(cfgMap, "env")
		} else {
			cfgMap["env"] = env
		}
	}
	return SafeWriteJSON(configPath, cfgMap)
}

func (c *ClaudeCode) Snippet(cfg ToolConfig) string {
	opus := cfg.Models["opus"]
	sonnet := cfg.Models["sonnet"]
	haiku := cfg.Models["haiku"]
	if opus == "" {
		opus = "ag/claude-opus-4-6-thinking"
	}
	if sonnet == "" {
		sonnet = "ag/claude-sonnet-4-6"
	}
	if haiku == "" {
		haiku = sonnet
	}
	return fmt.Sprintf(`{
  "env": {
    "ANTHROPIC_BASE_URL": "%s",
    "ANTHROPIC_AUTH_TOKEN": "%s",
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "%s",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "%s",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "%s",
    "API_TIMEOUT_MS": "600000"
  }
}`, cfg.BaseURL, cfg.APIKey, opus, sonnet, haiku)
}
