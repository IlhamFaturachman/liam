package proxy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/liam-auto/liam/internal/config"
	"github.com/liam-auto/liam/internal/db"
)

func newContentFilterTestServer(t *testing.T) (*Server, *db.Database) {
	t.Helper()
	cfg := &config.Config{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	}
	database, err := db.New(cfg)
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return &Server{db: database}, database
}

func makeJSONRequest(method, path string, body any) *http.Request {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestContentFiltersAPI_InvalidModeRejected(t *testing.T) {
	s, _ := newContentFilterTestServer(t)
	rr := httptest.NewRecorder()
	s.handleSetContentFilterMode(rr, makeJSONRequest(http.MethodPost, "/api/settings/content-filters/mode", map[string]any{
		"mode": "bad-mode",
	}))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestContentFiltersAPI_InvalidRegexRejected(t *testing.T) {
	s, _ := newContentFilterTestServer(t)
	rr := httptest.NewRecorder()
	s.handleCreateContentFilterRule(rr, makeJSONRequest(http.MethodPost, "/api/settings/content-filters/rules", map[string]any{
		"pattern_type": "regex",
		"pattern":      "(bad",
		"replacement":  "x",
	}))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestContentFiltersAPI_ReorderRequiresExactRuleSet(t *testing.T) {
	s, database := newContentFilterTestServer(t)

	r1, err := database.CreateContentFilterRule(&db.ContentFilterRule{
		Source:      "local",
		PatternType: "exact",
		Pattern:     "a",
		Replacement: "b",
		Enabled:     true,
		Position:    1,
	})
	if err != nil {
		t.Fatalf("create rule 1: %v", err)
	}
	r2, err := database.CreateContentFilterRule(&db.ContentFilterRule{
		Source:      "local",
		PatternType: "exact",
		Pattern:     "c",
		Replacement: "d",
		Enabled:     true,
		Position:    2,
	})
	if err != nil {
		t.Fatalf("create rule 2: %v", err)
	}

	rr := httptest.NewRecorder()
	s.handleReorderContentFilterRules(rr, makeJSONRequest(http.MethodPost, "/api/settings/content-filters/reorder", map[string]any{
		"ids": []string{r1.ID},
	}))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing IDs, got %d body=%s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	s.handleReorderContentFilterRules(rr, makeJSONRequest(http.MethodPost, "/api/settings/content-filters/reorder", map[string]any{
		"ids": []string{r1.ID, r1.ID},
	}))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for duplicate IDs, got %d body=%s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	s.handleReorderContentFilterRules(rr, makeJSONRequest(http.MethodPost, "/api/settings/content-filters/reorder", map[string]any{
		"ids": []string{r2.ID, r1.ID},
	}))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid reorder, got %d body=%s", rr.Code, rr.Body.String())
	}
	rules, err := database.ListContentFilterRules("local")
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if len(rules) != 2 || rules[0].ID != r2.ID || rules[1].ID != r1.ID {
		t.Fatalf("unexpected order after reorder: %#v", rules)
	}
}

func TestContentFiltersAPI_CacheInvalidatedOnRuleMutation(t *testing.T) {
	s, database := newContentFilterTestServer(t)
	if _, err := database.CreateContentFilterRule(&db.ContentFilterRule{
		Source:      "local",
		PatternType: "exact",
		Pattern:     "a",
		Replacement: "b",
		Enabled:     true,
		Position:    1,
	}); err != nil {
		t.Fatalf("seed rule: %v", err)
	}

	compiled, err := s.getCompiledLocalContentFilters()
	if err != nil {
		t.Fatalf("compile initial: %v", err)
	}
	if len(compiled) != 1 {
		t.Fatalf("expected 1 compiled rule, got %d", len(compiled))
	}

	rr := httptest.NewRecorder()
	s.handleCreateContentFilterRule(rr, makeJSONRequest(http.MethodPost, "/api/settings/content-filters/rules", map[string]any{
		"pattern_type": "exact",
		"pattern":      "x",
		"replacement":  "y",
		"enabled":      true,
	}))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 on create, got %d body=%s", rr.Code, rr.Body.String())
	}

	compiled, err = s.getCompiledLocalContentFilters()
	if err != nil {
		t.Fatalf("compile after mutation: %v", err)
	}
	if len(compiled) != 2 {
		t.Fatalf("expected cache refresh with 2 rules, got %d", len(compiled))
	}
}
