// Package desktop holds the Windows desktop integration: opening URLs and
// folders, the Start-with-Windows entry, the no-console tray executable and
// single-instance locking.
package desktop

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const (
	subsystemGUI     = 2
	subsystemConsole = 3
)

// WindowedName is the no-console copy of laman.exe, like pythonw/javaw.
const WindowedName = "lamanw.exe"

// MakeWindowed writes a copy of a console PE executable with its subsystem
// switched to GUI, so Windows starts it without opening a console window.
// One build serves both the CLI (laman.exe) and the tray (lamanw.exe).
func MakeWindowed(src, dest string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := setSubsystem(b, subsystemGUI); err != nil {
		return err
	}
	tmp := dest + ".new"
	if err := os.WriteFile(tmp, b, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func setSubsystem(b []byte, subsystem uint16) error {
	if len(b) < 0x40 || !bytes.Equal(b[:2], []byte("MZ")) {
		return errors.New("not a Windows executable")
	}
	pe := int(binary.LittleEndian.Uint32(b[0x3c:]))
	// PE signature (4) + COFF header (20) lead to the optional header, whose
	// Subsystem field sits at offset 68 in both PE32 and PE32+.
	off := pe + 4 + 20 + 68
	if pe < 0 || off+2 > len(b) || !bytes.Equal(b[pe:pe+4], []byte("PE\x00\x00")) {
		return errors.New("malformed PE header")
	}
	if cur := binary.LittleEndian.Uint16(b[off:]); cur != subsystemConsole && cur != subsystemGUI {
		return errors.New("unexpected PE subsystem")
	}
	binary.LittleEndian.PutUint16(b[off:], subsystem)
	return nil
}

// WindowedPath returns where lamanw.exe lives for a given laman.exe.
func WindowedPath(cliExe string) string {
	return filepath.Join(filepath.Dir(cliExe), WindowedName)
}

// CLIPath returns laman.exe next to the running executable (which may be
// lamanw.exe).
func CLIPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "laman.exe"
	}
	if strings.EqualFold(filepath.Base(exe), WindowedName) {
		return filepath.Join(filepath.Dir(exe), "laman.exe")
	}
	return exe
}

// IsWindowed reports whether this process is lamanw.exe.
func IsWindowed() bool {
	exe, err := os.Executable()
	return err == nil && strings.EqualFold(filepath.Base(exe), WindowedName)
}

// EnsureWindowed (re)creates lamanw.exe beside cliExe when it is missing or
// older than cliExe.
func EnsureWindowed(cliExe string) (string, error) {
	dest := WindowedPath(cliExe)
	src, err := os.Stat(cliExe)
	if err != nil {
		return "", err
	}
	if dst, err := os.Stat(dest); err == nil && !dst.ModTime().Before(src.ModTime()) {
		return dest, nil
	}
	return dest, MakeWindowed(cliExe, dest)
}
