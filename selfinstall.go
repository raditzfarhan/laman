package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"laman/internal/config"
	"laman/internal/desktop"
	"laman/internal/version"
)

// installedExe is where self-install puts laman; its folder goes on PATH.
func installedExe() string { return filepath.Join(config.Home(), "bin", "laman.exe") }

// cmdSelfInstall copies the running laman.exe into LAMAN_HOME and puts that
// folder on the user's PATH, so `laman` works from any new terminal.
func cmdSelfInstall() error {
	src, err := os.Executable()
	if err != nil {
		return err
	}
	if src, err = filepath.EvalSymlinks(src); err != nil {
		return err
	}
	dest := installedExe()
	os.Remove(dest + ".old") // left by `laman update`; free once that copy exits

	if !samePath(src, dest) {
		// Windows will not overwrite a running .exe, and the daemon may be
		// running from the installed copy: stop it, copy, start it again.
		_, statusErr := api("GET", "/status")
		wasRunning := statusErr == nil
		if wasRunning {
			if err := cmdStop(); err != nil {
				return err
			}
		}
		// The tray runs lamanw.exe, which is rewritten below.
		trayWasRunning := desktop.TrayRunningAt(desktop.WindowedPath(dest))
		if trayWasRunning {
			desktop.StopTrayAt(desktop.WindowedPath(dest))
		}
		if err := copyFile(src, dest); err != nil {
			return fmt.Errorf("install %s: %w (is a `laman` command still running?)", dest, err)
		}
		if err := desktop.MakeWindowed(dest, desktop.WindowedPath(dest)); err != nil {
			return fmt.Errorf("install %s: %w", desktop.WindowedName, err)
		}
		fmt.Printf("Installed laman to %s\n", dest)
		if trayWasRunning {
			desktop.Spawn(desktop.WindowedPath(dest))
		}
		if wasRunning {
			start := exec.Command(dest, "start")
			start.Stdout, start.Stderr = os.Stdout, os.Stderr
			if err := start.Run(); err != nil {
				return fmt.Errorf("restart laman: %w", err)
			}
		}
	} else {
		fmt.Printf("laman is already installed at %s\n", dest)
		if _, err := desktop.EnsureWindowed(dest); err != nil {
			return fmt.Errorf("install %s: %w", desktop.WindowedName, err)
		}
	}

	if !version.Dev() {
		syncSetupVersion(version.Version, config.Home())
	}

	added, err := addToUserPath(filepath.Dir(dest))
	if err != nil {
		return fmt.Errorf("add to PATH: %w", err)
	}
	if added {
		fmt.Printf("Added %s to your PATH. Open a new terminal, then run `laman` from anywhere.\n", filepath.Dir(dest))
	} else if !onProcessPath(filepath.Dir(dest)) {
		fmt.Println("It is on your PATH; open a new terminal to pick it up.")
	}
	return nil
}

func copyFile(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dest + ".new"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	// A process that just stopped can keep its .exe locked for a moment
	// while Windows tears it down, so retry briefly before giving up.
	for i := 0; i < 20; i++ {
		if err = os.Rename(tmp, dest); err == nil {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	os.Remove(tmp)
	return err
}

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// pathWith returns path with dir appended, or ok=false if it is already
// there (compared case-insensitively, ignoring trailing slashes).
func pathWith(path, dir string) (string, bool) {
	norm := func(s string) string { return strings.ToLower(strings.TrimRight(strings.TrimSpace(s), `\/`)) }
	for _, p := range strings.Split(path, ";") {
		if norm(expandWinEnv(p)) == norm(dir) || norm(p) == norm(dir) {
			return path, false
		}
	}
	path = strings.TrimRight(path, ";")
	if path == "" {
		return dir, true
	}
	return path + ";" + dir, true
}

// pathWithout returns path without dir (compared like pathWith), and
// whether it was there.
func pathWithout(path, dir string) (string, bool) {
	norm := func(s string) string { return strings.ToLower(strings.TrimRight(strings.TrimSpace(s), `\/`)) }
	var keep []string
	removed := false
	for _, p := range strings.Split(path, ";") {
		if p != "" && (norm(expandWinEnv(p)) == norm(dir) || norm(p) == norm(dir)) {
			removed = true
			continue
		}
		keep = append(keep, p)
	}
	return strings.Join(keep, ";"), removed
}

func onProcessPath(dir string) bool {
	_, added := pathWith(os.Getenv("PATH"), dir)
	return !added
}

var errNoUserPath = errors.New("could not read the user PATH")

var winEnvVar = regexp.MustCompile(`%([^%;]+)%`)

// expandWinEnv expands %VAR% references the way Windows does for PATH.
func expandWinEnv(s string) string {
	return winEnvVar.ReplaceAllStringFunc(s, func(m string) string {
		if v, ok := os.LookupEnv(m[1 : len(m)-1]); ok {
			return v
		}
		return m
	})
}
