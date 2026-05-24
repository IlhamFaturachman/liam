package antigravity

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// UpstreamModel represents a model returned from upstream API
type UpstreamModel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// FetchModels fetches the live list of available models from Antigravity upstream
func FetchModels(accessToken string) ([]UpstreamModel, error) {
	if accessToken == "" {
		return nil, fmt.Errorf("no access token")
	}

	// Try the models endpoint
	url := "https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:models"

	req, err := http.NewRequest("POST", url, strings.NewReader("{}"))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "antigravity/1.107.0 darwin/arm64")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		// Fallback to known static list if upstream doesn't expose endpoint
		return staticAGModels(), nil
	}

	var data struct {
		Models []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return staticAGModels(), nil
	}

	if len(data.Models) == 0 {
		return staticAGModels(), nil
	}

	result := make([]UpstreamModel, 0, len(data.Models))
	for _, m := range data.Models {
		name := m.DisplayName
		if name == "" {
			name = m.Name
		}
		if name == "" {
			name = m.ID
		}
		result = append(result, UpstreamModel{ID: m.ID, Name: name})
	}
	return result, nil
}

// staticAGModels returns the known AG models (fallback)
func staticAGModels() []UpstreamModel {
	return []UpstreamModel{
		{ID: "gemini-3.1-pro-high", Name: "AG Gemini 3.1 Pro High"},
		{ID: "gemini-3.1-pro-low", Name: "AG Gemini 3.1 Pro Low"},
		{ID: "gemini-3-flash", Name: "AG Gemini 3 Flash"},
		{ID: "claude-sonnet-4-6", Name: "AG Claude Sonnet 4.6"},
		{ID: "claude-opus-4-6-thinking", Name: "AG Claude Opus 4.6 Thinking"},
		{ID: "gpt-oss-120b-medium", Name: "AG GPT OSS 120B Medium"},
	}
}
