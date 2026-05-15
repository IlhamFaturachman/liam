package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

// SSE Hub for broadcasting live requests to dashboard
type SSEHub struct {
	clients map[chan []byte]bool
	mu      sync.RWMutex
}

var sseHub = &SSEHub{clients: make(map[chan []byte]bool)}

// BroadcastRequest sends a request event to all SSE clients
func BroadcastRequest(event map[string]interface{}) {
	data, _ := json.Marshal(event)
	sseHub.mu.RLock()
	defer sseHub.mu.RUnlock()
	for ch := range sseHub.clients {
		select {
		case ch <- data:
		default:
			// Client too slow, skip
		}
	}
}

// HandleSSE serves the SSE endpoint for live request feed
func (s *Server) HandleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", 500)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := make(chan []byte, 50)
	sseHub.mu.Lock()
	sseHub.clients[ch] = true
	sseHub.mu.Unlock()

	defer func() {
		sseHub.mu.Lock()
		delete(sseHub.clients, ch)
		sseHub.mu.Unlock()
		close(ch)
	}()

	// Send keepalive every 30s
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case data := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// HandleUsageRecent returns last N requests
func (s *Server) HandleUsageRecent(w http.ResponseWriter, r *http.Request) {
	logs, err := s.db.GetRecentUsage(50)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, logs)
}

// HandleUsageStats returns aggregated stats for the requested period.
// Accepts ?period=today (default) | 24h | 7d | 30d | 60d.
func (s *Server) HandleUsageStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.db.GetUsageStats(r.URL.Query().Get("period"))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, stats)
}

// HandleUsageChart returns bucketed data for charts. Accepts the same
// ?period= filter as HandleUsageStats; the bucket size is picked
// automatically based on the period.
func (s *Server) HandleUsageChart(w http.ResponseWriter, r *http.Request) {
	buckets, err := s.db.GetUsageChart(r.URL.Query().Get("period"))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, buckets)
}

// HandleOverview returns a bundled snapshot used by the dashboard's
// homepage so the page doesn't have to fan out to six endpoints just
// to render. Bundles:
//   - per-provider health (active / cooldown / disabled counts)
//   - today's quick-glance usage stats (requests, in/out tokens, cost)
//   - top 5 models for the period
//   - recent 5 errors from the last 24h
//   - active model-lock count (a quick "system degraded?" signal)
//   - api keys count + sync status snapshot
func (s *Server) HandleOverview(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "today"
	}

	type providerHealth struct {
		Name     string `json:"name"`
		Label    string `json:"label,omitempty"`
		Icon     string `json:"icon,omitempty"`
		Total    int    `json:"total"`
		Active   int    `json:"active"`
		Cooldown int    `json:"cooldown"`
		Disabled int    `json:"disabled"`
	}

	// Always include the registered providers so cards render even
	// before any accounts are attached. Unknown providers picked up
	// from accounts data slot in after the registered ones.
	accounts, _ := s.db.ListAccounts("")
	known := []string{}
	infoByID := map[string]*ProviderInfo{}
	if s.providers != nil {
		for _, info := range s.providers.All() {
			known = append(known, info.ID)
			infoByID[info.ID] = info
		}
	}
	byName := map[string]*providerHealth{}
	for _, n := range known {
		ph := &providerHealth{Name: n}
		if info, ok := infoByID[n]; ok {
			ph.Label = info.Label
			ph.Icon = info.Icon
		}
		byName[n] = ph
	}
	for _, a := range accounts {
		ph, ok := byName[a.Provider]
		if !ok {
			ph = &providerHealth{Name: a.Provider}
			byName[a.Provider] = ph
		}
		ph.Total++
		switch a.Status {
		case "active":
			ph.Active++
		case "cooldown":
			ph.Cooldown++
		case "disabled":
			ph.Disabled++
		}
	}
	ordered := []*providerHealth{}
	for _, n := range known {
		ordered = append(ordered, byName[n])
		delete(byName, n)
	}
	for _, ph := range byName {
		ordered = append(ordered, ph)
	}

	// Best-effort: each helper returns ([], nil) on miss so the page
	// renders whatever subset succeeded.
	stats, _ := s.db.GetUsageStats(period)
	topModels, _ := s.db.GetTopModels(period, 5)
	recentErrors, _ := s.db.GetRecentErrors(5)
	activeLocks, _ := s.db.CountActiveModelLocks()
	keys, _ := s.db.ListAPIKeys()

	writeJSON(w, 200, map[string]interface{}{
		"period":         period,
		"providers":      ordered,
		"stats":          stats,
		"top_models":     topModels,
		"recent_errors":  recentErrors,
		"active_locks":   activeLocks,
		"api_keys_total": len(keys),
		"sync":           s.syncer.Status(),
		"server_time":    time.Now().UTC().Format(time.RFC3339),
	})
}

// HandleUsageDetail returns full detail for a single request
func (s *Server) HandleUsageDetail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, 400, "missing id")
		return
	}
	log, err := s.db.GetUsageDetail(id)
	if err != nil {
		writeError(w, 404, "not found")
		return
	}
	writeJSON(w, 200, log)
}

// HandleProviderStats returns per-provider statistics. The list of
// "known" providers is sourced from the runtime registry so adding a
// new backend automatically gets its card on the dashboard with zero
// frontend changes.
func (s *Server) HandleProviderStats(w http.ResponseWriter, r *http.Request) {
	accounts, _ := s.db.ListAccounts("")

	// Initialize with registered providers (always shown so the grid
	// doesn't reflow when accounts come and go).
	known := []string{}
	if s.providers != nil {
		known = s.providers.IDs()
	}
	providers := map[string]map[string]interface{}{}
	for _, p := range known {
		providers[p] = map[string]interface{}{
			"name":     p,
			"total":    0,
			"active":   0,
			"cooldown": 0,
			"disabled": 0,
		}
	}

	// Tally account stats
	for _, a := range accounts {
		p := a.Provider
		if _, ok := providers[p]; !ok {
			providers[p] = map[string]interface{}{
				"name":     p,
				"total":    0,
				"active":   0,
				"cooldown": 0,
				"disabled": 0,
			}
		}
		providers[p]["total"] = providers[p]["total"].(int) + 1
		switch a.Status {
		case "active":
			providers[p]["active"] = providers[p]["active"].(int) + 1
		case "cooldown":
			providers[p]["cooldown"] = providers[p]["cooldown"].(int) + 1
		case "disabled":
			providers[p]["disabled"] = providers[p]["disabled"].(int) + 1
		}
	}

	// Return ordered: registered providers first (in registration order),
	// then any unknown ones picked up from accounts data.
	result := []map[string]interface{}{}
	for _, p := range known {
		result = append(result, providers[p])
		delete(providers, p)
	}
	for _, v := range providers {
		result = append(result, v)
	}
	writeJSON(w, 200, result)
}
