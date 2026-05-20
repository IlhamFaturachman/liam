package integrations

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type OpenClaw struct{}

func (o *OpenClaw) Name() string            { return "openclaw" }
func (o *OpenClaw) DisplayName() string     { return "Open Claw" }
func (o *OpenClaw) Description() string     { return "Open Claw AI Assistant" }
func (o *OpenClaw) Icon() string            { return "smart_toy" }
func (o *OpenClaw) ConfigPath() string      { return "~/.openclaw/openclaw.json" }
func (o *OpenClaw) BinaryName() string      { return "openclaw" }
func (o *OpenClaw) SupportsAutoApply() bool { return true }

func (o *OpenClaw) ModelSlots() []ModelSlot {
	return []ModelSlot{
		{Key: "primary", Label: "Primary Model", Description: "Default model for all agents", Default: "kr/claude-opus-4.7"},
	}
}

func (o *OpenClaw) Status() (*ToolStatus, error) {
	configPath := ExpandHome(o.ConfigPath())
	hasLiam := false
	if FileExists(configPath) {
		data, err := os.ReadFile(configPath)
		if err == nil {
			var cfg map[string]interface{}
			if json.Unmarshal(data, &cfg) == nil {
				if mod, ok := cfg["models"].(map[string]interface{}); ok {
					if providers, ok := mod["providers"].(map[string]interface{}); ok {
						if _, ok := providers["liam"]; ok {
							hasLiam = true
						}
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

func (o *OpenClaw) Apply(cfg ToolConfig) error {
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

	model := cfg.Models["primary"]
	if model == "" {
		model = "kr/claude-opus-4.7"
	}

	mod, _ := cfgMap["models"].(map[string]interface{})
	if mod == nil {
		mod = map[string]interface{}{}
	}
	providers, _ := mod["providers"].(map[string]interface{})
	if providers == nil {
		providers = map[string]interface{}{}
	}

	// Collect all unique models
	allModels := map[string]string{}
	allModels[model] = model

	// Add all models from registry if available
	if len(cfg.AllModels) > 0 {
		for _, m := range cfg.AllModels {
			allModels[m.ID] = m.DisplayName
		}
	}

	for _, m := range cfg.AgentModels {
		if m != "" {
			if _, exists := allModels[m]; !exists {
				allModels[m] = m
			}
		}
	}
	modelsList := []map[string]interface{}{}
	for id, name := range allModels {
		modelsList = append(modelsList, map[string]interface{}{"id": id, "name": name})
	}

	providers["liam"] = map[string]interface{}{
		"baseUrl": cfg.BaseURL,
		"apiKey":  cfg.APIKey,
		"api":     "openai-completions",
		"models":  modelsList,
	}
	mod["providers"] = providers
	cfgMap["models"] = mod

	agents, _ := cfgMap["agents"].(map[string]interface{})
	if agents == nil {
		agents = map[string]interface{}{}
	}
	defaults, _ := agents["defaults"].(map[string]interface{})
	if defaults == nil {
		defaults = map[string]interface{}{}
	}
	dmodel, _ := defaults["model"].(map[string]interface{})
	if dmodel == nil {
		dmodel = map[string]interface{}{}
	}
	dmodel["primary"] = "liam/" + model
	defaults["model"] = dmodel

	dmodels, _ := defaults["models"].(map[string]interface{})
	if dmodels == nil {
		dmodels = map[string]interface{}{}
	}
	for k := range dmodels {
		if strings.HasPrefix(k, "liam/") {
			delete(dmodels, k)
		}
	}
	for id := range allModels {
		dmodels["liam/"+id] = map[string]interface{}{}
	}
	defaults["models"] = dmodels
	agents["defaults"] = defaults

	// Per-agent overrides
	if list, ok := agents["list"].([]interface{}); ok && len(cfg.AgentModels) > 0 {
		for i, agent := range list {
			if a, ok := agent.(map[string]interface{}); ok {
				if id, ok := a["id"].(string); ok {
					if agentModel, has := cfg.AgentModels[id]; has && agentModel != "" {
						a["model"] = "liam/" + agentModel
						list[i] = a
					}
				}
			}
		}
		agents["list"] = list
	}
	cfgMap["agents"] = agents

	return SafeWriteJSON(configPath, cfgMap)
}

func (o *OpenClaw) Reset() error {
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

	if mod, ok := cfgMap["models"].(map[string]interface{}); ok {
		if providers, ok := mod["providers"].(map[string]interface{}); ok {
			delete(providers, "liam")
			if len(providers) == 0 {
				delete(mod, "providers")
			} else {
				mod["providers"] = providers
			}
			if len(mod) == 0 {
				delete(cfgMap, "models")
			} else {
				cfgMap["models"] = mod
			}
		}
	}
	if agents, ok := cfgMap["agents"].(map[string]interface{}); ok {
		if defaults, ok := agents["defaults"].(map[string]interface{}); ok {
			if dmodels, ok := defaults["models"].(map[string]interface{}); ok {
				for k := range dmodels {
					if strings.HasPrefix(k, "liam/") {
						delete(dmodels, k)
					}
				}
				if len(dmodels) == 0 {
					delete(defaults, "models")
				} else {
					defaults["models"] = dmodels
				}
			}
			if dmodel, ok := defaults["model"].(map[string]interface{}); ok {
				if p, ok := dmodel["primary"].(string); ok && strings.HasPrefix(p, "liam/") {
					delete(dmodel, "primary")
				}
				if len(dmodel) == 0 {
					delete(defaults, "model")
				} else {
					defaults["model"] = dmodel
				}
			}
		}
		// Reset per-agent models
		if list, ok := agents["list"].([]interface{}); ok {
			for i, agent := range list {
				if a, ok := agent.(map[string]interface{}); ok {
					if m, ok := a["model"].(string); ok && strings.HasPrefix(m, "liam/") {
						delete(a, "model")
						list[i] = a
					}
				}
			}
			agents["list"] = list
		}
	}

	return SafeWriteJSON(configPath, cfgMap)
}

func (o *OpenClaw) Snippet(cfg ToolConfig) string {
	model := cfg.Models["primary"]
	if model == "" {
		model = "kr/claude-opus-4.7"
	}
	return fmt.Sprintf(`{
  "agents": {
    "defaults": {
      "model": { "primary": "liam/%s" },
      "models": { "liam/%s": {} }
    }
  },
  "models": {
    "providers": {
      "liam": {
        "baseUrl": "%s",
        "apiKey": "%s",
        "api": "openai-completions",
        "models": [{ "id": "%s", "name": "%s" }]
      }
    }
  }
}`, model, model, cfg.BaseURL, cfg.APIKey, model, model)
}
