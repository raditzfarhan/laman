//go:build windows

package desktop

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	runKey   = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue = "laman"
)

// UserName is the signed-in user's name for display: the account's full
// name (as on the Start menu) when Windows has one, else the username.
func UserName() string {
	// Domain and Entra accounts.
	n := uint32(256)
	buf := make([]uint16, n)
	if windows.GetUserNameEx(windows.NameDisplay, &buf[0], &n) == nil {
		if s := strings.TrimSpace(windows.UTF16ToString(buf[:n])); s != "" {
			return s
		}
	}
	// Local and Microsoft accounts keep it in the local account database.
	user := os.Getenv("USERNAME")
	if u, err := windows.UTF16PtrFromString(user); err == nil && user != "" {
		var info *byte
		if windows.NetUserGetInfo(nil, u, 10, &info) == nil {
			s := strings.TrimSpace(windows.UTF16PtrToString((*windows.UserInfo10)(unsafe.Pointer(info)).FullName))
			windows.NetApiBufferFree(info)
			if s != "" {
				return s
			}
		}
	}
	return user
}

// OpenURL opens a URL in the default browser.
func OpenURL(url string) error {
	return hidden(exec.Command("rundll32", "url.dll,FileProtocolHandler", url)).Start()
}

// OpenFolder opens a folder in Explorer.
func OpenFolder(path string) error {
	return exec.Command("explorer", path).Start()
}

// PickFolder shows Windows' folder picker, in front of other windows, and
// returns the chosen folder, or "" when the user cancels.
func PickFolder(title string) (string, error) {
	script := `Add-Type -AssemblyName System.Windows.Forms
$owner = New-Object System.Windows.Forms.Form -Property @{ TopMost = $true; ShowInTaskbar = $false; Width = 0; Height = 0; StartPosition = 'CenterScreen' }
$owner.Show(); $owner.Activate()
$d = New-Object System.Windows.Forms.FolderBrowserDialog
$d.Description = $env:LAMAN_PICK_TITLE
$d.ShowNewFolderButton = $true
if ($d.ShowDialog($owner) -eq [System.Windows.Forms.DialogResult]::OK) { [Console]::Out.Write($d.SelectedPath) }
$owner.Close()`
	cmd := hidden(exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-STA", "-Command", script))
	cmd.Env = append(os.Environ(), "LAMAN_PICK_TITLE="+title)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("folder picker: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Spawn starts a program without a console window and does not wait.
func Spawn(exe string, args ...string) error {
	cmd := exec.Command(exe, args...)
	cmd.Dir = filepath.Dir(exe) // don't hold the caller's folder open
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// RunHidden runs a program without a console window and waits for it.
func RunHidden(exe string, args ...string) ([]byte, error) {
	return hidden(exec.Command(exe, args...)).CombinedOutput()
}

func hidden(cmd *exec.Cmd) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	return cmd
}

// StartupEnabled reports whether the tray starts with Windows.
func StartupEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runValue)
	return err == nil
}

// SetStartup adds or removes the per-user Run entry that starts exe at logon.
func SetStartup(enabled bool, exe string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if enabled {
		return k.SetStringValue(runValue, `"`+exe+`"`)
	}
	if err := k.DeleteValue(runValue); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

// SingleInstance takes a per-session lock; ok is false if another process
// already holds it. The handle is held for the life of the process.
func SingleInstance(name string) (ok bool, err error) {
	n, err := windows.UTF16PtrFromString(`Local\` + name)
	if err != nil {
		return false, err
	}
	_, err = windows.CreateMutex(nil, false, n)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return false, nil
	}
	return err == nil, err
}

// TrayRunning reports whether a lamanw.exe tray process exists.
func TrayRunning() bool {
	out, err := RunHidden("tasklist", "/FI", "IMAGENAME eq "+WindowedName, "/NH")
	return err == nil && strings.Contains(strings.ToLower(string(out)), WindowedName)
}

// StopTray ends any running tray so its executable can be replaced.
func StopTray() error {
	_, err := RunHidden("taskkill", "/IM", WindowedName, "/F")
	return err
}

// Home is the user's profile folder, for display.
func Home() string { h, _ := os.UserHomeDir(); return h }

// LightTaskbar reports whether Windows draws the taskbar light, so the tray
// icon can use the dark mark. Windows' default (and the fallback) is dark.
func LightTaskbar() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("SystemUsesLightTheme")
	return err == nil && v == 1
}

// Junction points link at the directory target, replacing an existing
// junction. Unlike a symlink it needs neither admin rights nor Developer
// Mode. It refuses to replace a real directory.
func Junction(link, target string) error {
	if fi, err := os.Lstat(link); err == nil {
		if fi.Mode()&os.ModeSymlink == 0 && fi.Mode()&os.ModeIrregular == 0 {
			return fmt.Errorf("%s exists and is not a link; move it away first", link)
		}
		// Removing a junction deletes only the link, never the target.
		if err := os.Remove(link); err != nil {
			return err
		}
	}
	out, err := hidden(exec.Command("cmd", "/c", "mklink", "/J", link, target)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mklink /J: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
