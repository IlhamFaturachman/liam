package upstream

import (
	"net/http"
	"testing"
)

func TestScrubUpstreamHeaders_removesLeakHeaders(t *testing.T) {
	req := &http.Request{
		Header: make(http.Header),
	}

	// Set all the headers that should be removed
	headersToRemove := []string{
		// Proxy tracing
		"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto",
		"X-Forwarded-Port", "X-Real-IP", "Forwarded", "Via",

		// SDK / client identity
		"X-Title", "X-Stainless-Lang", "X-Stainless-Package-Version",
		"X-Stainless-Os", "X-Stainless-Arch", "X-Stainless-Runtime",
		"X-Stainless-Runtime-Version", "Http-Referer", "Referer",

		// Electron / Chromium fingerprint
		"Sec-Ch-Ua", "Sec-Ch-Ua-Mobile", "Sec-Ch-Ua-Platform",
		"Sec-Fetch-Mode", "Sec-Fetch-Site", "Sec-Fetch-Dest", "Priority",

		// Encoding negotiation
		"Accept-Encoding",
	}

	for _, header := range headersToRemove {
		req.Header.Set(header, "test-value")
	}

	// Call the function under test
	ScrubUpstreamHeaders(req)

	// Assert all headers were removed
	for _, header := range headersToRemove {
		if value := req.Header.Get(header); value != "" {
			t.Errorf("header %q should be removed, but got: %q", header, value)
		}
	}
}

func TestScrubUpstreamHeaders_preservesSafeHeaders(t *testing.T) {
	req := &http.Request{
		Header: make(http.Header),
	}

	// Set headers that should NOT be removed
	req.Header.Set("Authorization", "Bearer token123")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "MyClient/1.0")

	// Call the function under test
	ScrubUpstreamHeaders(req)

	// Assert safe headers are preserved
	tests := []struct {
		header string
		want   string
	}{
		{"Authorization", "Bearer token123"},
		{"Content-Type", "application/json"},
		{"User-Agent", "MyClient/1.0"},
	}

	for _, test := range tests {
		if got := req.Header.Get(test.header); got != test.want {
			t.Errorf("header %q: got %q, want %q", test.header, got, test.want)
		}
	}
}

func TestScrubUpstreamHeaders_doesNotPanicOnEmptyHeaders(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://example.com", nil)
	// req.Header is already initialised by NewRequest; no headers set
	ScrubUpstreamHeaders(req) // must not panic
}
