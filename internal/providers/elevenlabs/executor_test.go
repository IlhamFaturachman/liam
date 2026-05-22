package elevenlabs

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTranslateRequest(t *testing.T) {
	tests := []struct {
		name       string
		req        SpeechRequest
		model      string
		wantModel  string
		wantVoice  string
		wantFormat string
		wantSpeed  bool
	}{
		{
			name:       "basic mp3",
			req:        SpeechRequest{Input: "hello", Voice: "abc123", ResponseFormat: "mp3", Speed: 1.0},
			model:      "el/eleven_flash_v2_5",
			wantModel:  "eleven_flash_v2_5",
			wantVoice:  "abc123",
			wantFormat: "mp3_44100_128",
			wantSpeed:  false,
		},
		{
			name:       "opus format",
			req:        SpeechRequest{Input: "test", Voice: "xyz", ResponseFormat: "opus"},
			model:      "el/eleven_multilingual_v2",
			wantModel:  "eleven_multilingual_v2",
			wantVoice:  "xyz",
			wantFormat: "opus_48000_128",
			wantSpeed:  false,
		},
		{
			name:       "speed clamped high",
			req:        SpeechRequest{Input: "fast", Voice: "v1", Speed: 4.0},
			model:      "el/eleven_v3",
			wantModel:  "eleven_v3",
			wantVoice:  "v1",
			wantFormat: "mp3_44100_128",
			wantSpeed:  true,
		},
		{
			name:       "speed clamped low",
			req:        SpeechRequest{Input: "slow", Voice: "v2", Speed: 0.25},
			model:      "elevenlabs/eleven_turbo_v2_5",
			wantModel:  "eleven_turbo_v2_5",
			wantVoice:  "v2",
			wantFormat: "mp3_44100_128",
			wantSpeed:  true,
		},
		{
			name:       "default voice when empty",
			req:        SpeechRequest{Input: "hi", Voice: "", ResponseFormat: "wav"},
			model:      "el/eleven_flash_v2_5",
			wantModel:  "eleven_flash_v2_5",
			wantVoice:  "21m00Tcm4TlvDq8ikWAM",
			wantFormat: "wav_44100",
			wantSpeed:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, url := translateRequest(tt.req, tt.model)

			var elReq ELSpeechRequest
			if err := json.Unmarshal(body, &elReq); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			if elReq.ModelID != tt.wantModel {
				t.Errorf("model_id = %q, want %q", elReq.ModelID, tt.wantModel)
			}
			if elReq.Text != tt.req.Input {
				t.Errorf("text = %q, want %q", elReq.Text, tt.req.Input)
			}
			if tt.wantSpeed && elReq.VoiceSettings == nil {
				t.Error("expected voice_settings with speed, got nil")
			}
			if !tt.wantSpeed && elReq.VoiceSettings != nil {
				t.Errorf("expected no voice_settings, got %+v", elReq.VoiceSettings)
			}

			if !strings.Contains(url, "/"+tt.wantVoice+"/") {
				t.Errorf("url %q missing voice_id %q", url, tt.wantVoice)
			}
			if !strings.Contains(url, "output_format="+tt.wantFormat) {
				t.Errorf("url %q missing output_format=%s", url, tt.wantFormat)
			}
		})
	}
}

func TestClampSpeed(t *testing.T) {
	tests := []struct {
		in   float64
		want float64
	}{
		{0.25, 0.7},
		{0.7, 0.7},
		{1.0, 1.0},
		{1.2, 1.2},
		{4.0, 1.2},
	}
	for _, tt := range tests {
		got := clampSpeed(tt.in)
		if got != tt.want {
			t.Errorf("clampSpeed(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
