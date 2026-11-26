//go:build windows

package testpg

import (
	"errors"
	"os/exec"
	"syscall"
)

const stillActive = 259 // STILL_ACTIVE

// processAlive reports whether pid names a running process.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return errors.Is(err, syscall.ERROR_ACCESS_DENIED)
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return true
	}
	return code == stillActive
}

// startWatchdog is unix-only; on Windows a crashed run's directory is
// removed by the next run's stale-directory sweep.
func startWatchdog(pgCtl, dir string) *exec.Cmd { return nil }
