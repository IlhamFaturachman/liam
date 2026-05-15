package dashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
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
	sub, _ := fs.Sub(StaticFS, "static")
	return &Handler{
		database: database,
		staticFS: http.FileServer(http.FS(sub)),
	}
}

// ServeStatic serves static files (index.html, app.js, style.css)
func (h *Handler) ServeStatic(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
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
	filename := path[len("/dashboard/"):]
	data, err := StaticFS.ReadFile("static/" + filename)
	if err != nil {
		http.Error(w, "Not found", 404)
		return
	}
	switch {
	case strings.HasSuffix(filename, ".js"):
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	case strings.HasSuffix(filename, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	default:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	w.Write(data)
}

// HandleLogin validates password and returns auth token with expiry
func (h *Handler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid request"})
		return
	}

	storedPassword := h.database.GetSetting("dashboard_password", "123456")
	if req.Password != storedPassword {
		writeJSON(w, 401, map[string]string{"error": "invalid password"})
		return
	}

	token := generateToken(storedPassword)
	expiresAt := time.Now().Add(7 * 24 * time.Hour).Unix()

	writeJSON(w, 200, map[string]interface{}{
		"token":      token,
		"expires_at": expiresAt,
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
	if !validateDashboardToken(req.Token, storedPassword) {
		writeJSON(w, 401, map[string]string{"error": "invalid or expired token"})
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

// --- Token Generation & Validation (with expiry) ---

// generateToken creates a token with embedded timestamp (7-day expiry)
func generateToken(password string) string {
	ts := time.Now().Unix()
	hash := sha256.Sum256([]byte(fmt.Sprintf("liam_dashboard_%s_%d_v2", password, ts)))
	return fmt.Sprintf("liam_%s.%d", hex.EncodeToString(hash[:16]), ts)
}

// ValidateDashboardToken verifies token is valid and not expired (exported for proxy middleware)
func ValidateDashboardToken(token, password string) bool {
	return validateDashboardToken(token, password)
}

// validateDashboardToken verifies token is valid and not expired
func validateDashboardToken(token, password string) bool {
	if !strings.HasPrefix(token, "liam_") {
		return false
	}

	// Parse: liam_<hash>.<timestamp>
	withoutPrefix := token[5:]
	dotIdx := strings.LastIndex(withoutPrefix, ".")
	if dotIdx == -1 {
		// Legacy token format (no timestamp) — validate as before
		expected := sha256.Sum256([]byte("liam_dashboard_" + password + "_v1"))
		legacyToken := "liam_" + hex.EncodeToString(expected[:])
		return token == legacyToken
	}

	tsStr := withoutPrefix[dotIdx+1:]
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return false
	}

	// Check expiry (7 days)
	if time.Now().Unix()-ts > 7*24*3600 {
		return false
	}

	// Verify hash
	expectedHash := sha256.Sum256([]byte(fmt.Sprintf("liam_dashboard_%s_%d_v2", password, ts)))
	expectedToken := fmt.Sprintf("liam_%s.%d", hex.EncodeToString(expectedHash[:16]), ts)
	return token == expectedToken
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
