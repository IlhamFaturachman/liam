package integrations

import "fmt"

type Cline struct{}

func (c *Cline) Name() string             { return "cline" }
func (c *Cline) DisplayName() string      { return "Cline" }
func (c *Cline) Description() string      { return "Cline AI Coding Assistant (VSCode)" }
func (c *Cline) Icon() string             { return "extension" }
func (c *Cline) ConfigPath() string       { return "" }
func (c *Cline) BinaryName() string       { return "" }
func (c *Cline) SupportsAutoApply() bool  { return false }

func (c *Cline) ModelSlots() []ModelSlot {
	return []ModelSlot{
		{Key: "primary", Label: "Default Model", Description: "Model to use in Cline", Default: "ag/claude-opus-4-6-thinking"},
	}
}

func (c *Cline) Status() (*ToolStatus, error) {
	return &ToolStatus{
		Name: c.Name(), DisplayName: c.DisplayName(), Description: c.Description(), Icon: c.Icon(),
		Installed: true, HasLiam: false,
		ConfigPath: "Manual (VSCode extension)", ConfigExists: false,
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
	if model == "" { model = "ag/claude-opus-4-6-thinking" }
	return fmt.Sprintf(`Cline (VSCode extension) → Settings:

  API Provider: OpenAI Compatible
  Base URL:     %s
  API Key:      %s
  Model ID:     %s`, cfg.BaseURL, cfg.APIKey, model)
}
