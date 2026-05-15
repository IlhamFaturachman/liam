package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

// Client is a Supabase REST (PostgREST) client
type Client struct {
	baseURL    string
	serviceKey string
	httpClient *http.Client
}

// dialTimeout is the per-attempt TCP connect timeout. We keep it short so a
// dead route (e.g. NAT64 with broken upstream) fails fast and we can retry
// against the next resolved address.
const dialTimeout = 8 * time.Second

// buildHTTPClient returns an http.Client that prefers IPv4 first. Some
// IPv6-only / NAT64 networks reset connections to Supabase's IPv6 endpoint,
// while the IPv4 path still works. We fall back to IPv6 only when no IPv4
// address is available so single-stack v6 networks remain functional.
func buildHTTPClient() *http.Client {
	baseDialer := &net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: 30 * time.Second,
	}

	dialIPv4Then6 := func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, splitErr := net.SplitHostPort(addr)
		if splitErr != nil {
			return baseDialer.DialContext(ctx, network, addr)
		}

		ips, lookupErr := net.DefaultResolver.LookupIPAddr(ctx, host)
		if lookupErr != nil || len(ips) == 0 {
			// Fall back to the system resolver / dialer if we can't
			// pre-resolve (e.g. captive portals, custom resolvers).
			return baseDialer.DialContext(ctx, network, addr)
		}

		// Try every IPv4 address first.
		var lastErr error
		for _, ip := range ips {
			if ip.IP.To4() == nil {
				continue
			}
			conn, dialErr := baseDialer.DialContext(ctx, "tcp4", net.JoinHostPort(ip.IP.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}

		// Then any IPv6 address.
		for _, ip := range ips {
			if ip.IP.To4() != nil {
				continue
			}
			conn, dialErr := baseDialer.DialContext(ctx, "tcp6", net.JoinHostPort(ip.IP.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}

		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("no usable address for %s", host)
	}

	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialIPv4Then6,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          50,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
	}
}

// NewClient creates a new Supabase client
func NewClient(url, serviceKey string) *Client {
	// Ensure URL doesn't have trailing slash
	if len(url) > 0 && url[len(url)-1] == '/' {
		url = url[:len(url)-1]
	}
	return &Client{
		baseURL:    url,
		serviceKey: serviceKey,
		httpClient: buildHTTPClient(),
	}
}

// IsConfigured returns true if Supabase credentials are set
func (c *Client) IsConfigured() bool {
	return c.baseURL != "" && c.serviceKey != ""
}

// Ping tests connectivity to Supabase
func (c *Client) Ping() error {
	_, err := c.get("/rest/v1/", nil)
	return err
}

// --- CRUD Operations ---

// List fetches all rows from a table with optional query params
func (c *Client) List(table string, params map[string]string) ([]map[string]interface{}, error) {
	query := "?select=*"
	for k, v := range params {
		query += "&" + k + "=" + v
	}
	body, err := c.get("/rest/v1/"+table+query, nil)
	if err != nil {
		return nil, err
	}
	var result []map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return result, nil
}

// Upsert inserts or updates rows (merge-duplicates on conflict)
func (c *Client) Upsert(table string, rows []map[string]interface{}) error {
	if len(rows) == 0 {
		return nil
	}
	data, _ := json.Marshal(rows)
	_, err := c.post("/rest/v1/"+table, data, map[string]string{
		"Prefer":      "resolution=merge-duplicates",
		"On-Conflict": "id",
	})
	return err
}

// UpsertOne inserts or updates a single row
func (c *Client) UpsertOne(table string, row map[string]interface{}) error {
	return c.Upsert(table, []map[string]interface{}{row})
}

// Delete removes rows matching a filter
func (c *Client) Delete(table string, filter string) error {
	_, err := c.del("/rest/v1/"+table+"?"+filter, nil)
	return err
}

// --- HTTP helpers ---

func (c *Client) get(path string, extraHeaders map[string]string) ([]byte, error) {
	return c.do("GET", path, nil, extraHeaders)
}

func (c *Client) post(path string, body []byte, extraHeaders map[string]string) ([]byte, error) {
	return c.do("POST", path, body, extraHeaders)
}

func (c *Client) patch(path string, body []byte, extraHeaders map[string]string) ([]byte, error) {
	return c.do("PATCH", path, body, extraHeaders)
}

func (c *Client) del(path string, extraHeaders map[string]string) ([]byte, error) {
	return c.do("DELETE", path, nil, extraHeaders)
}

// shouldRetryNetworkErr returns true for transient network conditions where
// retrying with a fresh connection (and possibly a different IP family) is
// likely to help: connection reset / EOF / timeout / connection refused.
func shouldRetryNetworkErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	transientFragments := []string{
		"connection reset",
		"connection refused",
		"broken pipe",
		"unexpected eof",
		"i/o timeout",
		"no route to host",
		"network is unreachable",
		"tls handshake",
	}
	for _, frag := range transientFragments {
		if strings.Contains(msg, frag) {
			return true
		}
	}
	return false
}

// do performs an HTTP request with up to 3 attempts on transient network
// errors. HTTP-level errors (4xx/5xx) are returned immediately because they
// represent application state, not connectivity.
func (c *Client) do(method, path string, body []byte, extraHeaders map[string]string) ([]byte, error) {
	url := c.baseURL + path

	const maxAttempts = 3
	var lastErr error

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 500ms, 1.5s
			delay := time.Duration(500*(1<<(attempt-1))) * time.Millisecond
			if !shouldRetryNetworkErr(lastErr) {
				break
			}
			log.Printf("[SYNC] %s %s retry %d/%d after %v: %v", method, path, attempt+1, maxAttempts, delay, lastErr)
			time.Sleep(delay)
		}

		var reqBody io.Reader
		if body != nil {
			reqBody = bytes.NewReader(body)
		}

		req, err := http.NewRequest(method, url, reqBody)
		if err != nil {
			return nil, err
		}

		req.Header.Set("apikey", c.serviceKey)
		req.Header.Set("Authorization", "Bearer "+c.serviceKey)
		req.Header.Set("Content-Type", "application/json")
		for k, v := range extraHeaders {
			req.Header.Set(k, v)
		}

		resp, doErr := c.httpClient.Do(req)
		if doErr != nil {
			lastErr = fmt.Errorf("request failed: %w", doErr)
			if shouldRetryNetworkErr(doErr) {
				continue
			}
			return nil, lastErr
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("supabase %s %s: %d — %s", method, path, resp.StatusCode, string(respBody[:min(len(respBody), 200)]))
		}

		return respBody, nil
	}

	return nil, lastErr
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
