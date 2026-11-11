//go:build windows

package main

import (
	"errors"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// addToUserPath appends dir to HKCU\Environment\Path (no admin needed) and
// tells running programs the environment changed, so terminals opened from
// now on see it.
func addToUserPath(dir string) (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, "Environment", registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return false, err
	}
	defer k.Close()
	current, _, err := k.GetStringValue("Path")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return false, errors.Join(errNoUserPath, err)
	}
	next, added := pathWith(current, dir)
	if !added {
		return false, nil
	}
	// Keep REG_EXPAND_SZ so entries like %USERPROFILE%\... still expand.
	if err := k.SetExpandStringValue("Path", next); err != nil {
		return false, err
	}
	broadcastEnvChange()
	return true, nil
}

// removeFromUserPath takes dir out of HKCU\Environment\Path.
func removeFromUserPath(dir string) (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, "Environment", registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return false, err
	}
	defer k.Close()
	current, _, err := k.GetStringValue("Path")
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, errors.Join(errNoUserPath, err)
	}
	next, removed := pathWithout(current, dir)
	if !removed {
		return false, nil
	}
	if err := k.SetExpandStringValue("Path", next); err != nil {
		return false, err
	}
	broadcastEnvChange()
	return true, nil
}

func broadcastEnvChange() {
	env, _ := syscall.UTF16PtrFromString("Environment")
	const hwndBroadcast, wmSettingChange, smtoAbortIfHung = 0xffff, 0x001A, 0x0002
	var result uintptr
	sendMessageTimeout := windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")
	sendMessageTimeout.Call(hwndBroadcast, wmSettingChange, 0, uintptr(unsafe.Pointer(env)),
		smtoAbortIfHung, 5000, uintptr(unsafe.Pointer(&result)))
}

// savedPathDirs lists the PATH that new terminals get, in search order:
// the machine PATH first, then the user PATH, with %VARS% expanded.
func savedPathDirs() []string {
	var out []string
	for _, src := range []struct {
		root registry.Key
		key  string
	}{
		{registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`},
		{registry.CURRENT_USER, "Environment"},
	} {
		k, err := registry.OpenKey(src.root, src.key, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		v, _, _ := k.GetStringValue("Path")
		k.Close()
		for _, p := range strings.Split(v, ";") {
			if p = strings.TrimSpace(expandWinEnv(p)); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}
