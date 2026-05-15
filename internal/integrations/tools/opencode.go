package integrations

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type OpenCode struct{}

func (o *OpenCode) Name() string            { return "opencode" }
func (o *OpenCode) DisplayName() string     { return "OpenCode" }
func (o *OpenCode) Description() string     { return "OpenCode AI Terminal Assistant" }
func (o *OpenCode) Icon() string            { return "terminal" }
func (o *OpenCode) ConfigPath() string      { return "~/.config/opencode/opencode.json" }
func (o *OpenCode) BinaryName() string      { return "opencode" }
func (o *OpenCode) SupportsAutoApply() bool { return true }

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
					if _, ok := providers["liam"]; ok {
						hasLiam = true
					}
				}
			}
		}
	}
	return &ToolStatus{
		Name: o.Name(), DisplayName: o.DisplayName(), Description: o.Description(), Icon: o.Icon(),
		Installed: IsToolInstalled(o.BinaryName(), o.ConfigPath()),
		HasLiam:   hasLiam, ConfigPath: configPath, ConfigExists: FileExists(configPath),
		SupportsAutoApply: true, BinaryName: o.BinaryName(), ModelSlots: o.ModelSlots(),
	}, nil
}

func (o *OpenCode) Apply(cfg ToolConfig) error {
	configPath := ExpandHome(o.ConfigPath())
	if err := EnsureDir(o.ConfigPath()); err != nil {
		return err
	}
	if err := BackupFile(o.ConfigPath()); err != nil {
		return err
	}

	cfgMap := map[string]interface{}{}
	if data, err := os.ReadFile(configPath); err == nil {
		json.Unmarshal(data, &cfgMap)
	}
	if cfgMap["$schema"] == nil {
		cfgMap["$schema"] = "https://opencode.ai/config.json"
	}

	providers, _ := cfgMap["provider"].(map[string]interface{})
	if providers == nil {
		providers = map[string]interface{}{}
	}

	primary := cfg.Models["primary"]
	subagent := cfg.Models["subagent"]
	if primary == "" {
		primary = "ag/claude-opus-4-6-thinking"
	}
	if subagent == "" {
		subagent = "ag/claude-sonnet-4-6"
	}

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

	// Set the active model at the top level. Opencode reads this as the
	// default for chat sessions; format must be "<provider_id>/<model_id>".
	cfgMap["model"] = "liam/" + primary

	// Subagent wiring. Opencode's `agent` map expects each entry to be an
	// OBJECT with at least {mode, model} — never a bare string. The bug
	// previously written here ("subagent": "liam/...") caused 4 of 5
	// endpoints (config.providers, provider.list, app.agents, config.get)
	// to throw schema validation errors and fail to start.
	//
	// We model the LIAM subagent as a normal agent named "subagent" so
	// existing agents (e.g. "explorer") keep working untouched.
	if cfgMap["agent"] == nil {
		cfgMap["agent"] = map[string]interface{}{}
	}
	if agentMap, ok := cfgMap["agent"].(map[string]interface{}); ok {
		// Refresh — preserve description if user already customized.
		existing, _ := agentMap["subagent"].(map[string]interface{})
		if existing == nil {
			existing = map[string]interface{}{
				"description": "LIAM-managed default subagent (auto-generated)",
				"mode":        "subagent",
			}
		}
		existing["mode"] = "subagent"
		existing["model"] = "liam/" + subagent
		agentMap["subagent"] = existing

		// Also refresh any pre-existing agent entries that still
		// reference a now-removed provider (common when users switch
		// from 9router/enowxlabs to LIAM). We retarget them at the
		// LIAM subagent model so opencode's agent dispatcher doesn't
		// fail validation against missing providers.
		for name, raw := range agentMap {
			if name == "subagent" {
				continue
			}
			obj, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			modelStr, _ := obj["model"].(string)
			if modelStr == "" {
				continue
			}
			provID := ""
			if idx := strings.Index(modelStr, "/"); idx > 0 {
				provID = modelStr[:idx]
			}
			if provID != "" {
				if _, exists := providers[provID]; !exists {
					obj["model"] = "liam/" + subagent
					agentMap[name] = obj
				}
			}
		}
		cfgMap["agent"] = agentMap
	}

	return SafeWriteJSON(configPath, cfgMap)
}

func (o *OpenCode) Reset() error {
	configPath := ExpandHome(o.ConfigPath())
	if !FileExists(configPath) {
		return nil
	}
	if err := BackupFile(o.ConfigPath()); err != nil {
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

	if providers, ok := cfgMap["provider"].(map[string]interface{}); ok {
		delete(providers, "liam")
		if len(providers) == 0 {
			delete(cfgMap, "provider")
		} else {
			cfgMap["provider"] = providers
		}
	}
	if model, ok := cfgMap["model"].(string); ok && strings.HasPrefix(model, "liam/") {
		delete(cfgMap, "model")
	}
	// Drop the LIAM-managed subagent entry. Be tolerant of both the
	// current object form ({mode,model,…}) and the legacy string form
	// ("liam/...") that older builds wrote into agent.subagent.
	if agent, ok := cfgMap["agent"].(map[string]interface{}); ok {
		if subStr, ok := agent["subagent"].(string); ok && strings.HasPrefix(subStr, "liam/") {
			delete(agent, "subagent")
		}
		if subObj, ok := agent["subagent"].(map[string]interface{}); ok {
			if m, _ := subObj["model"].(string); strings.HasPrefix(m, "liam/") {
				delete(agent, "subagent")
			}
		}
		// Also retarget any agent that still references a `liam/...` model
		// to nothing, so opencode falls back to the top-level default.
		for name, raw := range agent {
			obj, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			if m, _ := obj["model"].(string); strings.HasPrefix(m, "liam/") {
				delete(obj, "model")
				agent[name] = obj
			}
		}
		if len(agent) == 0 {
			delete(cfgMap, "agent")
		} else {
			cfgMap["agent"] = agent
		}
	}

	return SafeWriteJSON(configPath, cfgMap)
}

func (o *OpenCode) Snippet(cfg ToolConfig) string {
	primary := cfg.Models["primary"]
	subagent := cfg.Models["subagent"]
	if primary == "" {
		primary = "ag/claude-opus-4-6-thinking"
	}
	if subagent == "" {
		subagent = "ag/claude-sonnet-4-6"
	}
	// Note: agent.subagent is an OBJECT (not a string). Opencode rejects
	// the bare-string form with a schema validation error that surfaces
	// as 4 of 5 endpoints failing on startup.
	return fmt.Sprintf(`{
  "$schema": "https://opencode.ai/config.json",
  "model": "liam/%s",
  "agent": {
    "subagent": {
      "description": "LIAM-managed default subagent",
      "mode": "subagent",
      "model": "liam/%s"
    }
  },
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
