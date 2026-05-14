package harvest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sync"

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
	Running  bool                    `json:"running"`
	Provider string                  `json:"provider,omitempty"`
	Total    int                     `json:"total"`
	Success  int                     `json:"success"`
	Failed   int                     `json:"failed"`
	Logs     []string                `json:"logs"`
	Accounts []HarvestAccountStatus  `json:"accounts"`
}

type HarvestAccountStatus struct {
	Email   string `json:"email"`
	Status  string `json:"status"`  // pending, running, success, failed
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
	h.status = HarvestStatus{Running: true, Provider: provider, Logs: []string{}, Accounts: []HarvestAccountStatus{}}
	h.mu.Unlock()

	go h.runBatch(provider, accounts, concurrency, headless)
	return nil
}

func (h *HarvestService) runBatch(provider string, accounts string, concurrency int, headless bool) {
	defer func() {
		h.mu.Lock()
		h.running = false
		h.status.Running = false
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

	// Python script that runs batch and outputs JSON results line by line
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

print(json.dumps({"type":"status","total":len(accounts),"message":f"Loaded {len(accounts)} accounts"}), flush=True)

provider = get_provider('%s')
orch = Orchestrator()
orch.load_accounts(accounts)

def on_update(status):
    import asyncio
    print(json.dumps({"type":"progress","data":status}), flush=True)

async def run():
    await orch.start(provider=provider, concurrency=%d, headless=%s)
    # Output results
    for r in orch.results:
        print(json.dumps({"type":"result","data":r}), flush=True)
    for f in orch.failed:
        print(json.dumps({"type":"failed","data":f}), flush=True)
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
		// Initialize account list from accounts field if present
		if accs, ok := msg["accounts"].([]interface{}); ok {
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
		found := false
		for i := range h.status.Accounts {
			if h.status.Accounts[i].Email == email {
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
		h.mu.Unlock()

	case "progress":
		// Silent update

	case "result":
		data, _ := msg["data"].(map[string]interface{})
		if data != nil {
			h.mu.Lock()
			h.status.Success++
			h.mu.Unlock()
			h.importResult(data)
			email, _ := data["email"].(string)
			h.addLog(fmt.Sprintf("SUCCESS: %s", email))
		}

	case "failed":
		data, _ := msg["data"].(map[string]interface{})
		h.mu.Lock()
		h.status.Failed++
		h.mu.Unlock()
		if data != nil {
			email, _ := data["email"].(string)
			errMsg, _ := data["error"].(string)
			h.addLog(fmt.Sprintf("FAILED: %s - %s", email, errMsg))
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

// Ensure io import is used
var _ = io.EOF
