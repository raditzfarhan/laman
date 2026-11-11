//go:build windows

package daemon

import (
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobHandle is held for the daemon's lifetime; closing it (including when the
// daemon crashes) kills every child process, so no stray php-cgi or MariaDB
// keeps ports busy.
var jobHandle windows.Handle

func killChildrenOnExit() error {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			// BREAKAWAY_OK lets a child that asks for it (CREATE_BREAKAWAY_FROM_JOB)
			// leave the job: a dashboard-started `laman upgrade` must outlive
			// the daemon it restarts. Everything else still dies with us.
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK,
		},
	}
	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(h)
		return err
	}
	if err := windows.AssignProcessToJobObject(h, windows.CurrentProcess()); err != nil {
		windows.CloseHandle(h)
		return err
	}
	jobHandle = h
	return nil
}

func noWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

// spawnOutsideJob starts a detached process that survives the daemon. It
// fails rather than starting inside the job, where it would be killed.
func spawnOutsideJob(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true,
		CreationFlags: windows.CREATE_BREAKAWAY_FROM_JOB | windows.DETACHED_PROCESS |
			windows.CREATE_NEW_PROCESS_GROUP,
	}
	return cmd.Start()
}
