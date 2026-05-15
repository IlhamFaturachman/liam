package integrations

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

type Hermes struct{}

var hermesModelBlockRE = regexp.MustCompile(`(?m)^model:[ \t]*\r?\n((?:[ \t]+.*\r?\n?|[ \t]*\r?\n)*)`)

func (h *Hermes) Name() string            { return "hermes" }
func (h *Hermes) DisplayName() string     { return "Hermes Agent" }
func (h *Hermes) Description() string     { return "Nous Research self-improving AI agent" }
func (h *Hermes) Icon() string            { return "auto_awesome" }
func (h *Hermes) ConfigPath() string      { return "~/.hermes/config.yaml" }
func (h *Hermes) BinaryName() string      { return "hermes" }
func (h *Hermes) SupportsAutoApply() bool { return true }

func (h *Hermes) ModelSlots() []ModelSlot {
	return []ModelSlot{
		{Key: "primary", Label: "Default Model", Description: "Model used by Hermes agent", Default: "ag/claude-opus-4-6-thinking"},
	}
}

func (h *Hermes) envPath() string {
	return ExpandHome("~/.hermes/.env")
}

func (h *Hermes) Status() (*ToolStatus, error) {
	configPath := ExpandHome(h.ConfigPath())
	hasLiam := false
	if FileExists(configPath) {
		data, err := os.ReadFile(configPath)
		if err == nil {
			content := string(data)
			if hermesModelBlockRE.MatchString(content) {
				match := hermesModelBlockRE.FindStringSubmatch(content)
				if len(match) > 1 {
					body := match[1]
					if strings.Contains(body, "localhost") || strings.Contains(body, "127.0.0.1") || strings.Contains(body, "/v1") {
						hasLiam = true
					}
				}
			}
		}
	}
	return &ToolStatus{
		Name: h.Name(), DisplayName: h.DisplayName(), Description: h.Description(), Icon: h.Icon(),
		Installed: IsToolInstalled(h.BinaryName(), h.ConfigPath()),
		HasLiam:   hasLiam, ConfigPath: configPath, ConfigExists: FileExists(configPath),
		SupportsAutoApply: true, BinaryName: h.BinaryName(), ModelSlots: h.ModelSlots(),
	}, nil
}

func (h *Hermes) Apply(cfg ToolConfig) error {
	configPath := ExpandHome(h.ConfigPath())
	if err := EnsureDir(h.ConfigPath()); err != nil {
		return err
	}
	if err := BackupFile(h.ConfigPath()); err != nil {
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

	newBlock := fmt.Sprintf("model:\n  default: \"%s\"\n  provider: \"custom\"\n  base_url: \"%s\"\n", model, cfg.BaseURL)
	var newContent string
	if hermesModelBlockRE.MatchString(existing) {
		newContent = hermesModelBlockRE.ReplaceAllString(existing, newBlock)
	} else if existing == "" {
		newContent = newBlock
	} else {
		newContent = newBlock + "\n" + existing
	}
	if err := os.WriteFile(configPath, []byte(newContent), 0644); err != nil {
		return err
	}

	envContent := ""
	if data, err := os.ReadFile(h.envPath()); err == nil {
		envContent = string(data)
	}
	envContent = upsertEnvVar(envContent, "OPENAI_API_KEY", cfg.APIKey)
	return os.WriteFile(h.envPath(), []byte(envContent), 0644)
}

func (h *Hermes) Reset() error {
	configPath := ExpandHome(h.ConfigPath())
	if !FileExists(configPath) {
		return nil
	}
	if err := BackupFile(h.ConfigPath()); err != nil {
		return err
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	cleaned := hermesModelBlockRE.ReplaceAllString(string(data), "")
	cleaned = strings.TrimLeft(cleaned, "\n")
	return os.WriteFile(configPath, []byte(cleaned), 0644)
}

func (h *Hermes) Snippet(cfg ToolConfig) string {
	model := cfg.Models["primary"]
	if model == "" {
		model = "ag/claude-opus-4-6-thinking"
	}
	return fmt.Sprintf(`# ~/.hermes/config.yaml
model:
  default: "%s"
  provider: "custom"
  base_url: "%s"

# ~/.hermes/.env
OPENAI_API_KEY=%s`, model, cfg.BaseURL, cfg.APIKey)
}

func upsertEnvVar(envText, key, value string) string {
	lines := strings.Split(envText, "\n")
	found := false
	prefix := key + "="
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			lines[i] = key + "=" + value
			found = true
			break
		}
	}
	if !found {
		if envText != "" && !strings.HasSuffix(envText, "\n") {
			envText += "\n"
		}
		envText += key + "=" + value + "\n"
		return envText
	}
	result := strings.Join(lines, "\n")
	if !strings.HasSuffix(result, "\n") {
		result += "\n"
	}
	return result
}
