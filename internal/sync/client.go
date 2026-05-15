package sync

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is a Supabase REST (PostgREST) client
type Client struct {
	baseURL    string
	serviceKey string
	httpClient *http.Client
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
		httpClient: &http.Client{Timeout: 30 * time.Second},
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
		"Prefer":     "resolution=merge-duplicates",
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

func (c *Client) do(method, path string, body []byte, extraHeaders map[string]string) ([]byte, error) {
	url := c.baseURL + path

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

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("supabase %s %s: %d — %s", method, path, resp.StatusCode, string(respBody[:min(len(respBody), 200)]))
	}

	return respBody, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
