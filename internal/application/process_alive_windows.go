//go:build windows

package application

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// A read-only Windows process handle avoids Unix signals and never changes
// the state of a running application.
func processAlive(pid int64) (bool, error) {
	if pid <= 0 || pid > int64(^uint32(0)) {
		return false, errors.New("invalid process ID")
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return false, nil
		}
		return false, fmt.Errorf("inspect Windows process %d: %w", pid, err)
	}
	defer windows.CloseHandle(h)
	var exitCode uint32
	if err := windows.GetExitCodeProcess(h, &exitCode); err != nil {
		return false, fmt.Errorf("read Windows process status %d: %w", pid, err)
	}
	return exitCode == 259, nil // STILL_ACTIVE
}
