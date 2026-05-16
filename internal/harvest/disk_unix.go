//go:build !windows

package harvest

import "syscall"

// platformFreeDiskGB returns free space at `path` in gigabytes, using
// statfs on Unix-like systems. Best effort: any error returns 0 and
// lets the caller silently skip.
func platformFreeDiskGB(path string) (float64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	// Bavail is in blocks; Bsize bytes per block. Convert to GB.
	freeBytes := uint64(stat.Bsize) * stat.Bavail
	return float64(freeBytes) / (1024 * 1024 * 1024), nil
}
