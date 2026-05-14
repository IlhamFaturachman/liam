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

// HandleUsageStats returns aggregated stats for a period
func (s *Server) HandleUsageStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.db.GetUsageStats()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, stats)
}

// HandleUsageChart returns bucketed data for charts
func (s *Server) HandleUsageChart(w http.ResponseWriter, r *http.Request) {
	buckets, err := s.db.GetUsageChart(24)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, buckets)
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

// HandleProviderStats returns per-provider statistics
func (s *Server) HandleProviderStats(w http.ResponseWriter, r *http.Request) {
	accounts, _ := s.db.ListAccounts("")

	providers := map[string]map[string]interface{}{}
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

	result := []map[string]interface{}{}
	for _, v := range providers {
		result = append(result, v)
	}
	writeJSON(w, 200, result)
}
