package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"laman/internal/desktop"
	"laman/internal/update"
	"laman/internal/version"
)

func cmdVersion() {
	fmt.Println("laman", version.Version)
}

// cmdUpdate installs the newest release: it downloads laman.exe, checks
// its SHA-256 against the release's checksums.txt, and lets the new copy
// install itself (`self-install` stops laman, swaps the files, starts it
// again).
//
//	laman update            update when a newer release exists
//	laman update --check    only say whether one exists
//	laman update --force    also from a development build
func cmdUpdate(args []string) error {
	check, force := false, false
	for _, a := range args {
		switch a {
		case "--check", "check":
			check = true
		case "--force":
			force = true
		default:
			return fmt.Errorf("unknown option %q; usage: laman update [--check]", a)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	r, err := update.Latest(ctx)
	if errors.Is(err, update.ErrNoReleases) {
		fmt.Printf("laman %s: no release has been published yet, so there is nothing newer.\n", version.Version)
		return nil
	}
	if err != nil {
		return err
	}
	if version.Dev() && !force {
		fmt.Printf("This is a development build of laman. The newest release is %s (%s).\n", r.Version, r.URL)
		if !check {
			fmt.Println("Run `laman update --force` to replace this build with it.")
		}
		return nil
	}
	if !version.Dev() && !update.Newer(r.Version, version.Version) {
		fmt.Printf("laman %s is up to date.\n", version.Version)
		return nil
	}
	if check {
		fmt.Printf("laman %s is available (you have %s): %s\nRun `laman update` to install it.\n", r.Version, version.Version, r.URL)
		return nil
	}

	dir, err := os.MkdirTemp("", "laman-update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	fresh := filepath.Join(dir, "laman.exe")
	fmt.Printf("Downloading laman %s…\n", r.Version)
	if err := update.Download(ctx, r, fresh); err != nil {
		return err
	}

	// Windows won't overwrite a running .exe, but it will rename one. When
	// this command runs from the installed copy, move it aside first.
	dest := installedExe()
	old := dest + ".old"
	os.Remove(old) // left by the previous update
	self, _ := os.Executable()
	moved := false
	if self != "" && samePath(self, dest) {
		if err := os.Rename(dest, old); err != nil {
			return fmt.Errorf("could not move the running laman aside: %w", err)
		}
		moved = true
	}
	cmd := exec.Command(fresh, "self-install")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if _, statErr := os.Stat(dest); moved && errors.Is(statErr, os.ErrNotExist) {
			os.Rename(old, dest) // put the working copy back
		}
		return fmt.Errorf("installing laman %s: %w", r.Version, err)
	}
	fmt.Printf("Updated laman %s → %s. What's new: %s\n", version.Version, r.Version, r.URL)
	return nil
}

// cmdSelfUninstall undoes self-install: stops laman and the tray, stops
// starting at sign-in, takes laman off the PATH and removes its CA from
// Windows. Data stays in LAMAN_HOME; the setup program's uninstaller
// deletes the files after this.
func cmdSelfUninstall(args []string) error {
	quiet := len(args) > 0 && args[0] == "--quiet"
	if _, err := api("GET", "/status"); err == nil {
		if err := cmdStop(); err != nil {
			return err
		}
	}
	if desktop.TrayRunning() {
		desktop.StopTray()
	}
	if err := desktop.SetStartup(false, ""); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not remove the sign-in entry: %v\n", err)
	}
	bin := filepath.Dir(installedExe())
	if removed, err := removeFromUserPath(bin); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not take %s off your PATH: %v\n", bin, err)
	} else if removed {
		fmt.Printf("Removed %s from your PATH.\n", bin)
	}
	if err := cmdUntrust(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not remove laman's CA: %v\n", err)
	}
	if !quiet {
		fmt.Printf(`laman is uninstalled. Left in place:
  %s   your databases, PHP versions and settings (delete it to remove them)
  *.test lines in the Windows hosts file (harmless; remove them as admin if you like)
`, filepath.Dir(bin))
	}
	return nil
}
