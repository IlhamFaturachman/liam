package elevenlabs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/liam-auto/liam/internal/db"
	upstream "github.com/liam-auto/liam/internal/httputil"
)

const baseURL = "https://api.elevenlabs.io/v1"

// Executor handles ElevenLabs TTS requests.
type Executor struct {
	client *http.Client
}

// NewExecutor creates a new ElevenLabs executor.
func NewExecutor() *Executor {
	return &Executor{
		client: upstream.NewUTLSClient(0, 60*time.Second),
	}
}

// Execute sends a TTS request to ElevenLabs.
func (e *Executor) Execute(account *db.Account, model string, body []byte, stream bool) (*http.Response, error) {
	return e.ExecuteWithSession(account, model, body, stream, "")
}

// ExecuteWithSession sends a TTS request (sessionID unused for EL but satisfies interface).
func (e *Executor) ExecuteWithSession(account *db.Account, model string, body []byte, stream bool, sessionID string) (*http.Response, error) {
	var creds ELCredentials
	if err := json.Unmarshal(account.Credentials, &creds); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}
	if creds.APIKey == "" {
		return nil, fmt.Errorf("no api_key for account %s", account.Email)
	}

	var req SpeechRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("parse speech request: %w", err)
	}

	elBody, url := translateRequest(req, model)

	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(elBody))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("xi-api-key", creds.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "audio/mpeg")

	return e.client.Do(httpReq)
}

// translateRequest converts OpenAI speech request to EL format.
// Returns the JSON body and the full URL with query params.
func translateRequest(req SpeechRequest, model string) ([]byte, string) {
	// Strip el/ prefix from model
	modelID := strings.TrimPrefix(model, "el/")
	modelID = strings.TrimPrefix(modelID, "elevenlabs/")

	voiceID := req.Voice
	if voiceID == "" {
		voiceID = "21m00Tcm4TlvDq8ikWAM" // Rachel (default)
	}

	elReq := ELSpeechRequest{
		Text:    req.Input,
		ModelID: modelID,
	}

	// Only set speed if meaningfully non-default
	speed := req.Speed
	if speed != 0 && (speed < 0.99 || speed > 1.01) {
		speed = clampSpeed(speed)
		elReq.VoiceSettings = &VoiceSettings{Speed: speed}
	}

	body, _ := json.Marshal(elReq)

	// Build URL
	outputFormat := outputFormatMap[req.ResponseFormat]
	if outputFormat == "" {
		outputFormat = "mp3_44100_128"
	}
	url := fmt.Sprintf("%s/text-to-speech/%s/stream?output_format=%s", baseURL, url.PathEscape(voiceID), outputFormat)

	return body, url
}

// clampSpeed clamps speed to EL's valid range [0.7, 1.2].
func clampSpeed(s float64) float64 {
	if s < 0.7 {
		return 0.7
	}
	if s > 1.2 {
		return 1.2
	}
	return s
}

// FetchSubscription calls GET /v1/user/subscription to get quota info.
func FetchSubscription(apiKey string) (total, remaining int, resetAt string, err error) {
	client := upstream.NewUTLSClient(0, 30*time.Second)
	req, _ := http.NewRequest("GET", baseURL+"/user/subscription", nil)
	req.Header.Set("xi-api-key", apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return 0, 0, "", fmt.Errorf("subscription API returned %d", resp.StatusCode)
	}

	var sub struct {
		CharacterCount int    `json:"character_count"`
		CharacterLimit int    `json:"character_limit"`
		NextResetUnix  int64  `json:"next_character_count_reset_unix"`
		Tier           string `json:"tier"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sub); err != nil {
		return 0, 0, "", err
	}

	total = sub.CharacterLimit
	remaining = sub.CharacterLimit - sub.CharacterCount
	if sub.NextResetUnix > 0 {
		resetAt = time.Unix(sub.NextResetUnix, 0).UTC().Format(time.RFC3339)
	}
	return total, remaining, resetAt, nil
}
