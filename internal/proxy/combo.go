package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/liam-auto/liam/internal/db"
)

// ComboHandler manages combo CRUD and resolution
type ComboHandler struct {
	database      *db.Database
	rotationState map[string]*comboRotation
	mu            sync.Mutex
}

type comboRotation struct {
	index               int
	consecutiveUseCount int
}

func NewComboHandler(database *db.Database) *ComboHandler {
	return &ComboHandler{
		database:      database,
		rotationState: make(map[string]*comboRotation),
	}
}

// ResolveCombo checks if a model name is a combo and returns the rotated model list
// Returns nil if not a combo
func (ch *ComboHandler) ResolveCombo(modelName string) []string {
	combo, err := ch.database.GetCombo(modelName)
	if err != nil || combo == nil {
		return nil
	}

	if len(combo.Models) == 0 {
		return nil
	}

	if combo.Strategy == "round-robin" && len(combo.Models) > 1 {
		return ch.getRotatedModels(combo)
	}

	// Fallback strategy: return as-is (try in order)
	return combo.Models
}

// getRotatedModels returns models rotated based on round-robin state
func (ch *ComboHandler) getRotatedModels(combo *db.Combo) []string {
	ch.mu.Lock()
	defer ch.mu.Unlock()

	state, ok := ch.rotationState[combo.Name]
	if !ok {
		state = &comboRotation{index: 0, consecutiveUseCount: 0}
		ch.rotationState[combo.Name] = state
	}

	currentIndex := state.index % len(combo.Models)
	stickyLimit := combo.StickyLimit
	if stickyLimit <= 0 {
		stickyLimit = 1
	}

	state.consecutiveUseCount++
	if state.consecutiveUseCount >= stickyLimit {
		// Rotate to next model
		state.index = (currentIndex + 1) % len(combo.Models)
		state.consecutiveUseCount = 0
	}

	// Build rotated list (current first, then rest in order)
	rotated := make([]string, 0, len(combo.Models))
	for i := 0; i < len(combo.Models); i++ {
		idx := (currentIndex + i) % len(combo.Models)
		rotated = append(rotated, combo.Models[idx])
	}
	return rotated
}

// --- HTTP Handlers ---

func (ch *ComboHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	combos, err := ch.database.ListCombos()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if combos == nil {
		combos = []db.Combo{}
	}
	writeJSON(w, 200, combos)
}

func (ch *ComboHandler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string   `json:"name"`
		Models      []string `json:"models"`
		Strategy    string   `json:"strategy"`
		StickyLimit int      `json:"sticky_limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if req.Name == "" || len(req.Models) == 0 {
		writeError(w, 400, "name and models required")
		return
	}

	combo, err := ch.database.CreateCombo(req.Name, req.Strategy, req.Models, req.StickyLimit)
	if err != nil {
		writeError(w, 400, fmt.Sprintf("create failed: %v", err))
		return
	}
	writeJSON(w, 201, combo)
}

func (ch *ComboHandler) HandleUpdate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Name        string   `json:"name"`
		Models      []string `json:"models"`
		Strategy    string   `json:"strategy"`
		StickyLimit int      `json:"sticky_limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}

	if err := ch.database.UpdateCombo(id, req.Name, req.Strategy, req.Models, req.StickyLimit); err != nil {
		writeError(w, 500, err.Error())
		return
	}

	// Reset rotation state for this combo
	ch.mu.Lock()
	delete(ch.rotationState, req.Name)
	ch.mu.Unlock()

	writeJSON(w, 200, map[string]string{"status": "updated"})
}

func (ch *ComboHandler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := ch.database.DeleteCombo(id); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "deleted"})
}

// --- Routing Settings ---

func (s *Server) handleGetRouting(w http.ResponseWriter, r *http.Request) {
	strategy := s.db.GetSetting("routing_strategy", "round-robin")
	stickyStr := s.db.GetSetting("routing_sticky_limit", "3")
	overridesJSON := s.db.GetSetting("routing_provider_overrides", "{}")

	var overrides map[string]interface{}
	json.Unmarshal([]byte(overridesJSON), &overrides)

	writeJSON(w, 200, map[string]interface{}{
		"strategy":           strategy,
		"sticky_limit":       stickyStr,
		"provider_overrides": overrides,
	})
}

func (s *Server) handleSetRouting(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Strategy          string                 `json:"strategy"`
		StickyLimit       int                    `json:"sticky_limit"`
		ProviderOverrides map[string]interface{} `json:"provider_overrides"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}

	if req.Strategy != "" {
		s.db.SetSetting("routing_strategy", req.Strategy)
	}
	if req.StickyLimit > 0 {
		s.db.SetSetting("routing_sticky_limit", fmt.Sprintf("%d", req.StickyLimit))
	}
	if req.ProviderOverrides != nil {
		overJSON, _ := json.Marshal(req.ProviderOverrides)
		s.db.SetSetting("routing_provider_overrides", string(overJSON))
	}

	// Update pool config
	s.pool.UpdateStrategy(s.db)

	writeJSON(w, 200, map[string]string{"status": "saved"})
}

// HandleReorderAccounts handles drag-and-drop reorder
func (s *Server) handleReorderAccounts(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if len(req.IDs) == 0 {
		writeError(w, 400, "ids required")
		return
	}

	if err := s.db.ReorderAccounts(req.IDs); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "reordered"})
}

// Suppress unused import
