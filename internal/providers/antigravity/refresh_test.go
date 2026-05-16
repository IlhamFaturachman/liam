package antigravity

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liam-auto/liam/internal/config"
	"github.com/liam-auto/liam/internal/db"
)

// roundTripFunc lets tests stub out http.DefaultTransport without spinning
// up a real listener for trivial header checks. Currently unused but kept
// here so future tests don't have to re-derive it.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestRefreshToken_PreservesRefreshTokenWhenAbsent verifies the rotation-safe
// behaviour: when Google's response omits refresh_token (the common case)
// we keep the old one. Using `tokens.RefreshToken` blindly here would clear
// the field and kill the account on the next refresh.
func TestRefreshToken_PreservesRefreshTokenWhenAbsent(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if got := r.Form.Get("grant_type"); got != "refresh_token" {
			t.Errorf("grant_type = %q, want refresh_token", got)
		}
		if got := r.Form.Get("refresh_token"); got != "rt-original" {
			t.Errorf("refresh_token = %q, want rt-original", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at-new",
			"expires_in":   3600,
			"token_type":   "Bearer",
			// no refresh_token in response — Google's typical behaviour
		})
	}))
	defer ts.Close()

	withMockedTokenEndpoint(t, ts.URL, func() {
		creds := db.AGCredentials{
			AccessToken:  "at-old",
			RefreshToken: "rt-original",
			ExpiresAt:    time.Now().UTC().Format(time.RFC3339),
		}
		credsJSON, _ := json.Marshal(creds)
		account := &db.Account{Email: "x@y", Credentials: credsJSON}

		got, err := RefreshToken(&config.Config{AGClientID: "c", AGClientSecret: "s"}, account)
		if err != nil {
			t.Fatalf("RefreshToken failed: %v", err)
		}
		if got.AccessToken != "at-new" {
			t.Errorf("AccessToken = %q, want at-new", got.AccessToken)
		}
		if got.RefreshToken != "rt-original" {
			t.Errorf("RefreshToken = %q, want rt-original (preserved)", got.RefreshToken)
		}
	})
}

// TestRefreshToken_AdoptsRotatedRefreshToken verifies we DO replace the
// stored refresh_token when Google rotates it. Missing this is the
// "kill the account by next refresh" bug.
func TestRefreshToken_AdoptsRotatedRefreshToken(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "at-new",
			"refresh_token": "rt-rotated",
			"expires_in":    3600,
			"token_type":    "Bearer",
		})
	}))
	defer ts.Close()

	withMockedTokenEndpoint(t, ts.URL, func() {
		creds := db.AGCredentials{RefreshToken: "rt-original"}
		credsJSON, _ := json.Marshal(creds)
		account := &db.Account{Credentials: credsJSON}

		got, err := RefreshToken(&config.Config{AGClientID: "c", AGClientSecret: "s"}, account)
		if err != nil {
			t.Fatalf("RefreshToken failed: %v", err)
		}
		if got.RefreshToken != "rt-rotated" {
			t.Errorf("RefreshToken = %q, want rt-rotated (adopted from response)", got.RefreshToken)
		}
	})
}

// TestRefreshToken_ClassifiesInvalidGrant verifies the worker can tell
// dead-token from network-blip via the structured RefreshError.
func TestRefreshToken_ClassifiesInvalidGrant(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`))
	}))
	defer ts.Close()

	withMockedTokenEndpoint(t, ts.URL, func() {
		creds := db.AGCredentials{RefreshToken: "rt-dead"}
		credsJSON, _ := json.Marshal(creds)
		account := &db.Account{Credentials: credsJSON}

		_, err := RefreshToken(&config.Config{AGClientID: "c", AGClientSecret: "s"}, account)
		if err == nil {
			t.Fatal("expected error for invalid_grant")
		}
		re := AsRefreshError(err)
		if re == nil {
			t.Fatalf("expected *RefreshError, got %T: %v", err, err)
		}
		if re.Reason != RefreshErrorInvalidGrant {
			t.Errorf("Reason = %q, want %q", re.Reason, RefreshErrorInvalidGrant)
		}
		if !re.IsUnrecoverable() {
			t.Error("IsUnrecoverable() = false, want true for invalid_grant")
		}
	})
}

// TestRefreshToken_Singleflight verifies concurrent refresh calls for the
// same refresh_token collapse to a single upstream HTTP request. Without
// this dedup, parallel ticks (worker + on-demand) racing on the same token
// triggers Google's `refresh_token_reused` family revoke.
func TestRefreshToken_Singleflight(t *testing.T) {
	var hits atomic.Int32
	gate := make(chan struct{}) // hold the upstream until we've fanned out

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		<-gate
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at-new",
			"expires_in":   3600,
			"token_type":   "Bearer",
		})
	}))
	defer ts.Close()

	withMockedTokenEndpoint(t, ts.URL, func() {
		creds := db.AGCredentials{RefreshToken: "rt-shared"}
		credsJSON, _ := json.Marshal(creds)

		const N = 20
		var wg sync.WaitGroup
		var failures atomic.Int32
		for i := 0; i < N; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				account := &db.Account{Credentials: credsJSON}
				if _, err := RefreshToken(&config.Config{AGClientID: "c", AGClientSecret: "s"}, account); err != nil {
					failures.Add(1)
				}
			}()
		}

		// Give all goroutines time to enter singleflight before unblocking.
		time.Sleep(50 * time.Millisecond)
		close(gate)
		wg.Wait()

		if failures.Load() != 0 {
			t.Errorf("got %d failures, want 0", failures.Load())
		}
		if hits.Load() != 1 {
			t.Errorf("upstream hit %d times, want 1 (singleflight should dedupe)", hits.Load())
		}
	})
}

// withMockedTokenEndpoint swaps the http.DefaultTransport so requests to
// oauth2.googleapis.com transparently hit the test server. We can't just
// override the URL because the package hardcodes it; rewriting on the
// transport keeps the production code path intact.
func withMockedTokenEndpoint(t *testing.T, mockURL string, fn func()) {
	t.Helper()

	orig := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = orig })

	mockTarget, err := parseHostPort(mockURL)
	if err != nil {
		t.Fatalf("parse mock URL: %v", err)
	}

	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Host, "oauth2.googleapis.com") {
			r.URL.Scheme = "http"
			r.URL.Host = mockTarget
			r.Host = mockTarget
		}
		return orig.RoundTrip(r)
	})

	fn()
}

// parseHostPort extracts host:port from an httptest URL like "http://127.0.0.1:54321".
func parseHostPort(u string) (string, error) {
	u = strings.TrimPrefix(u, "http://")
	u = strings.TrimPrefix(u, "https://")
	if i := strings.Index(u, "/"); i >= 0 {
		u = u[:i]
	}
	if u == "" {
		return "", &refreshTestErr{"empty URL"}
	}
	return u, nil
}

type refreshTestErr struct{ s string }

func (e *refreshTestErr) Error() string { return e.s }
