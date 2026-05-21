//go:build windows

package harvest

import (
	"syscall"
	"unsafe"
)

// platformFreeDiskGB queries Windows' GetDiskFreeSpaceExW. We resolve
// the kernel32 symbol lazily so non-harvest code paths don't pay for
// it at startup.
func platformFreeDiskGB(path string) (float64, error) {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	getDiskFreeSpaceEx := kernel32.NewProc("GetDiskFreeSpaceExW")

	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}

	var freeBytesAvailable uint64
	r1, _, callErr := getDiskFreeSpaceEx.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		0,
		0,
	)
	if r1 == 0 {
		return 0, callErr
	}
	return float64(freeBytesAvailable) / (1024 * 1024 * 1024), nil
}
