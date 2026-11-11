//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

// spawnDetached starts the daemon with no console, in its own process group,
// so closing the terminal does not kill it. It first tries to break away from
// the caller's job object (some terminals kill their whole job on exit).
func spawnDetached(exe string, logf *os.File, args ...string) (*exec.Cmd, error) {
	base := uint32(windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP)
	var lastErr error
	for _, flags := range []uint32{base | windows.CREATE_BREAKAWAY_FROM_JOB, base} {
		cmd := exec.Command(exe, args...)
		// Not the caller's folder: the daemon would hold it open for as long
		// as it runs (the setup program's temp folder, say).
		cmd.Dir = filepath.Dir(exe)
		cmd.Stdout, cmd.Stderr = logf, logf
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags}
		if lastErr = cmd.Start(); lastErr == nil {
			return cmd, nil
		}
	}
	return nil, lastErr
}
