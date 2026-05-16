package integrations

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	tools "github.com/liam-auto/liam/internal/integrations/tools"
)

// Service manages all CLI tool integrations
type Service struct {
	tools map[string]tools.Tool
	order []string
}

// NewService creates a new integrations service with all tool adapters
func NewService() *Service {
	t := []tools.Tool{
		&tools.ClaudeCode{},
		&tools.Codex{},
		&tools.OpenCode{},
		&tools.Cursor{},
		&tools.Cline{},
		&tools.OpenClaw{},
		&tools.Hermes{},
	}

	m := make(map[string]tools.Tool, len(t))
	order := make([]string, 0, len(t))
	for _, tool := range t {
		m[tool.Name()] = tool
		order = append(order, tool.Name())
	}

	return &Service{tools: m, order: order}
}

// HandleList returns the list of all tools with their status
func (s *Service) HandleList(w http.ResponseWriter, r *http.Request) {
	type ToolInfo struct {
		*tools.ToolStatus
	}

	result := []ToolInfo{}
	for _, name := range s.order {
		tool, ok := s.tools[name]
		if !ok {
			continue
		}
		status, err := tool.Status()
		if err != nil {
			continue
		}
		result = append(result, ToolInfo{ToolStatus: status})
	}
	writeJSON(w, 200, result)
}

// HandleGet returns detailed status for a single tool with snippet
func (s *Service) HandleGet(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "tool")
	tool, ok := s.tools[name]
	if !ok {
		writeError(w, 404, fmt.Sprintf("unknown tool: %s", name))
		return
	}

	status, err := tool.Status()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}

	// Build a default config for snippet generation
	model := r.URL.Query().Get("model")
	apiKey := r.URL.Query().Get("api_key")
	baseURL := r.URL.Query().Get("base_url")
	if apiKey == "" {
		apiKey = "<YOUR_KEY>"
	}
	if baseURL == "" {
		baseURL = "http://localhost:666/v1"
	}

	models := map[string]string{}
	for _, slot := range tool.ModelSlots() {
		val := r.URL.Query().Get("model_" + slot.Key)
		if val == "" {
			val = slot.Default
		}
		if val == "" {
			val = model
		}
		models[slot.Key] = val
	}

	cfg := tools.ToolConfig{
		APIKey:  apiKey,
		BaseURL: baseURL,
		Models:  models,
	}

	type Detail struct {
		*tools.ToolStatus
		Snippet string `json:"snippet"`
	}
	writeJSON(w, 200, Detail{ToolStatus: status, Snippet: tool.Snippet(cfg)})
}

// HandleApply auto-applies LIAM config to a tool
func (s *Service) HandleApply(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "tool")
	tool, ok := s.tools[name]
	if !ok {
		writeError(w, 404, fmt.Sprintf("unknown tool: %s", name))
		return
	}

	if !tool.SupportsAutoApply() {
		writeError(w, 400, "this tool requires manual setup")
		return
	}

	var req struct {
		APIKey      string            `json:"api_key"`
		BaseURL     string            `json:"base_url"`
		Models      map[string]string `json:"models"`
		AgentModels map[string]string `json:"agent_models"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}

	if req.APIKey == "" || req.BaseURL == "" {
		writeError(w, 400, "api_key and base_url are required")
		return
	}
	if req.Models == nil {
		req.Models = map[string]string{}
	}

	cfg := tools.ToolConfig{
		APIKey:      req.APIKey,
		BaseURL:     req.BaseURL,
		Models:      req.Models,
		AgentModels: req.AgentModels,
	}

	if err := tool.Apply(cfg); err != nil {
		writeError(w, 500, fmt.Sprintf("apply failed: %v", err))
		return
	}

	writeJSON(w, 200, map[string]interface{}{
		"success":     true,
		"tool":        name,
		"config_path": tool.ConfigPath(),
	})
}

// HandleSnippet returns just the snippet for a tool given a config
func (s *Service) HandleSnippet(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "tool")
	tool, ok := s.tools[name]
	if !ok {
		writeError(w, 404, fmt.Sprintf("unknown tool: %s", name))
		return
	}

	var req struct {
		APIKey      string            `json:"api_key"`
		BaseURL     string            `json:"base_url"`
		Models      map[string]string `json:"models"`
		AgentModels map[string]string `json:"agent_models"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	if req.APIKey == "" {
		req.APIKey = "<YOUR_KEY>"
	}
	if req.BaseURL == "" {
		req.BaseURL = "http://localhost:666/v1"
	}
	if req.Models == nil {
		req.Models = map[string]string{}
	}

	for _, slot := range tool.ModelSlots() {
		if req.Models[slot.Key] == "" {
			req.Models[slot.Key] = slot.Default
		}
	}

	cfg := tools.ToolConfig{
		APIKey:      req.APIKey,
		BaseURL:     req.BaseURL,
		Models:      req.Models,
		AgentModels: req.AgentModels,
	}

	writeJSON(w, 200, map[string]string{
		"snippet":     tool.Snippet(cfg),
		"config_path": tool.ConfigPath(),
	})
}

// HandleReset removes LIAM config from a tool
func (s *Service) HandleReset(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "tool")
	tool, ok := s.tools[name]
	if !ok {
		writeError(w, 404, fmt.Sprintf("unknown tool: %s", name))
		return
	}

	if err := tool.Reset(); err != nil {
		writeError(w, 500, fmt.Sprintf("reset failed: %v", err))
		return
	}

	writeJSON(w, 200, map[string]interface{}{"success": true, "tool": name})
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]interface{}{
		"error": map[string]string{"message": msg, "type": "error"},
	})
}
