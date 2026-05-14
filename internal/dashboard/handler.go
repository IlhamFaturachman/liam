package dashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"time"

	"github.com/liam-auto/liam/internal/db"
)

// Handler serves the dashboard UI and auth endpoints
type Handler struct {
	database *db.Database
	staticFS http.Handler
}

// NewHandler creates a dashboard handler
func NewHandler(database *db.Database) *Handler {
	// Serve from embedded filesystem
	sub, _ := fs.Sub(StaticFS, "static")
	return &Handler{
		database: database,
		staticFS: http.FileServer(http.FS(sub)),
	}
}

// ServeStatic serves static files (index.html, app.js, style.css)
func (h *Handler) ServeStatic(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// For /dashboard or /dashboard/, serve index.html directly
	if path == "/dashboard" || path == "/dashboard/" {
		data, err := StaticFS.ReadFile("static/index.html")
		if err != nil {
			http.Error(w, "Not found", 404)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
		return
	}

	// For other files, strip prefix and serve from embedded FS
	filename := path[len("/dashboard/"):]
	data, err := StaticFS.ReadFile("static/" + filename)
	if err != nil {
		http.Error(w, "Not found", 404)
		return
	}

	// Set content type
	switch {
	case len(filename) > 3 && filename[len(filename)-3:] == ".js":
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	case len(filename) > 4 && filename[len(filename)-4:] == ".css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	default:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	w.Write(data)
}

// HandleLogin validates password and returns auth token
func (h *Handler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid request"})
		return
	}

	// Get stored password (default: 123456)
	storedPassword := h.database.GetSetting("dashboard_password", "123456")

	if req.Password != storedPassword {
		writeJSON(w, 401, map[string]string{"error": "invalid password"})
		return
	}

	// Generate token
	token := generateToken(storedPassword)

	writeJSON(w, 200, map[string]interface{}{
		"token":      token,
		"expires_at": time.Now().Add(7 * 24 * time.Hour).Unix(),
	})
}

// HandleVerify checks if a token is still valid
func (h *Handler) HandleVerify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid request"})
		return
	}

	storedPassword := h.database.GetSetting("dashboard_password", "123456")
	expected := generateToken(storedPassword)

	if req.Token != expected {
		writeJSON(w, 401, map[string]string{"error": "invalid token"})
		return
	}

	writeJSON(w, 200, map[string]string{"status": "valid"})
}

// HandleChangePassword changes the dashboard password
func (h *Handler) HandleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid request"})
		return
	}

	storedPassword := h.database.GetSetting("dashboard_password", "123456")

	if req.Current != storedPassword {
		writeJSON(w, 401, map[string]string{"error": "current password is incorrect"})
		return
	}

	if len(req.New) < 4 {
		writeJSON(w, 400, map[string]string{"error": "new password must be at least 4 characters"})
		return
	}

	h.database.SetSetting("dashboard_password", req.New)
	writeJSON(w, 200, map[string]string{"status": "password changed"})
}

// generateToken creates a deterministic token from password (changes when password changes)
func generateToken(password string) string {
	hash := sha256.Sum256([]byte("liam_dashboard_" + password + "_v1"))
	return "liam_" + hex.EncodeToString(hash[:])
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// Ensure fmt is used
var _ = fmt.Sprintf
