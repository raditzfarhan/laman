package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"laman/internal/client"
	"laman/internal/config"
	"laman/internal/desktop"
	"laman/internal/install"
)

const appUsage = `usage:
  laman app                       list apps (installed and available)
  laman app install <name>        install dbzuper, adminer or phpmyadmin
  laman app install dbzuper --from <folder>  build DBZuper from a local checkout
  laman app update <name>         update to the newest version (keeps its data)
  laman app remove <name> [--yes] remove it and its data
  laman app open <name>           open it in the browser
  laman app default <name>        choose the database app behind db.test
  laman db                        open the default database app`

func cmdApp(args []string) error {
	if len(args) == 0 || args[0] == "list" {
		return listApps()
	}
	if len(args) < 2 {
		return errors.New(appUsage)
	}
	name := strings.ToLower(args[1])
	if install.AppByName(name) == nil {
		return fmt.Errorf("no app called %q; see `laman app`", args[1])
	}
	switch args[0] {
	case "install", "add", "update", "upgrade":
		from := ""
		for i := 2; i < len(args); i++ {
			switch {
			case args[i] == "--from" && i+1 < len(args):
				from = args[i+1]
				i++
			case strings.HasPrefix(args[i], "--from="):
				from = strings.TrimPrefix(args[i], "--from=")
			}
		}
		return installApp(name, args[0] == "update" || args[0] == "upgrade", from)
	case "remove", "uninstall":
		yes := len(args) > 2 && (args[2] == "--yes" || args[2] == "-y")
		return removeApp(name, yes)
	case "open":
		return openApp(name)
	case "default":
		return defaultDBApp(name)
	}
	return errors.New(appUsage)
}

// appURL is where a host is served, https when laman serves HTTPS.
func appURL(cfg *config.Config, host string) string {
	return siteURL(cfg.HTTPSPort > 0, cfg.HTTPPort, cfg.HTTPSPort)(host + "." + cfg.TLD)
}

func listApps() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "APP\tSTATE\tADDRESS\tWHAT")
	for _, a := range install.AppCatalog {
		state, addr := "not installed", ""
		if v, ok := cfg.Apps[a.Name]; ok {
			state, addr = v, appURL(cfg, a.Name)
			if cfg.DefaultDBClient() == a.Name {
				addr += "  (also " + appURL(cfg, "db") + ")"
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", a.Name, state, addr, a.About)
	}
	tw.Flush()
	fmt.Println("\nInstall one with `laman app install <name>`.")
	return nil
}

// appPHP picks the PHP version an app runs on: the default when it fits
// the app's range, else the newest installed one that does, else it
// installs the newest PHP line that fits.
func appPHP(cfg *config.Config, spec *install.AppSpec) (string, error) {
	if def := cfg.DefaultPHP(); def != "" && install.PHPFits(def, spec.PHP) {
		return def, nil
	}
	list := config.Installed("php")
	for i := len(list) - 1; i >= 0; i-- {
		if install.PHPFits(list[i], spec.PHP) {
			return list[i], nil
		}
	}
	lines, err := install.PHPLines()
	if err != nil {
		return "", err
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if install.PHPFits(lines[i], spec.PHP) {
			fmt.Printf("%s needs PHP %s; installing PHP %s for it\n", spec.Title, spec.PHP, lines[i])
			return install.PHP(lines[i])
		}
	}
	return "", fmt.Errorf("%s needs PHP %s, which windows.php.net no longer offers", spec.Title, spec.PHP)
}

// installApp installs or updates an app. from, for DBZuper development,
// builds it from a local source folder instead of the latest release.
func installApp(name string, update bool, from string) error {
	spec := install.AppByName(name)
	cfg, err := loadOrInit()
	if err != nil {
		return err
	}
	have, installed := cfg.Apps[name]
	if update && !installed {
		return fmt.Errorf("%s is not installed; run `laman app install %s`", spec.Title, name)
	}
	var latest string
	if from != "" {
		if name != "dbzuper" {
			return fmt.Errorf("--from only works for dbzuper")
		}
		abs, err := filepath.Abs(from)
		if err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(abs, "artisan")); err != nil {
			return fmt.Errorf("%s does not look like a DBZuper checkout (no artisan file)", abs)
		}
		from, latest = abs, "dev"
	} else if latest, err = install.LatestApp(spec); err != nil {
		return err
	}
	if installed && have == latest && from == "" {
		fmt.Printf("%s %s is already the newest version: %s\n", spec.Title, have, appURL(cfg, name))
		return nil
	}
	if installed && !update {
		fmt.Printf("%s %s is installed; updating to %s (its data is kept)\n", spec.Title, have, latest)
	}
	php, err := appPHP(cfg, spec)
	if err != nil {
		return err
	}
	ctx := &install.AppCtx{
		PHP:  filepath.Join(config.BinDir("php"), php, "php.exe"),
		Host: name + "." + cfg.TLD, URL: strings.TrimSuffix(appURL(cfg, name), "/"),
		MySQL: cfg.MySQLPort, Postgre: cfg.PostgresPort, Redis: cfg.RedisPort,
		HasMariaDB: config.Latest("mariadb") != "", HasPostgres: config.Latest("postgres") != "",
		UserName: accountName(), UserEmail: gitConfig("user.email"),
		From: from,
		Out:  func(f string, a ...any) { fmt.Printf(f, a...) },
	}
	note, err := install.InstallApp(spec, latest, ctx)
	if err != nil {
		return err
	}
	if cfg.Apps == nil {
		cfg.Apps = map[string]string{}
	}
	cfg.Apps[name] = latest
	// Run it on that PHP even if the default changes later.
	if php != cfg.DefaultPHP() {
		cfg.Isolated[name] = php
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	notifyDaemon()
	fmt.Printf("\n%s %s is at %s", spec.Title, latest, appURL(cfg, name))
	if cfg.DefaultDBClient() == name {
		fmt.Printf(" and %s", appURL(cfg, "db"))
	}
	fmt.Println()
	if note != "" {
		fmt.Println(note)
	}
	if !client.Running() {
		fmt.Println("Start laman (`laman start`) to use it.")
	} else {
		fmt.Println("If the address does not open yet, run `laman hosts` once (asks for admin).")
	}
	return nil
}

func removeApp(name string, yes bool) error {
	spec := install.AppByName(name)
	cfg, err := loadOrInit()
	if err != nil {
		return err
	}
	if _, ok := cfg.Apps[name]; !ok {
		fmt.Printf("%s is not installed\n", spec.Title)
		return nil
	}
	if !yes {
		fmt.Printf("Remove %s and everything it stored (accounts, saved connections)? [y/N] ", spec.Title)
		answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			fmt.Println("Left it installed.")
			return nil
		}
	}
	delete(cfg.Apps, name)
	delete(cfg.Isolated, name)
	if cfg.DBClient == name {
		cfg.DBClient = ""
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	notifyDaemon() // stop serving it before deleting its files
	if err := install.RemoveApp(name); err != nil {
		return err
	}
	fmt.Printf("Removed %s\n", spec.Title)
	return nil
}

func openApp(name string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if _, ok := cfg.Apps[name]; !ok {
		return fmt.Errorf("%s is not installed; run `laman app install %s`", name, name)
	}
	url := appURL(cfg, name)
	fmt.Println("Opening", url)
	return desktop.OpenURL(url)
}

func defaultDBApp(name string) error {
	spec := install.AppByName(name)
	if spec.Category != "database" {
		return fmt.Errorf("%s is not a database app", spec.Title)
	}
	cfg, err := loadOrInit()
	if err != nil {
		return err
	}
	if _, ok := cfg.Apps[name]; !ok {
		return fmt.Errorf("%s is not installed; run `laman app install %s`", spec.Title, name)
	}
	cfg.DBClient = name
	if err := cfg.Save(); err != nil {
		return err
	}
	notifyDaemon()
	fmt.Printf("%s now opens %s\n", appURL(cfg, "db"), spec.Title)
	return nil
}

// cmdDB opens the default database app, offering to install DBZuper.
func cmdDB() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.DefaultDBClient() == "" {
		return errors.New("no database app is installed; run `laman app install dbzuper` (or adminer, phpmyadmin)")
	}
	url := appURL(cfg, "db")
	fmt.Println("Opening", url)
	return desktop.OpenURL(url)
}

// accountName is who an app's account is for: git's user.name, else the
// Windows account's full name, else the Windows username.
func accountName() string {
	if n := gitConfig("user.name"); n != "" {
		return n
	}
	return desktop.UserName()
}

// gitConfig reads a global git setting, or "" without git.
func gitConfig(key string) string {
	out, err := exec.Command("git", "config", "--global", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
