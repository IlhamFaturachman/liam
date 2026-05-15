package proxy

import (
	"net/http"
	"strings"

	"github.com/liam-auto/liam/internal/config"
	"github.com/liam-auto/liam/internal/db"
)

// ProviderExecutor is the runtime contract every backend implements: take
// a model id + raw OpenAI-format body and return an HTTP response that
// downstream code (forward / stream / log) treats opaquely. Implementing
// this interface is the only thing required to register a new provider —
// no other file in the proxy package needs to change.
type ProviderExecutor interface {
	// ExecuteWithSession issues the upstream request bound to a stable
	// session id (used for prompt caching and anti-ban session affinity).
	ExecuteWithSession(account *db.Account, model string, body []byte, stream bool, sessionID string) (*http.Response, error)
}

// ProviderRefresher is the optional token-refresh hook. Providers that
// don't need refresh (or handle it inline elsewhere) can omit it; the
// proxy will simply skip the call.
type ProviderRefresher func(cfg *config.Config, database *db.Database, account *db.Account) error

// ProviderInfo carries everything the dashboard, registry, and API
// surfaces need about a provider so we can drive the UI from a single
// source of truth instead of duplicating "is this AG or Kiro?" logic
// across server.go, sse.go, and the HTML template.
type ProviderInfo struct {
	// ID is the canonical name persisted in DB (`accounts.provider`).
	// Examples: "antigravity", "kiro".
	ID string `json:"id"`

	// Aliases are model-id prefixes that route to this provider.
	// Example: AG accepts both `ag/...` and `antigravity/...`. The first
	// alias is treated as canonical for display + custom-model add UI.
	Aliases []string `json:"aliases"`

	// Label is the human-friendly name shown in the dashboard.
	Label string `json:"label"`

	// Icon is the Material Symbols icon name rendered next to the
	// label on cards and headers.
	Icon string `json:"icon"`

	// SupportsImport flags providers where the user can paste a refresh
	// token to add an account (Kiro / Antigravity). Drives the "Add
	// account" button + import modal on the Providers page.
	SupportsImport bool `json:"supports_import"`

	// Executor is the runtime that actually talks to the upstream.
	Executor ProviderExecutor `json:"-"`

	// Refresh is invoked before every Execute when set, giving the
	// provider a chance to mint a fresh access token.
	Refresh ProviderRefresher `json:"-"`
}

// providerRegistry holds the live ProviderInfo entries keyed by canonical
// id. We populate it once at server boot and never mutate after — the
// map is read-only at request time so no locking is required.
type providerRegistry struct {
	byID    map[string]*ProviderInfo
	byAlias map[string]*ProviderInfo
	ordered []*ProviderInfo
}

// newProviderRegistry returns an empty registry that the server bootstrap
// fills via Register before serving traffic.
func newProviderRegistry() *providerRegistry {
	return &providerRegistry{
		byID:    map[string]*ProviderInfo{},
		byAlias: map[string]*ProviderInfo{},
	}
}

// Register adds a provider. Calling Register with the same id twice
// silently overwrites the previous entry — convenient for tests but the
// production server only registers each provider once at boot.
func (r *providerRegistry) Register(info *ProviderInfo) {
	if info == nil || info.ID == "" {
		return
	}
	r.byID[info.ID] = info
	for _, alias := range info.Aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			continue
		}
		r.byAlias[alias] = info
	}
	// Keep ordered list de-duplicated and stable in registration order.
	for _, existing := range r.ordered {
		if existing.ID == info.ID {
			return
		}
	}
	r.ordered = append(r.ordered, info)
}

// All returns every registered provider in registration order. Used by
// the overview / provider-stats handlers and the dashboard provider grid.
func (r *providerRegistry) All() []*ProviderInfo {
	out := make([]*ProviderInfo, len(r.ordered))
	copy(out, r.ordered)
	return out
}

// IDs returns just the canonical ids — handy for code paths that only
// need the names ("antigravity", "kiro", ...) without bringing along
// metadata or executors.
func (r *providerRegistry) IDs() []string {
	out := make([]string, 0, len(r.ordered))
	for _, p := range r.ordered {
		out = append(out, p.ID)
	}
	return out
}

// ByID looks up a provider by its canonical id. Returns nil when the id
// is unknown so callers must always nil-check.
func (r *providerRegistry) ByID(id string) *ProviderInfo {
	return r.byID[id]
}

// ResolveModel turns a model string ("kr/claude-sonnet-4.5") into the
// matching ProviderInfo + the trailing model id without the alias
// prefix. When no alias matches we fall back to the first registered
// provider (compatible with the old behaviour where unknown models went
// to Antigravity by default).
func (r *providerRegistry) ResolveModel(model string) (*ProviderInfo, string) {
	for alias, info := range r.byAlias {
		prefix := alias + "/"
		if strings.HasPrefix(model, prefix) {
			return info, strings.TrimPrefix(model, prefix)
		}
	}
	if len(r.ordered) > 0 {
		return r.ordered[0], model
	}
	return nil, model
}
