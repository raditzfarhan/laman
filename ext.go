package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"laman/internal/client"
	"laman/internal/config"
	"laman/internal/install"
)

const extUsage = `usage:
  laman ext [--php 8.4]                       list extensions (default: the PHP of this folder)
  laman ext enable|disable <name>... [--php 8.4]
  laman ext install|uninstall <name>... [--php 8.4]
  laman xdebug on|off [--php 8.4]`

// extPHP takes --php off the arguments and resolves the PHP version to
// work on: the given one, else the one the current folder's site uses.
func extPHP(args []string) (string, []string, error) {
	var rest []string
	want := ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--php" && i+1 < len(args):
			want = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--php="):
			want = strings.TrimPrefix(args[i], "--php=")
		default:
			rest = append(rest, args[i])
		}
	}
	if want != "" {
		v := config.Match("php", want)
		if v == "" {
			return "", nil, fmt.Errorf("PHP %s is not installed", want)
		}
		return v, rest, nil
	}
	exe, err := phpHere()
	if err != nil {
		return "", nil, err
	}
	return filepath.Base(filepath.Dir(exe)), rest, nil
}

func cmdExt(args []string) error {
	v, args, err := extPHP(args)
	if err != nil {
		return err
	}
	if len(args) == 0 || args[0] == "list" {
		return listExt(v)
	}
	action, names := args[0], args[1:]
	if len(names) == 0 {
		return errors.New(extUsage)
	}
	short := strings.TrimSuffix(v, "-1")
	for _, name := range names {
		switch action {
		case "enable", "on":
			err = install.SetExt(v, name, true)
		case "disable", "off":
			err = install.SetExt(v, name, false)
		case "install", "add":
			err = install.InstallExt(v, name)
		case "uninstall", "remove":
			// Turn it off and restart PHP first: Windows will not delete a
			// DLL that a running php-cgi has loaded.
			if err = install.SetExt(v, name, false); err == nil {
				if err = restartPHPIfRunning(v); err == nil {
					err = install.UninstallExt(v, name)
				}
			}
		default:
			return errors.New(extUsage)
		}
		if err != nil {
			return err
		}
		past := map[string]string{"enable": "enabled", "on": "enabled", "disable": "disabled", "off": "disabled",
			"install": "installed and enabled", "add": "installed and enabled", "uninstall": "uninstalled", "remove": "uninstalled"}[action]
		fmt.Printf("%s %s for PHP %s\n", name, past, short)
	}
	return restartPHPIfRunning(v)
}

// restartPHPIfRunning makes laman's workers for that PHP reread php.ini.
// `laman php` and `laman composer` read it on every run anyway.
func restartPHPIfRunning(v string) error {
	if !client.Running() {
		return nil
	}
	_, err := client.Do("POST", "/api/php/restart", map[string]string{"php": v})
	return err
}

func listExt(v string) error {
	list, err := install.ListExt(v)
	if err != nil {
		return err
	}
	fmt.Printf("PHP %s extensions (%s)\n\n", strings.TrimSuffix(v, "-1"), filepath.Join(config.BinDir("php"), v, "php.ini"))
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSTATE\tSOURCE\tWHAT")
	for _, e := range list {
		state := "off"
		switch {
		case e.Enabled:
			state = "on"
		case !e.Present:
			state = "not installed"
		}
		source := "ships with PHP"
		if e.Installable {
			source = "extra (laman ext install)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Name, state, source, e.Note)
	}
	return tw.Flush()
}

// cmdXdebug turns Xdebug on or off, installing it the first time.
func cmdXdebug(args []string) error {
	v, args, err := extPHP(args)
	if err != nil {
		return err
	}
	if len(args) != 1 || (args[0] != "on" && args[0] != "off") {
		return errors.New("usage: laman xdebug on|off [--php 8.4]")
	}
	if args[0] == "on" {
		err = install.InstallExt(v, "xdebug") // just enables it when present
	} else {
		err = install.SetExt(v, "xdebug", false)
	}
	if err != nil {
		return err
	}
	if err := restartPHPIfRunning(v); err != nil {
		return err
	}
	if args[0] == "on" {
		fmt.Printf("Xdebug is on for PHP %s. It steps in when a request asks for it (browser extension or XDEBUG_TRIGGER), on port 9003.\n", strings.TrimSuffix(v, "-1"))
	} else {
		fmt.Printf("Xdebug is off for PHP %s.\n", strings.TrimSuffix(v, "-1"))
	}
	return nil
}
