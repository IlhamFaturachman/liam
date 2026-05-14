package harvest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// Setup installs Python venv + dependencies + Camoufox
func Setup(harvestDir string) error {
	fmt.Println("=== LIAM Harvest Setup ===")
	fmt.Println()

	// Step 1: Check Python
	fmt.Print("1. Checking Python 3.10+... ")
	pythonCmd := findPython()
	if pythonCmd == "" {
		return fmt.Errorf("Python 3.10+ not found. Install Python first.")
	}
	fmt.Printf("OK (%s)\n", pythonCmd)

	// Step 2: Create venv
	venvPath := filepath.Join(harvestDir, "venv")
	if _, err := os.Stat(venvPath); err != nil {
		fmt.Print("2. Creating virtual environment... ")
		cmd := exec.Command(pythonCmd, "-m", "venv", venvPath)
		cmd.Dir = harvestDir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed: %s\n%s", err, string(out))
		}
		fmt.Println("OK")
	} else {
		fmt.Println("2. Virtual environment already exists... OK")
	}

	// Step 3: Install dependencies
	fmt.Print("3. Installing dependencies... ")
	pip := GetVenvPython(harvestDir)

	installCmd := exec.Command(pip, "-m", "pip", "install", "-r", "requirements.txt", "-q")
	installCmd.Dir = harvestDir
	if out, err := installCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed: %s\n%s", err, string(out))
	}
	fmt.Println("OK")

	// Step 4: Install Playwright Firefox
	fmt.Print("4. Installing Playwright Firefox... ")
	pwCmd := exec.Command(pip, "-m", "playwright", "install", "firefox")
	pwCmd.Dir = harvestDir
	if out, err := pwCmd.CombinedOutput(); err != nil {
		fmt.Printf("Warning: %s\n", string(out))
	} else {
		fmt.Println("OK")
	}

	// Step 5: Download Camoufox
	fmt.Print("5. Downloading Camoufox browser... ")
	cfCmd := exec.Command(pip, "-m", "camoufox", "fetch")
	cfCmd.Dir = harvestDir
	if out, err := cfCmd.CombinedOutput(); err != nil {
		fmt.Printf("Warning: %s\n", string(out))
	} else {
		fmt.Println("OK")
	}

	fmt.Println()
	fmt.Println("=== Setup Complete! ===")
	fmt.Println()
	fmt.Println("You can now run:")
	fmt.Println("  liam harvest --provider ag --file accounts.txt")
	fmt.Println("  liam harvest --ui")

	return nil
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
