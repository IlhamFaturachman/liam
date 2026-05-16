package harvest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/liam-auto/liam/internal/config"
	"github.com/liam-auto/liam/internal/db"
)

// HarvestService manages batch login from within the Go server
type HarvestService struct {
	cfg      *config.Config
	database *db.Database
	mu       sync.Mutex
	running  bool
	cmd      *exec.Cmd
	status   HarvestStatus
}

type HarvestStatus struct {
	Running   bool                   `json:"running"`
	Provider  string                 `json:"provider,omitempty"`
	Total     int                    `json:"total"`
	Success   int                    `json:"success"`
	Failed    int                    `json:"failed"`
	Active    int                    `json:"active"`     // workers currently running an account
	StartedAt int64                  `json:"started_at"` // unix epoch seconds, 0 when idle
	EndedAt   int64                  `json:"ended_at"`   // unix epoch seconds, 0 while running
	Logs      []string               `json:"logs"`
	Accounts  []HarvestAccountStatus `json:"accounts"`
}

type HarvestAccountStatus struct {
	Email   string `json:"email"`
	Status  string `json:"status"` // pending, running, success, failed
	Detail  string `json:"detail"`
	TimeSec int    `json:"time_sec"`
}

func NewHarvestService(cfg *config.Config, database *db.Database) *HarvestService {
	return &HarvestService{
		cfg:      cfg,
		database: database,
		status:   HarvestStatus{Logs: []string{}, Accounts: []HarvestAccountStatus{}},
	}
}

// StartBatch spawns the Python harvest process and streams results back
func (h *HarvestService) StartBatch(provider string, accounts string, concurrency int, headless bool) error {
	h.mu.Lock()
	if h.running {
		h.mu.Unlock()
		return fmt.Errorf("harvest already running")
	}
	h.running = true
	h.status = HarvestStatus{
		Running:   true,
		Provider:  provider,
		StartedAt: time.Now().Unix(),
		Logs:      []string{},
		Accounts:  []HarvestAccountStatus{},
	}
	h.mu.Unlock()

	go h.runBatch(provider, accounts, concurrency, headless)
	return nil
}

func (h *HarvestService) runBatch(provider string, accounts string, concurrency int, headless bool) {
	defer func() {
		h.mu.Lock()
		h.running = false
		h.status.Running = false
		h.status.Active = 0
		h.status.EndedAt = time.Now().Unix()
		h.mu.Unlock()
	}()

	harvestDir := GetHarvestDir()
	if harvestDir == "" {
		h.addLog("ERROR: harvest directory not found")
		return
	}

	python := GetVenvPython(harvestDir)
	if _, err := os.Stat(python); err != nil {
		h.addLog("ERROR: harvest not set up. Run: liam setup")
		return
	}

	// Write accounts to temp file
	tmpFile := harvestDir + "/results/_batch_input.txt"
	os.MkdirAll(harvestDir+"/results", 0755)
	if err := os.WriteFile(tmpFile, []byte(accounts), 0644); err != nil {
		h.addLog(fmt.Sprintf("ERROR: write accounts file: %v", err))
		return
	}
	defer os.Remove(tmpFile)

	// Python script that runs batch and outputs JSON results line by line.
	//
	// Important details:
	//   - We register `on_result` and `on_failed` so the Go side gets each
	//     account as soon as the worker finishes it. Without these the Go
	//     `result`/`failed` lines only emit AFTER `orch.start()` returns,
	//     which can be 10+ minutes for a 50-account batch — so the dashboard
	//     looks frozen the whole time and accounts don't land in the DB.
	//   - The progress callback uses `account_status` events: one line per
	//     status change keyed by email so the Go side can update the
	//     per-account table without re-shipping the entire status map every
	//     event (which would amplify N² for big batches).
	//   - All callbacks are PLAIN sync functions — orchestrator's
	//     `_maybe_await` handles both shapes, so we get cheap stdout writes
	//     without dragging an event loop through every progress tick.
	script := fmt.Sprintf(`
import sys, asyncio, json
sys.path.insert(0, '.')
from core.providers import get_provider
from core.orchestrator import Orchestrator
from utils.parser import parse_accounts

text = open('results/_batch_input.txt').read()
accounts = parse_accounts(text)
if not accounts:
    print(json.dumps({"type":"error","message":"No valid accounts found"}), flush=True)
    sys.exit(1)

print(json.dumps({
    "type":"status",
    "total":len(accounts),
    "message":f"Loaded {len(accounts)} accounts",
    "accounts":[a["email"] for a in accounts],
}), flush=True)

provider = get_provider('%s')
orch = Orchestrator()
orch.load_accounts(accounts)

# Track which accounts we've already pushed a row for so we can rate-limit
# noisy "running" status updates without dropping the first/last for each.
_last_status = {}

def on_update(status):
    # Aggregate snapshot — the Go side uses this to drive per-account rows.
    # We translate it into one "account_status" line per email so processLine
    # only has to update the affected row instead of replacing the table.
    for email, info in (status.get("accounts") or {}).items():
        new_status = info.get("status", "")
        new_detail = info.get("detail", "")
        new_time = info.get("time", 0)
        prev = _last_status.get(email)
        if prev == (new_status, new_detail):
            continue
        _last_status[email] = (new_status, new_detail)
        print(json.dumps({
            "type":"account_status",
            "email":email,
            "status":new_status,
            "detail":new_detail,
            "time_sec":new_time,
        }), flush=True)

def on_result(r):
    print(json.dumps({"type":"result","data":r}), flush=True)

def on_failed(f):
    print(json.dumps({"type":"failed","data":f}), flush=True)

orch.set_update_callback(on_update)
orch.set_result_callback(on_result)
orch.set_failed_callback(on_failed)

async def run():
    await orch.start(provider=provider, concurrency=%d, headless=%s)
    print(json.dumps({"type":"done","success":len(orch.results),"failed":len(orch.failed)}), flush=True)

asyncio.run(run())
`, provider, concurrency, boolToPython(headless))

	h.cmd = exec.Command(python, "-c", script)
	h.cmd.Dir = harvestDir

	stdout, err := h.cmd.StdoutPipe()
	if err != nil {
		h.addLog(fmt.Sprintf("ERROR: stdout pipe: %v", err))
		return
	}
	h.cmd.Stderr = h.cmd.Stdout // Merge stderr into stdout

	if err := h.cmd.Start(); err != nil {
		h.addLog(fmt.Sprintf("ERROR: start process: %v", err))
		return
	}

	h.addLog(fmt.Sprintf("Harvest started: provider=%s, concurrency=%d, headless=%v", provider, concurrency, headless))

	// Read output line by line
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer
	for scanner.Scan() {
		line := scanner.Text()
		h.processLine(line)
	}

	h.cmd.Wait()
	h.addLog("Harvest process finished")
}

func (h *HarvestService) processLine(line string) {
	var msg map[string]interface{}
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		h.addLog(line)
		return
	}

	msgType, _ := msg["type"].(string)
	switch msgType {
	case "status":
		message, _ := msg["message"].(string)
		total, _ := msg["total"].(float64)
		h.mu.Lock()
		h.status.Total = int(total)
		// Initialize account list from accounts field if present so the
		// dashboard can show every queued email immediately, with status
		// "pending" until the first worker picks it up.
		if accs, ok := msg["accounts"].([]interface{}); ok {
			h.status.Accounts = h.status.Accounts[:0]
			for _, a := range accs {
				if email, ok := a.(string); ok {
					h.status.Accounts = append(h.status.Accounts, HarvestAccountStatus{
						Email: email, Status: "pending", Detail: "-",
					})
				}
			}
		}
		h.mu.Unlock()
		h.addLog(message)

	case "account_status":
		email, _ := msg["email"].(string)
		status, _ := msg["status"].(string)
		detail, _ := msg["detail"].(string)
		timeSec, _ := msg["time_sec"].(float64)
		h.mu.Lock()
		// Track per-status transitions to keep an accurate `active` count
		// without needing a separate event from Python. The orchestrator
		// transitions are pending → running → success|failed (or retrying),
		// so we only flip the counter when the status changes.
		var prevStatus string
		found := false
		for i := range h.status.Accounts {
			if h.status.Accounts[i].Email == email {
				prevStatus = h.status.Accounts[i].Status
				h.status.Accounts[i].Status = status
				h.status.Accounts[i].Detail = detail
				h.status.Accounts[i].TimeSec = int(timeSec)
				found = true
				break
			}
		}
		if !found {
			h.status.Accounts = append(h.status.Accounts, HarvestAccountStatus{
				Email: email, Status: status, Detail: detail, TimeSec: int(timeSec),
			})
		}
		// Active workers transitions:
		//   anything → "running": active++
		//   "running" → success/failed/done: active--
		if prevStatus != "running" && status == "running" {
			h.status.Active++
		} else if prevStatus == "running" && (status == "success" || status == "failed") {
			if h.status.Active > 0 {
				h.status.Active--
			}
		}
		h.mu.Unlock()

	case "progress":
		// Reserved: per-tick aggregate snapshots. Currently unused — we
		// derive everything from `account_status` to keep the wire small
		// and avoid the N² status fanout of full-table updates.

	case "result":
		data, _ := msg["data"].(map[string]interface{})
		if data != nil {
			h.importResult(data)
			email, _ := data["email"].(string)
			h.addLog(fmt.Sprintf("SUCCESS: %s", email))
			h.mu.Lock()
			h.status.Success++
			h.mu.Unlock()
		}

	case "failed":
		data, _ := msg["data"].(map[string]interface{})
		if data != nil {
			email, _ := data["email"].(string)
			errMsg, _ := data["error"].(string)
			h.addLog(fmt.Sprintf("FAILED: %s - %s", email, errMsg))
			h.mu.Lock()
			h.status.Failed++
			h.mu.Unlock()
		}

	case "done":
		success, _ := msg["success"].(float64)
		failed, _ := msg["failed"].(float64)
		h.addLog(fmt.Sprintf("DONE: %d success, %d failed", int(success), int(failed)))

	case "error":
		message, _ := msg["message"].(string)
		h.addLog(fmt.Sprintf("ERROR: %s", message))
	}
}

// importResult auto-imports a harvest result into the DB
func (h *HarvestService) importResult(data map[string]interface{}) {
	provider, _ := data["provider"].(string)
	email, _ := data["email"].(string)
	creds, _ := data["credentials"].(map[string]interface{})

	if provider == "" || email == "" || creds == nil {
		return
	}

	credsJSON, _ := json.Marshal(creds)
	account := &db.Account{
		Provider:    provider,
		Email:       email,
		Status:      "active",
		Credentials: credsJSON,
	}

	if err := h.database.UpsertAccount(account); err != nil {
		h.addLog(fmt.Sprintf("IMPORT ERROR: %s - %v", email, err))
	}
}

func (h *HarvestService) addLog(msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.status.Logs = append(h.status.Logs, msg)
	// Keep last 200 logs
	if len(h.status.Logs) > 200 {
		h.status.Logs = h.status.Logs[len(h.status.Logs)-200:]
	}
}

// GetStatus returns current harvest status
func (h *HarvestService) GetStatus() HarvestStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.status
}

// Stop kills the harvest process
func (h *HarvestService) Stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cmd != nil && h.cmd.Process != nil {
		h.cmd.Process.Kill()
	}
	h.running = false
	h.status.Running = false
}

// --- HTTP Handlers (mounted on main router) ---

func (h *HarvestService) HandleStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider    string `json:"provider"`
		Accounts    string `json:"accounts"`
		Concurrency int    `json:"concurrency"`
		Headless    bool   `json:"headless"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
		return
	}
	if req.Provider == "" {
		req.Provider = "ag"
	}
	if req.Concurrency == 0 {
		req.Concurrency = 4
	}
	if req.Accounts == "" {
		writeJSON(w, 400, map[string]string{"error": "accounts field required"})
		return
	}

	if err := h.StartBatch(req.Provider, req.Accounts, req.Concurrency, req.Headless); err != nil {
		writeJSON(w, 409, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, 200, map[string]string{"status": "started"})
}

func (h *HarvestService) HandleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, h.GetStatus())
}

func (h *HarvestService) HandleStop(w http.ResponseWriter, r *http.Request) {
	h.Stop()
	writeJSON(w, 200, map[string]string{"status": "stopped"})
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func boolToPython(b bool) string {
	if b {
		return "True"
	}
	return "False"
}
