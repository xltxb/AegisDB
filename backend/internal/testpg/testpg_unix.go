//go:build !windows

package testpg

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// processAlive reports whether pid names a running process (EPERM still
// means it exists, just not ours to signal).
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// watchdogScript waits for the test process ($1) to exit, then stops the
// instance it started ($2 = pg_ctl, $3 = data dir) and deletes the directory.
// It covers deaths that skip Main's deferred cleanup (a panicking test, the
// -timeout panic, SIGKILL). On a normal exit Main stops the instance and kills
// the watchdog first.
const watchdogScript = `while kill -0 "$1" 2>/dev/null; do sleep 1; done
"$2" stop -D "$3" -m immediate -w >/dev/null 2>&1
rm -rf "$3"`

func startWatchdog(pgCtl, dir string) *exec.Cmd {
	cmd := exec.Command("/bin/sh", "-c", watchdogScript, "testpg-watchdog", strconv.Itoa(os.Getpid()), pgCtl, dir)
	// Own process group: a terminal Ctrl-C must not kill it before it cleans up.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil
	}
	return cmd
}
