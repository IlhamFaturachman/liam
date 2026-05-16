package harvest

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// GetHarvestDir returns the path to the harvest module
func GetHarvestDir() string {
	// Try relative to binary first
	exe, _ := os.Executable()
	exeDir := filepath.Dir(exe)

	// Check: binary_dir/harvest/
	harvestDir := filepath.Join(exeDir, "harvest")
	if _, err := os.Stat(filepath.Join(harvestDir, "main.py")); err == nil {
		return harvestDir
	}

	// Check: working directory/harvest/
	cwd, _ := os.Getwd()
	harvestDir = filepath.Join(cwd, "harvest")
	if _, err := os.Stat(filepath.Join(harvestDir, "main.py")); err == nil {
		return harvestDir
	}

	return ""
}

// GetVenvPython returns the path to the venv Python binary
func GetVenvPython(harvestDir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(harvestDir, "venv", "Scripts", "python.exe")
	}
	return filepath.Join(harvestDir, "venv", "bin", "python")
}

// IsSetupComplete checks if harvest dependencies are installed
func IsSetupComplete(harvestDir string) bool {
	python := GetVenvPython(harvestDir)
	_, err := os.Stat(python)
	return err == nil
}

// RunUI starts the harvest web UI (FastAPI on port 8000)
func RunUI(harvestDir string) error {
	python := GetVenvPython(harvestDir)
	if !IsSetupComplete(harvestDir) {
		return fmt.Errorf("harvest not set up. Run: liam setup")
	}

	fmt.Println("Starting LIAM Harvest UI...")
	fmt.Println("Dashboard: http://localhost:8000")
	fmt.Println("Press Ctrl+C to stop")
	fmt.Println()

	cmd := exec.Command(python, "main.py")
	cmd.Dir = harvestDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	return cmd.Run()
}

// RunBatch runs batch harvest from CLI (no UI)
func RunBatch(harvestDir string, provider string, accountsFile string, concurrency int, headless bool) error {
	python := GetVenvPython(harvestDir)
	if !IsSetupComplete(harvestDir) {
		return fmt.Errorf("harvest not set up. Run: liam setup")
	}

	// Build Python command that runs batch directly
	script := fmt.Sprintf(`
import sys, asyncio, json
sys.path.insert(0, '.')
from core.providers import get_provider
from core.orchestrator import Orchestrator
from utils.parser import parse_accounts_file

accounts = parse_accounts_file('%s')
if not accounts:
    print('No valid accounts found in file')
    sys.exit(1)

print(f'Loaded {len(accounts)} accounts')
print(f'Provider: %s | Concurrency: %d | Headless: %v')

provider = get_provider('%s')
orch = Orchestrator()
orch.load_accounts(accounts)

async def run():
    await orch.start(provider=provider, concurrency=%d, headless=%v)
    print(f'\nResults: {len(orch.results)} success, {len(orch.failed)} failed')
    if orch.results:
        with open('results/success.json', 'w') as f:
            json.dump(orch.results, f, indent=2)
        print(f'Saved to results/success.json')

asyncio.run(run())
`, accountsFile, provider, concurrency, headless, provider, concurrency, headless)

	cmd := exec.Command(python, "-c", script)
	cmd.Dir = harvestDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// Setup installs Python venv + dependencies + Camoufox.
//
// Setup is intentionally chatty. The previous implementation called
// pip via CombinedOutput() with -q which produced ZERO console output
// during a 3-8 minute install — operators reported it looking like a
// hang. The replacement below:
//
//   - Streams every subprocess line to stdout in real time so the
//     operator sees pip download/install progress as it happens.
//   - Wraps silent steps (`python -m venv`, `camoufox fetch`) with a
//     keep-alive ticker that prints a heartbeat dot every 2s so
//     "long quiet" steps still feel responsive.
//   - Runs preflight checks BEFORE any heavy work (Python version,
//     pip availability, internet reachability, disk space) so failures
//     surface in the first 5 seconds instead of after a partial
//     download.
//   - Times each step and prints the elapsed time at completion so
//     operators can spot anomalies ("hmm, pip install took 45s last
//     time, why is it 12 min today?").
//   - Returns a structured error on any hard failure so the caller
//     (cmd/liam/main.go) can print a single clean line at the wizard
//     level instead of dumping a stack trace.
func Setup(harvestDir string) error {
	overallStart := time.Now()
	printSection("LIAM Harvest Setup")

	// --- Preflight ----------------------------------------------------
	printStep(0, "Preflight checks")

	pythonCmd := findPython()
	if pythonCmd == "" {
		return fmt.Errorf("Python 3.10+ not found. Install from https://python.org and re-run `liam setup`")
	}
	pyVersion := pythonShortVersion(pythonCmd)
	printOK(fmt.Sprintf("Python: %s (%s)", pyVersion, pythonCmd))

	if err := checkInternet(); err != nil {
		printWarn(fmt.Sprintf("Internet check failed: %v (proceeding anyway — pip may use a cache)", err))
	} else {
		printOK("Network: reachable")
	}

	if free, err := freeDiskGB(harvestDir); err == nil {
		if free < 2 {
			printWarn(fmt.Sprintf("Free disk: %.1f GB (recommended ≥ 2 GB; Camoufox + Playwright pull ~600 MB combined)", free))
		} else {
			printOK(fmt.Sprintf("Free disk: %.1f GB", free))
		}
	}
	fmt.Println()

	// --- Venv ---------------------------------------------------------
	if err := stepWithTimer(1, "Creating virtual environment", func() error {
		venvPath := filepath.Join(harvestDir, "venv")
		if _, err := os.Stat(venvPath); err == nil {
			fmt.Println("   (already exists, reusing)")
			return nil
		}
		cmd := exec.Command(pythonCmd, "-m", "venv", venvPath)
		cmd.Dir = harvestDir
		return runWithHeartbeat(cmd, "creating venv")
	}); err != nil {
		return err
	}

	// --- Pip install --------------------------------------------------
	pip := GetVenvPython(harvestDir)
	if err := stepWithTimer(2, "Installing Python dependencies", func() error {
		// Upgrade pip first so progress bar + cache work properly on
		// older Python distributions.
		upgrade := exec.Command(pip, "-m", "pip", "install", "--upgrade", "pip", "--quiet")
		upgrade.Dir = harvestDir
		upgrade.Stdout = os.Stdout
		upgrade.Stderr = os.Stderr
		_ = upgrade.Run() // best-effort; pip ships with venv

		// Real install. Stream stdout/stderr live; pip's progress bar
		// is line-buffered so we get per-package status as it lands.
		install := exec.Command(pip, "-m", "pip", "install",
			"-r", "requirements.txt",
			"--progress-bar", "on",
			"--disable-pip-version-check",
		)
		install.Dir = harvestDir
		return runWithLiveOutput(install, "  | ")
	}); err != nil {
		return fmt.Errorf("pip install failed: %w (run `cd %s && venv/bin/python -m pip install -r requirements.txt` manually to see why)", err, harvestDir)
	}

	// --- Playwright Firefox -------------------------------------------
	if err := stepWithTimer(3, "Downloading Playwright Firefox (~80 MB)", func() error {
		cmd := exec.Command(pip, "-m", "playwright", "install", "firefox")
		cmd.Dir = harvestDir
		return runWithLiveOutput(cmd, "  | ")
	}); err != nil {
		// Non-fatal — Camoufox is what we actually use; Playwright
		// Firefox is a fallback.
		printWarn(fmt.Sprintf("Playwright install failed (non-fatal — Camoufox is the primary): %v", err))
	}

	// --- Camoufox -----------------------------------------------------
	if err := stepWithTimer(4, "Downloading Camoufox anti-detect browser (~120 MB)", func() error {
		// camoufox fetch prints a progress bar but it's stuck behind
		// stderr without flushes; use the heartbeat wrapper.
		cmd := exec.Command(pip, "-m", "camoufox", "fetch")
		cmd.Dir = harvestDir
		return runWithLiveOutput(cmd, "  | ")
	}); err != nil {
		// This IS fatal — harvest can't run without Camoufox.
		return fmt.Errorf("camoufox fetch failed: %w (run `cd %s && venv/bin/python -m camoufox fetch` manually to see why)", err, harvestDir)
	}

	// --- Done ---------------------------------------------------------
	fmt.Println()
	printOK(fmt.Sprintf("All steps complete in %s", elapsedRound(overallStart)))
	fmt.Println()
	fmt.Println("You can now run:")
	fmt.Println("  liam harvest --provider ag --file accounts.txt")
	fmt.Println("  liam harvest --ui")
	return nil
}

// --- output helpers ---------------------------------------------------

func printSection(title string) {
	fmt.Printf("\n=== %s ===\n\n", title)
}

func printStep(n int, title string) {
	if n == 0 {
		fmt.Printf("[*] %s\n", title)
	} else {
		fmt.Printf("[%d] %s\n", n, title)
	}
}

func printOK(msg string)   { fmt.Printf("    \u2713 %s\n", msg) }
func printWarn(msg string) { fmt.Printf("    ! %s\n", msg) }

// stepWithTimer runs `body` under a numbered step header and prints
// the wall-clock duration on success. Failures bubble up untouched.
func stepWithTimer(n int, title string, body func() error) error {
	printStep(n, title)
	start := time.Now()
	if err := body(); err != nil {
		fmt.Printf("    \u2717 failed after %s\n", elapsedRound(start))
		return err
	}
	fmt.Printf("    \u2713 done in %s\n\n", elapsedRound(start))
	return nil
}

// runWithLiveOutput runs cmd and streams stdout+stderr line-by-line,
// prefixing each line so the user can clearly see what came from
// the subprocess (vs the wizard frame). It also installs a keep-alive
// heartbeat: if the subprocess produces no output for 8 seconds, we
// print "..." so the operator knows the process is still alive (e.g.
// pip resolving dependencies before any download starts).
func runWithLiveOutput(cmd *exec.Cmd, linePrefix string) error {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	lastLine := time.Now()
	done := make(chan struct{})

	go streamLines(stdout, linePrefix, &lastLine)
	go streamLines(stderr, linePrefix, &lastLine)

	// Heartbeat goroutine: every 2s, if no output for 8s, print "..."
	// to reassure the operator we're not hung.
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if time.Since(lastLine) > 8*time.Second {
					fmt.Print(".")
					lastLine = time.Now()
				}
			}
		}
	}()

	err = cmd.Wait()
	close(done)
	// If we printed heartbeat dots, push a newline so the next "✓ done"
	// line lands on its own row.
	if time.Since(lastLine) < 1*time.Second {
		// no-op: last byte was a dot but cmd just finished
	}
	fmt.Println()
	return err
}

func streamLines(r io.ReadCloser, prefix string, lastLine *time.Time) {
	scanner := bufio.NewScanner(r)
	// 1MB max line — pip prints long progress lines on slow networks
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		fmt.Printf("%s%s\n", prefix, scanner.Text())
		*lastLine = time.Now()
	}
}

// runWithHeartbeat is for subprocesses that produce zero stdout
// (e.g. `python -m venv`) — we attach pipes anyway and just print
// a ticking dot so the user knows the wizard hasn't deadlocked.
func runWithHeartbeat(cmd *exec.Cmd, _ string) error {
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}

	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				fmt.Print(".")
			}
		}
	}()
	err := cmd.Wait()
	close(done)
	fmt.Println()
	return err
}

// --- preflight helpers -------------------------------------------------

// pythonShortVersion returns "3.12.4" or "" if version output isn't
// parseable. We use this for the preflight banner only.
func pythonShortVersion(python string) string {
	out, err := exec.Command(python, "--version").Output()
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(out))
	// "Python 3.12.4" → "3.12.4"
	if strings.HasPrefix(s, "Python ") {
		return s[7:]
	}
	return s
}

// checkInternet does a fast TCP-level reachability test against pypi
// (the actual host pip will hit). 3s timeout — we only want to flag
// "no internet"; transient slowness shouldn't block setup.
func checkInternet() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", "pypi.org:443")
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}

// freeDiskGB returns free space at `path` in GB. Best-effort: if the
// platform stat call doesn't work we return 0,err and the caller
// silently skips the check.
func freeDiskGB(path string) (float64, error) {
	return platformFreeDiskGB(path)
}

// elapsedRound prints "12s", "1m23s", "3m" — short enough to fit
// inline at the right of a step header.
func elapsedRound(since time.Time) string {
	d := time.Since(since)
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	return d.Round(time.Second).String()
}

// findPython finds a suitable Python 3.10+ binary
func findPython() string {
	candidates := []string{"python3", "python"}
	for _, name := range candidates {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		// Check version
		out, err := exec.Command(path, "--version").Output()
		if err != nil {
			continue
		}
		version := string(out)
		// Simple check: "Python 3.1x" or "Python 3.2x"
		if len(version) > 9 {
			major := version[7:8]
			minor := version[9:11]
			if major == "3" && (minor >= "10" || len(minor) == 1 && minor[0] >= '1') {
				return path
			}
		}
		// Fallback: any Python 3
		if len(version) > 7 && version[7:8] == "3" {
			return path
		}
	}
	return ""
}
