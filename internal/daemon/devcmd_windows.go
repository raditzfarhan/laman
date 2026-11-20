//go:build windows

package daemon

import (
	"os/exec"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// shellCmd runs a command line through cmd.exe, as a terminal would, so
// npm.cmd, pnpm.cmd and node_modules\.bin\*.cmd resolve. No window opens.
func shellCmd(line string) *exec.Cmd {
	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
		CmdLine:       `cmd.exe /d /s /c "` + line + `"`,
	}
	return cmd
}

// killTree ends a process and everything it started (cmd.exe -> npm ->
// node -> esbuild): killing cmd.exe alone would leave the server running.
func killTree(pid int) error {
	c := exec.Command("taskkill.exe", "/T", "/F", "/PID", strconv.Itoa(pid))
	noWindow(c)
	return c.Run()
}

// killChildren ends what an exited process left running: the processes it
// started, and theirs, found by parent id (Windows keeps that id after the
// parent dies). Only processes created after started count, so ones that
// belong to an earlier owner of a reused id are left alone. It terminates
// them directly: taskkill takes a second or two, which would delay noticing
// a crash.
func killChildren(pid int, started time.Time) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return
	}
	kids := map[uint32][]uint32{}
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		if pe.ProcessID != 0 && pe.ProcessID != pe.ParentProcessID {
			kids[pe.ParentProcessID] = append(kids[pe.ParentProcessID], pe.ProcessID)
		}
	}
	windows.CloseHandle(snap)
	seen := map[uint32]bool{}
	queue := kids[uint32(pid)]
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, id)
		if err != nil {
			continue
		}
		var created, exited, kernel, user windows.Filetime
		if windows.GetProcessTimes(h, &created, &exited, &kernel, &user) == nil &&
			!time.Unix(0, created.Nanoseconds()).Before(started) {
			windows.TerminateProcess(h, 1)
			queue = append(queue, kids[id]...)
		}
		windows.CloseHandle(h)
	}
}
