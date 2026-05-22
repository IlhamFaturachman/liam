package elevenlabs

// SpeechRequest is the incoming OpenAI /v1/audio/speech format.
type SpeechRequest struct {
	Model          string  `json:"model"`
	Input          string  `json:"input"`
	Voice          string  `json:"voice"`
	ResponseFormat string  `json:"response_format"`
	Speed          float64 `json:"speed"`
}

// ELSpeechRequest is the outgoing ElevenLabs wire format.
type ELSpeechRequest struct {
	Text          string         `json:"text"`
	ModelID       string         `json:"model_id"`
	VoiceSettings *VoiceSettings `json:"voice_settings,omitempty"`
}

// VoiceSettings controls voice parameters.
type VoiceSettings struct {
	Speed float64 `json:"speed"`
}

// ELCredentials for ElevenLabs accounts (API key only).
type ELCredentials struct {
	APIKey string `json:"api_key"`
}

// outputFormatMap maps OpenAI response_format to EL output_format query param.
var outputFormatMap = map[string]string{
	"mp3":  "mp3_44100_128",
	"opus": "opus_48000_128",
	"wav":  "wav_44100",
	"pcm":  "pcm_16000",
	"flac": "mp3_44100_128",
	"":     "mp3_44100_128",
}
