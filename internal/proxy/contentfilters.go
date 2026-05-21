package proxy

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/liam-auto/liam/internal/db"
)

const (
	settingContentFiltersMode = "content_filters.mode"
	contentFilterModeLocal    = "local"
	contentFilterModeOff      = "off"
)

type contentFilterRule struct {
	ID              string `json:"id,omitempty"`
	Source          string `json:"source,omitempty"`
	PatternType     string `json:"pattern_type"`
	Pattern         string `json:"pattern"`
	Replacement     string `json:"replacement"`
	CaseInsensitive bool   `json:"case_insensitive"`
	Enabled         bool   `json:"enabled"`
	Position        int    `json:"position"`
}

type compiledContentFilterRule struct {
	rule  contentFilterRule
	regex *regexp.Regexp
}

func normalizeContentFilterMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case contentFilterModeLocal:
		return contentFilterModeLocal
	case contentFilterModeOff:
		return contentFilterModeOff
	default:
		return contentFilterModeOff
	}
}

func isAllowedContentFilterMode(mode string) bool {
	return mode == contentFilterModeLocal || mode == contentFilterModeOff
}

func compileContentFilterRules(rules []contentFilterRule) ([]compiledContentFilterRule, error) {
	sorted := make([]contentFilterRule, 0, len(rules))
	sorted = append(sorted, rules...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Position == sorted[j].Position {
			return sorted[i].ID < sorted[j].ID
		}
		return sorted[i].Position < sorted[j].Position
	})

	out := make([]compiledContentFilterRule, 0, len(sorted))
	for _, r := range sorted {
		patternType := strings.ToLower(strings.TrimSpace(r.PatternType))
		pattern := strings.TrimSpace(r.Pattern)
		if pattern == "" {
			return nil, &contentFilterValidationError{Message: "pattern cannot be empty"}
		}
		switch patternType {
		case "exact":
			cr := compiledContentFilterRule{rule: r}
			if r.CaseInsensitive {
				re, err := regexp.Compile("(?i)" + regexp.QuoteMeta(pattern))
				if err != nil {
					return nil, err
				}
				cr.regex = re
			}
			out = append(out, cr)
		case "regex":
			expr := pattern
			if r.CaseInsensitive {
				expr = "(?i)" + expr
			}
			re, err := regexp.Compile(expr)
			if err != nil {
				return nil, err
			}
			out = append(out, compiledContentFilterRule{rule: r, regex: re})
		default:
			return nil, &contentFilterValidationError{Message: "pattern_type must be exact or regex"}
		}
	}
	return out, nil
}

func (s *Server) invalidateContentFilterCache() {
	s.contentFilterMu.Lock()
	defer s.contentFilterMu.Unlock()
	s.contentFilterCacheReady = false
	s.contentFilterCompiled = nil
}

func (s *Server) getCompiledLocalContentFilters() ([]compiledContentFilterRule, error) {
	s.contentFilterMu.RLock()
	if s.contentFilterCacheReady {
		out := make([]compiledContentFilterRule, len(s.contentFilterCompiled))
		copy(out, s.contentFilterCompiled)
		s.contentFilterMu.RUnlock()
		return out, nil
	}
	s.contentFilterMu.RUnlock()

	s.contentFilterMu.Lock()
	defer s.contentFilterMu.Unlock()
	if s.contentFilterCacheReady {
		out := make([]compiledContentFilterRule, len(s.contentFilterCompiled))
		copy(out, s.contentFilterCompiled)
		return out, nil
	}

	dbRules, err := s.db.ListContentFilterRules("local")
	if err != nil {
		return nil, err
	}
	rules := make([]contentFilterRule, 0, len(dbRules))
	for _, rr := range dbRules {
		rules = append(rules, dbRuleToProxy(rr))
	}
	compiled, err := compileContentFilterRules(rules)
	if err != nil {
		return nil, err
	}
	s.contentFilterCompiled = compiled
	s.contentFilterCacheReady = true
	out := make([]compiledContentFilterRule, len(compiled))
	copy(out, compiled)
	return out, nil
}

func applyContentFiltersToRequest(req map[string]any, mode string, rules []compiledContentFilterRule) bool {
	if normalizeContentFilterMode(mode) != contentFilterModeLocal || len(rules) == 0 {
		return false
	}
	msgs, ok := req["messages"].([]any)
	if !ok {
		return false
	}

	changed := false
	for i := range msgs {
		msg, ok := msgs[i].(map[string]any)
		if !ok {
			continue
		}
		content, ok := msg["content"]
		if !ok {
			continue
		}
		next, did := applyContentFiltersToValue(content, rules)
		if did {
			msg["content"] = next
			changed = true
		}
	}
	return changed
}

func applyContentFiltersToValue(v any, rules []compiledContentFilterRule) (any, bool) {
	switch c := v.(type) {
	case string:
		return applyFirstMatchingRule(c, rules)
	case []any:
		changed := false
		for i := range c {
			part, ok := c[i].(map[string]any)
			if !ok {
				continue
			}
			typ, _ := part["type"].(string)
			typ = strings.ToLower(strings.TrimSpace(typ))

			if isTextPartType(typ) {
				if txt, ok := part["text"].(string); ok {
					next, did := applyFirstMatchingRule(txt, rules)
					if did {
						part["text"] = next
						changed = true
					}
				}
				continue
			}

			if typ == "tool_result" {
				next, did := applyContentFiltersToToolResultContent(part["content"], rules)
				if did {
					part["content"] = next
					changed = true
				}
			}
		}
		return c, changed
	default:
		return v, false
	}
}

func applyContentFiltersToToolResultContent(v any, rules []compiledContentFilterRule) (any, bool) {
	switch c := v.(type) {
	case string:
		return applyFirstMatchingRule(c, rules)
	case []any:
		changed := false
		for i := range c {
			part, ok := c[i].(map[string]any)
			if !ok {
				continue
			}
			typ, _ := part["type"].(string)
			if !isTextPartType(strings.ToLower(strings.TrimSpace(typ))) {
				continue
			}
			txt, ok := part["text"].(string)
			if !ok {
				continue
			}
			next, did := applyFirstMatchingRule(txt, rules)
			if did {
				part["text"] = next
				changed = true
			}
		}
		return c, changed
	default:
		return v, false
	}
}

func isTextPartType(typ string) bool {
	return typ == "text" || typ == "input_text" || typ == "output_text"
}

func applyFirstMatchingRule(text string, rules []compiledContentFilterRule) (string, bool) {
	for _, r := range rules {
		if !r.rule.Enabled {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(r.rule.PatternType)) {
		case "exact":
			if r.rule.CaseInsensitive {
				if r.regex != nil && r.regex.MatchString(text) {
					return r.regex.ReplaceAllString(text, r.rule.Replacement), true
				}
				continue
			}
			if strings.Contains(text, r.rule.Pattern) {
				return strings.ReplaceAll(text, r.rule.Pattern, r.rule.Replacement), true
			}
		case "regex":
			if r.regex != nil && r.regex.MatchString(text) {
				return r.regex.ReplaceAllString(text, r.rule.Replacement), true
			}
		}
	}
	return text, false
}

type contentFilterValidationError struct {
	Message string
}

func (e *contentFilterValidationError) Error() string {
	return e.Message
}

func dbRuleToProxy(r db.ContentFilterRule) contentFilterRule {
	return contentFilterRule{
		ID:              r.ID,
		Source:          r.Source,
		PatternType:     r.PatternType,
		Pattern:         r.Pattern,
		Replacement:     r.Replacement,
		CaseInsensitive: r.CaseInsensitive,
		Enabled:         r.Enabled,
		Position:        r.Position,
	}
}

func proxyRuleToDB(r contentFilterRule) db.ContentFilterRule {
	return db.ContentFilterRule{
		ID:              r.ID,
		Source:          r.Source,
		PatternType:     strings.ToLower(strings.TrimSpace(r.PatternType)),
		Pattern:         r.Pattern,
		Replacement:     r.Replacement,
		CaseInsensitive: r.CaseInsensitive,
		Enabled:         r.Enabled,
		Position:        r.Position,
	}
}

func (s *Server) handleGetContentFilters(w http.ResponseWriter, r *http.Request) {
	rules, err := s.db.ListContentFilterRules("local")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list content filters: "+err.Error())
		return
	}
	out := make([]contentFilterRule, 0, len(rules))
	for _, rr := range rules {
		out = append(out, dbRuleToProxy(rr))
	}
	mode := normalizeContentFilterMode(s.db.GetSetting(settingContentFiltersMode, contentFilterModeOff))
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":  mode,
		"rules": out,
		"modes": []map[string]any{
			{"id": "both", "enabled": false},
			{"id": "cloud", "enabled": false},
			{"id": "local", "enabled": true},
			{"id": "off", "enabled": true},
		},
		"tabs": []map[string]any{
			{"id": "cloud", "enabled": false},
			{"id": "local", "enabled": true},
			{"id": "community", "enabled": false},
		},
	})
}

func (s *Server) handleSetContentFilterMode(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	mode := strings.ToLower(strings.TrimSpace(input.Mode))
	if !isAllowedContentFilterMode(mode) {
		writeError(w, http.StatusBadRequest, "mode must be local or off")
		return
	}
	if err := s.db.SetSetting(settingContentFiltersMode, mode); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save mode: "+err.Error())
		return
	}
	s.invalidateContentFilterCache()
	s.handleGetContentFilters(w, r)
}

func (s *Server) handleCreateContentFilterRule(w http.ResponseWriter, r *http.Request) {
	var input struct {
		PatternType     string `json:"pattern_type"`
		Pattern         string `json:"pattern"`
		Replacement     string `json:"replacement"`
		CaseInsensitive *bool  `json:"case_insensitive,omitempty"`
		Enabled         *bool  `json:"enabled,omitempty"`
		Position        int    `json:"position,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	caseInsensitive := false
	enabled := true
	if input.CaseInsensitive != nil {
		caseInsensitive = *input.CaseInsensitive
	}
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	rule := contentFilterRule{
		Source:          "local",
		PatternType:     input.PatternType,
		Pattern:         input.Pattern,
		Replacement:     input.Replacement,
		CaseInsensitive: caseInsensitive,
		Enabled:         enabled,
		Position:        input.Position,
	}
	if _, err := compileContentFilterRules([]contentFilterRule{rule}); err != nil {
		writeError(w, http.StatusBadRequest, "invalid rule: "+err.Error())
		return
	}
	created, err := s.db.CreateContentFilterRule(&db.ContentFilterRule{
		Source:          "local",
		PatternType:     strings.ToLower(strings.TrimSpace(rule.PatternType)),
		Pattern:         rule.Pattern,
		Replacement:     rule.Replacement,
		CaseInsensitive: rule.CaseInsensitive,
		Enabled:         rule.Enabled,
		Position:        rule.Position,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create rule: "+err.Error())
		return
	}
	s.invalidateContentFilterCache()
	writeJSON(w, http.StatusOK, dbRuleToProxy(*created))
}

func (s *Server) handleUpdateContentFilterRule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if strings.TrimSpace(id) == "" {
		writeError(w, http.StatusBadRequest, "missing id")
		return
	}

	existing, err := s.db.GetContentFilterRule(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "rule not found")
		return
	}

	var input struct {
		PatternType     *string `json:"pattern_type,omitempty"`
		Pattern         *string `json:"pattern,omitempty"`
		Replacement     *string `json:"replacement,omitempty"`
		CaseInsensitive *bool   `json:"case_insensitive,omitempty"`
		Enabled         *bool   `json:"enabled,omitempty"`
		Position        *int    `json:"position,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	next := dbRuleToProxy(*existing)
	if input.PatternType != nil {
		next.PatternType = *input.PatternType
	}
	if input.Pattern != nil {
		next.Pattern = *input.Pattern
	}
	if input.Replacement != nil {
		next.Replacement = *input.Replacement
	}
	if input.CaseInsensitive != nil {
		next.CaseInsensitive = *input.CaseInsensitive
	}
	if input.Enabled != nil {
		next.Enabled = *input.Enabled
	}
	if input.Position != nil {
		next.Position = *input.Position
	}

	if _, err := compileContentFilterRules([]contentFilterRule{next}); err != nil {
		writeError(w, http.StatusBadRequest, "invalid rule: "+err.Error())
		return
	}

	updated, err := s.db.UpdateContentFilterRule(&db.ContentFilterRule{
		ID:              id,
		Source:          "local",
		PatternType:     strings.ToLower(strings.TrimSpace(next.PatternType)),
		Pattern:         next.Pattern,
		Replacement:     next.Replacement,
		CaseInsensitive: next.CaseInsensitive,
		Enabled:         next.Enabled,
		Position:        next.Position,
	})
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "rule not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update rule: "+err.Error())
		return
	}
	s.invalidateContentFilterCache()
	writeJSON(w, http.StatusOK, dbRuleToProxy(*updated))
}

func (s *Server) handleDeleteContentFilterRule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if strings.TrimSpace(id) == "" {
		writeError(w, http.StatusBadRequest, "missing id")
		return
	}
	if err := s.db.DeleteContentFilterRule(id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete rule: "+err.Error())
		return
	}
	s.invalidateContentFilterCache()
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleReorderContentFilterRules(w http.ResponseWriter, r *http.Request) {
	var input struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(input.IDs) == 0 {
		writeError(w, http.StatusBadRequest, "ids cannot be empty")
		return
	}
	rules, err := s.db.ListContentFilterRules("local")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list current rules: "+err.Error())
		return
	}
	if len(rules) != len(input.IDs) {
		writeError(w, http.StatusBadRequest, "ids must contain all local rule IDs exactly once")
		return
	}
	existing := make(map[string]struct{}, len(rules))
	for _, rr := range rules {
		existing[rr.ID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(input.IDs))
	for _, id := range input.IDs {
		id = strings.TrimSpace(id)
		if id == "" {
			writeError(w, http.StatusBadRequest, "id cannot be empty")
			return
		}
		if _, ok := existing[id]; !ok {
			writeError(w, http.StatusBadRequest, "unknown rule id: "+id)
			return
		}
		if _, dup := seen[id]; dup {
			writeError(w, http.StatusBadRequest, "duplicate rule id: "+id)
			return
		}
		seen[id] = struct{}{}
	}
	if err := s.db.ReorderContentFilterRules("local", input.IDs); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reorder rules: "+err.Error())
		return
	}
	s.invalidateContentFilterCache()
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}
