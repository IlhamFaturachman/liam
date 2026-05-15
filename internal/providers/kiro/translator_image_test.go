package kiro

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

// 8x8 red PNG, 124 bytes.
const sampleRedPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAgAAAAICAYAAADED76LAAAAFklEQVR4nGP8z8AARMQDxlEFlCkAAFhjAhFA9ZHgAAAAAElFTkSuQmCC"

func TestTranslateRequestImageOpenAIFormat(t *testing.T) {
	body := []byte(`{
		"model": "kr/claude-sonnet-4.6",
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "What color?"},
				{"type": "image_url", "image_url": {"url": "data:image/png;base64,` + sampleRedPNGBase64 + `"}}
			]
		}]
	}`)
	out, err := translateRequest("kr/claude-sonnet-4.6", body, "arn:test")
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, `"images":`) {
		t.Errorf("expected images field, got: %s", s)
	}
	if !strings.Contains(s, `"format":"png"`) {
		t.Errorf("expected png format, got: %s", s)
	}
	if !strings.Contains(s, `"bytes":"`+sampleRedPNGBase64+`"`) {
		t.Errorf("expected raw base64 bytes preserved, got: %s", s)
	}
}

// 1x1 white JPEG (smallest valid JPEG).
const sampleWhiteJPEGBase64 = "/9j/4AAQSkZJRgABAQEAAQABAAD/2wBDAAEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQH/2wBDAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQH/wAARCAABAAEDASIAAhEBAxEB/8QAFQABAQAAAAAAAAAAAAAAAAAAAAr/xAAUEAEAAAAAAAAAAAAAAAAAAAAA/8QAFAEBAAAAAAAAAAAAAAAAAAAAAP/EABQRAQAAAAAAAAAAAAAAAAAAAAD/2gAMAwEAAhEDEQA/AL+f/9k="

func TestTranslateRequestImageAnthropicFormat(t *testing.T) {
	body := []byte(`{
		"model": "kr/claude-sonnet-4.6",
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "Hello"},
				{"type": "image", "source": {"type": "base64", "media_type": "image/jpeg", "data": "` + sampleWhiteJPEGBase64 + `"}}
			]
		}]
	}`)
	out, err := translateRequest("kr/claude-sonnet-4.6", body, "arn:test")
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, `"format":"jpeg"`) {
		t.Errorf("expected jpeg format, got: %s", s)
	}
	if !strings.Contains(s, sampleWhiteJPEGBase64) {
		t.Errorf("expected base64 bytes preserved, got: %s", s)
	}
}

func TestTranslateRequestUnsupportedFormat(t *testing.T) {
	body := []byte(`{
		"model": "kr/claude-sonnet-4.6",
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "Read this:"},
				{"type": "document", "name": "report.pdf"}
			]
		}]
	}`)
	out, err := translateRequest("kr/claude-sonnet-4.6", body, "arn:test")
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if strings.Contains(string(out), `"images":`) {
		t.Errorf("PDF should not produce images field, got: %s", out)
	}
	if !strings.Contains(string(out), "Attached document") {
		t.Errorf("expected text fallback for unsupported document, got: %s", out)
	}
}

func TestTranslateRequestMultipleImages(t *testing.T) {
	body := []byte(`{
		"model": "kr/claude-sonnet-4.6",
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "compare"},
				{"type": "image_url", "image_url": {"url": "data:image/png;base64,` + sampleRedPNGBase64 + `"}},
				{"type": "image_url", "image_url": {"url": "data:image/jpeg;base64,` + sampleWhiteJPEGBase64 + `"}}
			]
		}]
	}`)
	out, err := translateRequest("kr/claude-sonnet-4.6", body, "arn:test")
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	var parsed map[string]interface{}
	json.Unmarshal(out, &parsed)
	state := parsed["conversationState"].(map[string]interface{})
	current := state["currentMessage"].(map[string]interface{})
	uim := current["userInputMessage"].(map[string]interface{})
	images, ok := uim["images"].([]interface{})
	if !ok || len(images) != 2 {
		t.Errorf("expected 2 images, got: %v", uim["images"])
	}
	if first, ok := images[0].(map[string]interface{}); ok {
		if format, _ := first["format"].(string); format != "png" {
			t.Errorf("expected first image png, got: %s", format)
		}
	}
	if second, ok := images[1].(map[string]interface{}); ok {
		if format, _ := second["format"].(string); format != "jpeg" {
			t.Errorf("expected second image jpeg, got: %s", format)
		}
	}
}

func TestExtractContentPartsBareString(t *testing.T) {
	text, images := extractContentParts(json.RawMessage(`"hello world"`))
	if text != "hello world" {
		t.Errorf("got: %q", text)
	}
	if len(images) != 0 {
		t.Errorf("expected no images")
	}
}

func TestExtractContentPartsOversized(t *testing.T) {
	// 14MB raw payload → above hardKiroImageBytes; loadBase64Image must
	// reject without crashing and surface a "too large" hint via the text
	// channel. Anything below the hard ceiling is shrunk in-flight.
	raw := strings.Repeat("a", 14*1024*1024)
	encoded := base64.StdEncoding.EncodeToString([]byte(raw))
	content, _ := json.Marshal([]map[string]interface{}{
		{"type": "image_url", "image_url": map[string]string{
			"url": "data:image/png;base64," + encoded,
		}},
	})
	text, images := extractContentParts(content)
	if len(images) != 0 {
		t.Errorf("oversized image should be rejected")
	}
	if !strings.Contains(text, "too large") {
		t.Errorf("expected 'too large' hint, got: %q", text[:200])
	}
}

func TestShrinkLargePNG(t *testing.T) {
	// Build a 2400x2400 image with high-frequency noise so PNG compression
	// can't squash it down. Raw size ~17 MB, which is what real
	// macOS screenshots end up at after Cmd+Shift+5 captures.
	img := image.NewRGBA(image.Rect(0, 0, 2400, 2400))
	rng := uint32(0x12345678)
	for y := 0; y < 2400; y++ {
		for x := 0; x < 2400; x++ {
			// Cheap LCG for deterministic noise.
			rng = rng*1664525 + 1013904223
			img.Set(x, y, color.RGBA{
				uint8(rng),
				uint8(rng >> 8),
				uint8(rng >> 16),
				255,
			})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	t.Logf("raw PNG: %d bytes (%.2f MB)", buf.Len(), float64(buf.Len())/1024/1024)
	if buf.Len() < targetKiroImageBytes {
		t.Skip("synthetic noise PNG is unexpectedly compressible")
	}

	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())
	out, hint, ok := loadBase64Image("image/png", encoded)
	if !ok {
		t.Fatalf("expected loadBase64Image to succeed, hint: %q", hint)
	}

	decoded, err := base64.StdEncoding.DecodeString(out.Source.Bytes)
	if err != nil {
		t.Fatalf("decode shrunk image: %v", err)
	}
	if len(decoded) > targetKiroImageBytes {
		t.Errorf("shrunk image still %d > target %d", len(decoded), targetKiroImageBytes)
	}
	if out.Format != "jpeg" {
		t.Errorf("expected jpeg after shrink, got %s", out.Format)
	}
	t.Logf("shrunk: %d bytes (%.2f MB), format=%s — %.1f%% reduction",
		len(decoded),
		float64(len(decoded))/1024/1024,
		out.Format,
		100*(1-float64(len(decoded))/float64(buf.Len())))
}

func TestExtractContentPartsHTTPURLDropsToText(t *testing.T) {
	// Non-data:// URLs would require a network round-trip. We're not
	// running the server during unit tests, so the fetch is expected to
	// fail and the loader must fall back to a clear hint instead of
	// blocking.
	content, _ := json.Marshal([]map[string]interface{}{
		{"type": "image_url", "image_url": map[string]string{
			"url": "https://invalid.localhost.example/none.png",
		}},
	})
	text, images := extractContentParts(content)
	if len(images) != 0 {
		t.Errorf("network-fetched image should fail in tests")
	}
	if !strings.Contains(text, "Image fetch") {
		t.Errorf("expected 'Image fetch' hint, got: %q", text)
	}
}
