//go:build windows

package daemon

import (
	"os/exec"
	"strconv"
	"syscall"

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
