package integrations

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// IsBinaryInstalled returns true if a CLI binary is in PATH
func IsBinaryInstalled(name string) bool {
	if name == "" {
		return false
	}
	_, err := exec.LookPath(name)
	return err == nil
}

// FileExists returns true if a file exists at the path
func FileExists(path string) bool {
	if path == "" {
		return false
	}
	expanded := ExpandHome(path)
	info, err := os.Stat(expanded)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// ExpandHome expands ~ in paths to the user's home directory
func ExpandHome(path string) string {
	if path == "" {
		return path
	}
	if path[0] != '~' {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if len(path) == 1 {
		return home
	}
	return filepath.Join(home, path[1:])
}

// IsToolInstalled checks if a tool is installed via binary OR config file presence
func IsToolInstalled(binaryName, configPath string) bool {
	if binaryName != "" && IsBinaryInstalled(binaryName) {
		return true
	}
	if FileExists(configPath) {
		return true
	}
	return false
}

// BackupFile creates a timestamped backup of a file before modification
// Keeps the last 3 backups, removes older ones
func BackupFile(path string) error {
	expanded := ExpandHome(path)
	if !FileExists(expanded) {
		return nil // Nothing to backup
	}

	ts := time.Now().Format("20060102-150405")
	backupPath := fmt.Sprintf("%s.bak.%s", expanded, ts)

	src, err := os.Open(expanded)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer src.Close()

	dst, err := os.Create(backupPath)
	if err != nil {
		return fmt.Errorf("create backup: %w", err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("copy: %w", err)
	}

	// Cleanup old backups (keep last 3)
	dir := filepath.Dir(expanded)
	base := filepath.Base(expanded)
	pattern := filepath.Join(dir, base+".bak.*")
	matches, _ := filepath.Glob(pattern)

	if len(matches) > 3 {
		// Sort by name (timestamp = natural sort)
		oldest := len(matches) - 3
		for i := 0; i < oldest; i++ {
			os.Remove(matches[i])
		}
	}

	return nil
}

// EnsureDir ensures a directory exists
func EnsureDir(path string) error {
	expanded := ExpandHome(path)
	return os.MkdirAll(filepath.Dir(expanded), 0755)
}

// Suppress unused import in build
var _ = runtime.GOOS
