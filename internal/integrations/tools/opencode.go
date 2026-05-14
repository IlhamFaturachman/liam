package integrations

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type OpenCode struct{}

func (o *OpenCode) Name() string             { return "opencode" }
func (o *OpenCode) DisplayName() string      { return "OpenCode" }
func (o *OpenCode) Description() string      { return "OpenCode AI Terminal Assistant" }
func (o *OpenCode) Icon() string             { return "terminal" }
func (o *OpenCode) ConfigPath() string       { return "~/.config/opencode/opencode.json" }
func (o *OpenCode) BinaryName() string       { return "opencode" }
func (o *OpenCode) SupportsAutoApply() bool  { return true }

func (o *OpenCode) ModelSlots() []ModelSlot {
	return []ModelSlot{
		{Key: "primary", Label: "Primary Model", Description: "Default model for chat", Default: "ag/claude-opus-4-6-thinking"},
		{Key: "subagent", Label: "Subagent Model", Description: "Used by sub-agents (faster/cheaper)", Default: "ag/claude-sonnet-4-6"},
	}
}

func (o *OpenCode) Status() (*ToolStatus, error) {
	configPath := ExpandHome(o.ConfigPath())
	hasLiam := false
	if FileExists(configPath) {
		data, err := os.ReadFile(configPath)
		if err == nil {
			var cfg map[string]interface{}
			if json.Unmarshal(data, &cfg) == nil {
				if providers, ok := cfg["provider"].(map[string]interface{}); ok {
					if _, ok := providers["liam"]; ok { hasLiam = true }
				}
			}
		}
	}
	return &ToolStatus{
		Name: o.Name(), DisplayName: o.DisplayName(), Description: o.Description(), Icon: o.Icon(),
		Installed: IsToolInstalled(o.BinaryName(), o.ConfigPath()),
		HasLiam: hasLiam, ConfigPath: configPath, ConfigExists: FileExists(configPath),
		SupportsAutoApply: true, BinaryName: o.BinaryName(), ModelSlots: o.ModelSlots(),
	}, nil
}

func (o *OpenCode) Apply(cfg ToolConfig) error {
	configPath := ExpandHome(o.ConfigPath())
	if err := EnsureDir(o.ConfigPath()); err != nil { return err }
	if err := BackupFile(o.ConfigPath()); err != nil { return err }

	cfgMap := map[string]interface{}{}
	if data, err := os.ReadFile(configPath); err == nil { json.Unmarshal(data, &cfgMap) }
	if cfgMap["$schema"] == nil { cfgMap["$schema"] = "https://opencode.ai/config.json" }

	providers, _ := cfgMap["provider"].(map[string]interface{})
	if providers == nil { providers = map[string]interface{}{} }

	primary := cfg.Models["primary"]
	subagent := cfg.Models["subagent"]
	if primary == "" { primary = "ag/claude-opus-4-6-thinking" }
	if subagent == "" { subagent = "ag/claude-sonnet-4-6" }

	models := map[string]interface{}{
		primary: map[string]interface{}{"name": modelDisplay(primary)},
	}
	if subagent != primary {
		models[subagent] = map[string]interface{}{"name": modelDisplay(subagent)}
	}

	providers["liam"] = map[string]interface{}{
		"npm":  "@ai-sdk/openai-compatible",
		"name": "LIAM",
		"options": map[string]interface{}{
			"baseURL": cfg.BaseURL,
			"apiKey":  cfg.APIKey,
		},
		"models": models,
	}
	cfgMap["provider"] = providers

	// Set active + subagent at top level
	cfgMap["model"] = "liam/" + primary
	if cfgMap["agent"] == nil {
		cfgMap["agent"] = map[string]interface{}{}
	}
	if agentMap, ok := cfgMap["agent"].(map[string]interface{}); ok {
		agentMap["subagent"] = "liam/" + subagent
		cfgMap["agent"] = agentMap
	}

	out, err := json.MarshalIndent(cfgMap, "", "  ")
	if err != nil { return err }
	return os.WriteFile(configPath, out, 0644)
}

func (o *OpenCode) Reset() error {
	configPath := ExpandHome(o.ConfigPath())
	if !FileExists(configPath) { return nil }
	if err := BackupFile(o.ConfigPath()); err != nil { return err }
	data, err := os.ReadFile(configPath)
	if err != nil { return err }
	cfgMap := map[string]interface{}{}
	if err := json.Unmarshal(data, &cfgMap); err != nil { return err }

	if providers, ok := cfgMap["provider"].(map[string]interface{}); ok {
		delete(providers, "liam")
		if len(providers) == 0 { delete(cfgMap, "provider") } else { cfgMap["provider"] = providers }
	}
	if model, ok := cfgMap["model"].(string); ok && strings.HasPrefix(model, "liam/") {
		delete(cfgMap, "model")
	}
	if agent, ok := cfgMap["agent"].(map[string]interface{}); ok {
		if sub, ok := agent["subagent"].(string); ok && strings.HasPrefix(sub, "liam/") {
			delete(agent, "subagent")
		}
		if len(agent) == 0 { delete(cfgMap, "agent") } else { cfgMap["agent"] = agent }
	}

	out, _ := json.MarshalIndent(cfgMap, "", "  ")
	return os.WriteFile(configPath, out, 0644)
}

func (o *OpenCode) Snippet(cfg ToolConfig) string {
	primary := cfg.Models["primary"]
	subagent := cfg.Models["subagent"]
	if primary == "" { primary = "ag/claude-opus-4-6-thinking" }
	if subagent == "" { subagent = "ag/claude-sonnet-4-6" }
	return fmt.Sprintf(`{
  "$schema": "https://opencode.ai/config.json",
  "model": "liam/%s",
  "agent": { "subagent": "liam/%s" },
  "provider": {
    "liam": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "LIAM",
      "options": {
        "baseURL": "%s",
        "apiKey": "%s"
      },
      "models": {
        "%s": { "name": "%s" },
        "%s": { "name": "%s" }
      }
    }
  }
}`, primary, subagent, cfg.BaseURL, cfg.APIKey,
   primary, modelDisplay(primary), subagent, modelDisplay(subagent))
}

func modelDisplay(modelID string) string {
	if idx := strings.LastIndex(modelID, "/"); idx >= 0 {
		return modelID[idx+1:]
	}
	return modelID
}
