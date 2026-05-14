package integrations

import "fmt"

type Cursor struct{}

func (c *Cursor) Name() string             { return "cursor" }
func (c *Cursor) DisplayName() string      { return "Cursor" }
func (c *Cursor) Description() string      { return "Cursor AI Code Editor" }
func (c *Cursor) Icon() string             { return "edit_note" }
func (c *Cursor) ConfigPath() string       { return "" }
func (c *Cursor) BinaryName() string       { return "" }
func (c *Cursor) SupportsAutoApply() bool  { return false }

func (c *Cursor) ModelSlots() []ModelSlot {
	return []ModelSlot{
		{Key: "primary", Label: "Default Model", Description: "Model to use in Cursor", Default: "ag/claude-opus-4-6-thinking"},
	}
}

func (c *Cursor) Status() (*ToolStatus, error) {
	return &ToolStatus{
		Name: c.Name(), DisplayName: c.DisplayName(), Description: c.Description(), Icon: c.Icon(),
		Installed: true, HasLiam: false,
		ConfigPath: "Manual setup", ConfigExists: false,
		SupportsAutoApply: false, ModelSlots: c.ModelSlots(),
	}, nil
}

func (c *Cursor) Apply(cfg ToolConfig) error {
	return fmt.Errorf("Cursor requires manual setup")
}

func (c *Cursor) Reset() error {
	return nil
}

func (c *Cursor) Snippet(cfg ToolConfig) string {
	model := cfg.Models["primary"]
	if model == "" { model = "ag/claude-opus-4-6-thinking" }
	return fmt.Sprintf(`Cursor → Settings → Models → OpenAI API:

  Override OpenAI Base URL: %s
  OpenAI API Key:           %s

Then add a custom model: %s`, cfg.BaseURL, cfg.APIKey, model)
}
