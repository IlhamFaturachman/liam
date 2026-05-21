package harvest

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed all:src
var harvestFS embed.FS

// ExtractHarvestModule extracts the embedded harvest python module to the target directory.
func ExtractHarvestModule(targetDir string) error {
	return fs.WalkDir(harvestFS, "src", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Skip the root "src" dir itself
		if path == "src" {
			return nil
		}

		// Calculate relative path
		relPath, err := filepath.Rel("src", path)
		if err != nil {
			return err
		}

		targetPath := filepath.Join(targetDir, relPath)

		if d.IsDir() {
			return os.MkdirAll(targetPath, 0755)
		}

		// Read file from embed
		data, err := harvestFS.ReadFile(path)
		if err != nil {
			return err
		}

		// Write to target
		return os.WriteFile(targetPath, data, 0644)
	})
}
