// laman is a local PHP development environment for Windows: it installs PHP
// and MariaDB, has Caddy built in, and serves every folder in ~/Sites as
// <name>.test.
package main

//go:generate go run winres/gen.go

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/caddyserver/caddy/v2"

	"laman/internal/caddyconf"
	"laman/internal/certs"
	"laman/internal/client"
	"laman/internal/config"
	"laman/internal/daemon"
	"laman/internal/desktop"
	"laman/internal/editor"
	"laman/internal/hosts"
	"laman/internal/install"
	"laman/internal/sites"
	"laman/internal/tray"
)

const usage = `laman: a local PHP dev environment for Windows

Usage:
  laman setup                     install PHP and MariaDB, create the sites folder
  laman install php [8.4]         install a PHP line (newest when omitted)
  laman install mariadb [11.4]    install MariaDB (newest LTS when omitted)
  laman versions                  list installed versions
  laman use php <8.3>             set the default PHP version
  laman use mariadb|redis|postgres <ver>  switch the version (each line keeps its own data)
  laman enable|disable <service>  turn mariadb, postgres, redis or mailpit on/off (now and at every start)
  laman upgrade mariadb|redis|postgres [v]  move to a newer line with your data (old data kept)
  laman isolate <8.3> [site]      pin a site (default: the one you're in) to a PHP version
  laman unisolate [site]          return a site to the default PHP version
  laman php [args]                run the PHP CLI matching the site you're in
  laman composer [args]           run Composer with that PHP (installs Composer on first use)
  laman ext [--php 8.4]           list PHP extensions; ext enable|disable|install|uninstall <name>
  laman new <name> [starter]      start a project: laravel, laravel-react|vue|livewire, wordpress, php, vite
  laman app [install|update|remove|open] <name>  web apps: dbzuper, adminer, phpmyadmin
  laman db                        open the default database app (https://db.test)
  laman xdebug on|off             turn Xdebug on/off for this folder's PHP (installs it once)
  laman install composer          install or update Composer
  laman install mailpit           install Mailpit, which catches the mail your apps send
  laman mail                      open the Mailpit inbox
  laman share [site]              share a site on a public https link (Cloudflare); share stop|list|setup
  laman install redis             install Redis (and the phpredis extension for each PHP)
  laman install postgres [17]     install PostgreSQL (newest when omitted)
  laman psql [args]               run psql against laman's PostgreSQL
  laman redis [args]              run redis-cli against laman's Redis
  laman install node [22]         install Node.js (newest LTS when omitted; also ranges like ">=22.13")
  laman use node <22>             set the default Node (plain node/npm/npx in new terminals)
  laman node|npm|npx [args]       run with the project's Node (.nvmrc, else package.json engines, else the default)
  laman install bun               install bun, for projects that use it
  laman bun|bunx [args]           run laman's bun with the project's Node on PATH

  laman start | stop | restart    run or stop the background services
  laman status                    show services and sites

  laman sites                     list sites and URLs
  laman folders [add|remove] [path]  project folders: every folder inside one is a site
  laman link [name] [path]        serve one folder outside your project folders (default: current dir)
  laman unlink <name>             remove a link
  laman dev [start|stop|log] [site]  run a Node app's dev server behind its .test name
  laman proxy <name> <port>       serve <name>.test from a port (Docker, your own server)
  laman hosts                     write *.test entries to the hosts file (asks for admin)
  laman trust [--reset]           trust laman's local CA so https:// works without warnings
  laman untrust                   remove the CA from the Windows certificate store
  laman config                    print the config file path and contents
  laman editor [name]             list code editors, or pick the one the dashboard opens
  laman self-install              copy laman into ~/.laman/bin and add it to your PATH
  laman self-uninstall            stop laman, take it off PATH and sign-in, remove its CA (keeps data)
  laman update [--check]          install the newest laman release
  laman version                   print laman's version
  laman tray                      show the laman icon in the Windows notification area
  laman dashboard                 open the dashboard in your browser

Data lives in %LAMAN_HOME% (default ~/.laman).
`

func main() {
	// lamanw.exe (the no-console copy) started with no arguments, e.g. at
	// sign-in, is the tray.
	if desktop.IsWindowed() && len(os.Args) < 2 {
		tray.Run()
		return
	}
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "setup":
		err = cmdSetup()
	case "install":
		err = cmdInstall(args)
	case "versions":
		cmdVersions()
	case "use":
		err = cmdUse(args)
	case "start":
		err = cmdStart()
	case "stop":
		err = cmdStop()
	case "restart":
		if err = cmdStop(); err == nil {
			err = cmdStart()
		}
	case "status":
		err = cmdStatus()
	case "sites":
		err = cmdSites()
	case "folders", "folder":
		err = cmdFolders(args)
	case "link":
		err = cmdLink(args)
	case "unlink":
		err = cmdUnlink(args)
	case "hosts":
		err = cmdHosts(args)
	case "self-install":
		err = cmdSelfInstall()
	case "self-uninstall":
		err = cmdSelfUninstall(args)
	case "update", "self-update":
		err = cmdUpdate(args)
	case "version", "--version", "-v":
		cmdVersion()
	case "tray":
		err = cmdTray()
	case "tray-run":
		tray.Run()
	case "dashboard":
		err = cmdDashboard()
	case "isolate":
		err = cmdIsolate(args)
	case "unisolate":
		err = cmdUnisolate(args)
	case "php":
		err = cmdPHP(args)
	case "composer":
		err = cmdComposer(args)
	case "mail":
		err = cmdMail()
	case "share":
		err = cmdShare(args)
	case "dev":
		err = cmdDev(args)
	case "proxy":
		err = cmdProxy(args)
	case "psql":
		err = cmdPsql(args)
	case "redis", "redis-cli":
		err = cmdRedis(args)
	case "new":
		err = cmdNew(args)
	case "app", "apps":
		err = cmdApp(args)
	case "db":
		err = cmdDB()
	case "ext", "extension", "extensions":
		err = cmdExt(args)
	case "xdebug":
		err = cmdXdebug(args)
	case "enable", "disable":
		err = cmdEnable(args, cmd == "enable")
	case "upgrade":
		err = cmdUpgrade(args)
	case "node", "npm", "npx", "bun", "bunx":
		err = cmdNode(cmd, args)
	case "trust":
		err = cmdTrust(args)
	case "untrust":
		err = cmdUntrust()
	case "config":
		err = cmdConfig()
	case "editor":
		err = cmdEditor(args)
	case "daemon":
		err = daemon.Run()
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		err = fmt.Errorf("unknown command %q; run `laman help`", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "laman:", err)
		os.Exit(1)
	}
}

func loadOrInit() (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if _, statErr := os.Stat(config.Path()); errors.Is(statErr, os.ErrNotExist) {
		err = cfg.Save()
	}
	return cfg, err
}

func cmdSetup() error {
	cfg, err := loadOrInit()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.SitesDir, 0o755); err != nil {
		return err
	}
	php, err := install.PHP("")
	if err != nil {
		return err
	}
	if cfg.PHP == "" {
		cfg.PHP = php
	}
	if err := install.PinVersions(cfg); err != nil {
		return err
	}
	mv, err := install.MariaDB("")
	if err != nil {
		return err
	}
	if cfg.MariaDB == "" {
		cfg.MariaDB = config.Line(mv)
	}
	if err := install.InitMariaDB(config.Active("mariadb", cfg.MariaDB), cfg.MySQLPort); err != nil {
		return err
	}
	if _, err := install.Composer(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not install Composer (`laman composer` will retry): %v\n", err)
	}
	if _, err := install.Mailpit(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not install Mailpit (try `laman install mailpit`): %v\n", err)
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	if err := cmdSelfInstall(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not put laman on your PATH: %v\n", err)
	}
	if v, err := install.Node(""); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not install Node (try `laman install node`): %v\n", err)
	} else if cfg.DefaultNode() == v || cfg.Node == "" {
		if err := setDefaultNode(cfg, v); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not make Node %s the default: %v\n", v, err)
		}
	}
	fmt.Printf(`
Done. Next (in a new terminal):
  1. Put a project in %s  (a folder "blog" becomes https://blog.%s)
  2. laman start
  3. laman hosts    (once per new site, asks for admin)
  4. laman trust    (once, so HTTPS has no warnings)
`, cfg.SitesDir, cfg.TLD)
	return nil
}

func cmdInstall(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: laman install php|mariadb|postgres|node|bun|composer|mailpit|redis [version]")
	}
	want := ""
	if len(args) > 1 {
		want = args[1]
	}
	cfg, err := loadOrInit()
	if err != nil {
		return err
	}
	switch args[0] {
	case "php":
		v, err := install.PHP(want)
		if err != nil {
			return err
		}
		if cfg.PHP == "" {
			cfg.PHP = v
			return cfg.Save()
		}
		fmt.Printf("Run `laman use php %s` to switch to it.\n", v)
	case "mariadb", "redis", "postgres":
		return installService(cfg, args[0], want)
	case "postgresql", "pgsql", "pg":
		return installService(cfg, "postgres", want)
	case "composer":
		_, err = install.Composer()
	case "mailpit", "mail":
		if _, err = install.Mailpit(); err == nil {
			fmt.Println("Run `laman restart` to start it.")
			mailHint(cfg)
		}
	case "node", "nodejs":
		v, err := install.Node(want)
		if err != nil {
			return err
		}
		if cfg.Node == "" || install.NodeDir(cfg.Node) == "" {
			return setDefaultNode(cfg, v)
		}
		if v != cfg.Node {
			fmt.Printf("Run `laman use node %s` to make it the default, or put %s in a project's .nvmrc.\n", v, strings.SplitN(v, ".", 2)[0])
		}
	case "bun":
		if _, err = install.Bun(); err == nil {
			fmt.Println("Dev servers of bun projects use it now; `laman bun` runs it here.")
		}
	case "nginx", "caddy":
		fmt.Println("Nothing to install: laman has Caddy built in as its web server.")
	default:
		return fmt.Errorf("unknown component %q (php, mariadb, postgres, node, bun, composer, mailpit, redis)", args[0])
	}
	return err
}

func cmdVersions() {
	cfg, _ := config.Load()
	simple, _ := caddy.Version()
	fmt.Printf("%-8s * %s (built in)\n", "caddy", simple)
	for _, kind := range []string{"php", "node", "mariadb", "postgres", "redis", "composer", "mailpit"} {
		list := config.Installed(kind)
		if len(list) == 0 {
			fmt.Printf("%-8s (none)\n", kind)
			continue
		}
		for i, v := range list {
			mark := " "
			switch kind {
			case "php":
				if v == cfg.DefaultPHP() {
					mark = "*"
				}
			case "node":
				if v == cfg.DefaultNode() {
					mark = "*"
				}
			case "mariadb", "redis", "postgres":
				if v == config.Active(kind, *pinned(cfg, kind)) {
					mark = "*"
				}
			default:
				if i == len(list)-1 {
					mark = "*"
				}
			}
			fmt.Printf("%-8s %s %s\n", kind, mark, v)
		}
	}
}

func cmdUse(args []string) error {
	if len(args) != 2 || (args[0] != "php" && args[0] != "node" && serviceNames[args[0]] == "") {
		return errors.New("usage: laman use php|node|mariadb|postgres|redis <version>")
	}
	if serviceNames[args[0]] != "" {
		return cmdUseService(args[0], args[1])
	}
	if args[0] == "node" {
		v := config.Match("node", strings.TrimPrefix(args[1], "v"))
		if v == "" {
			return fmt.Errorf("Node %s is not installed; run `laman install node %s`", args[1], args[1])
		}
		cfg, err := loadOrInit()
		if err != nil {
			return err
		}
		return setDefaultNode(cfg, v)
	}
	v := config.Match("php", args[1])
	if v == "" {
		return fmt.Errorf("PHP %s is not installed; run `laman install php %s`", args[1], args[1])
	}
	cfg, err := loadOrInit()
	if err != nil {
		return err
	}
	cfg.PHP = v
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("Default PHP is now %s (sites pinned with `laman isolate` keep their version)\n", v)
	return notifyDaemonErr()
}

// siteHere finds the site for the current directory, or the named site.
func siteHere(cfg *config.Config, name string) (sites.Site, error) {
	list := discover(cfg)
	if name != "" {
		slug := sites.Slug(name)
		for _, s := range list {
			if s.Name == slug {
				return s, nil
			}
		}
		return sites.Site{}, fmt.Errorf("no site named %q; see `laman sites`", name)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return sites.Site{}, err
	}
	s, ok := sites.ForDir(list, cwd)
	if !ok {
		return sites.Site{}, errors.New("this folder is not a laman site; cd into one or pass its name")
	}
	return s, nil
}

func cmdIsolate(args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return errors.New("usage: laman isolate <php-version> [site]")
	}
	v := config.Match("php", args[0])
	if v == "" {
		return fmt.Errorf("PHP %s is not installed; run `laman install php %s`", args[0], args[0])
	}
	cfg, err := loadOrInit()
	if err != nil {
		return err
	}
	name := ""
	if len(args) == 2 {
		name = args[1]
	}
	s, err := siteHere(cfg, name)
	if err != nil {
		return err
	}
	cfg.Isolated[s.Name] = v
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("%s now runs PHP %s\n", s.Host, v)
	return notifyDaemonErr()
}

func cmdUnisolate(args []string) error {
	if len(args) > 1 {
		return errors.New("usage: laman unisolate [site]")
	}
	cfg, err := loadOrInit()
	if err != nil {
		return err
	}
	name := ""
	if len(args) == 1 {
		name = args[0]
	}
	s, err := siteHere(cfg, name)
	if err != nil {
		// Allow removing a pin for a site whose folder is gone.
		if _, ok := cfg.Isolated[sites.Slug(name)]; name == "" || !ok {
			return err
		}
		s.Name = sites.Slug(name)
	}
	if _, ok := cfg.Isolated[s.Name]; !ok {
		fmt.Printf("%s is not isolated; it uses the default PHP %s\n", s.Name, cfg.DefaultPHP())
		return nil
	}
	delete(cfg.Isolated, s.Name)
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("%s now uses the default PHP %s\n", s.Name, cfg.DefaultPHP())
	return notifyDaemonErr()
}

// cmdPHP runs the PHP CLI that matches the site in the current directory,
// so `laman php artisan ...` behaves like the site does in the browser.
func cmdPHP(args []string) error {
	php, err := phpHere()
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// Same mail settings as the web workers, so mail() from artisan or a
	// script lands in Mailpit too.
	pre := []string{"-d", "SMTP=127.0.0.1", "-d", fmt.Sprintf("smtp_port=%d", cfg.SMTPPort), "-d", "sendmail_from=laman@localhost"}
	return runPassthrough(php, append(pre, args...)...)
}

// cmdMail opens Mailpit's inbox.
func cmdMail() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if install.MailpitExe() == "" {
		return errors.New("Mailpit is not installed; run `laman install mailpit`")
	}
	if !client.Running() {
		return errors.New("laman is not running; start it with `laman start`")
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/", cfg.MailUIPort)
	fmt.Println("Opening", url)
	return desktop.OpenURL(url)
}

// cmdRedis runs redis-cli against laman's Redis.
func cmdRedis(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	dir := install.RedisDir(config.Active("redis", cfg.Redis))
	if dir == "" {
		return errors.New("Redis is not installed; run `laman install redis`")
	}
	pre := []string{"-h", "127.0.0.1", "-p", fmt.Sprint(cfg.RedisPort)}
	return runPassthrough(filepath.Join(dir, "redis-cli.exe"), append(pre, args...)...)
}

func mailHint(cfg *config.Config) {
	fmt.Printf(`Point your app's mail at Mailpit. For Laravel, in .env:
  MAIL_MAILER=smtp
  MAIL_HOST=127.0.0.1
  MAIL_PORT=%d
PHP's mail() goes there already. See caught mail with `+"`laman mail`"+`.
`, cfg.SMTPPort)
}

// phpHere returns php.exe for the site in the current directory, or the
// default PHP outside a site.
func phpHere() (string, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", err
	}
	v := cfg.DefaultPHP()
	if cwd, err := os.Getwd(); err == nil {
		if s, ok := sites.ForDir(discover(cfg), cwd); ok {
			v, _ = cfg.PHPFor(s.Name)
		}
	}
	if v == "" {
		return "", errors.New("no PHP installed; run `laman install php`")
	}
	return filepath.Join(config.BinDir("php"), v, "php.exe"), nil
}

// cmdComposer runs Composer with the PHP that matches the current site,
// installing Composer the first time it is needed.
func cmdComposer(args []string) error {
	if len(args) > 0 && (args[0] == "self-update" || args[0] == "selfupdate") {
		// composer.phar lives in a versioned folder laman manages.
		_, err := install.Composer()
		return err
	}
	php, err := phpHere()
	if err != nil {
		return err
	}
	phar := install.ComposerPhar()
	if phar == "" {
		if _, err := install.Composer(); err != nil {
			return err
		}
		if phar = install.ComposerPhar(); phar == "" {
			return errors.New("composer install finished but composer.phar is missing")
		}
	}
	return runPassthrough(php, append([]string{phar}, args...)...)
}

// runPassthrough runs a console program in the foreground and exits with its
// exit code. Ctrl+C reaches the child directly (they share the console), so
// laman ignores it and lets the child decide how to stop.
func runPassthrough(exe string, args ...string) error {
	cmd := exec.Command(exe, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	signal.Ignore(os.Interrupt)
	defer signal.Reset(os.Interrupt)
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.ExitCode())
		}
		return err
	}
	return nil
}

// api calls the running daemon's control API.
func api(method, path string) ([]byte, error) { return client.Do(method, path, nil) }

func cmdStart() error {
	if _, err := api("GET", "/status"); err == nil {
		fmt.Println("laman is already running")
		return cmdStatus()
	}
	if _, err := loadOrInit(); err != nil {
		return err
	}
	if err := os.MkdirAll(config.LogDir(), 0o755); err != nil {
		return err
	}
	logPath := filepath.Join(config.LogDir(), "laman.log")
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logf.Close()
	before, _ := logf.Seek(0, io.SeekEnd)

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd, err := spawnDetached(exe, logf, "daemon")
	if err != nil {
		return err
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			return fmt.Errorf("laman failed to start:\n%s", logSince(logPath, before))
		case <-time.After(300 * time.Millisecond):
		}
		if _, err := api("GET", "/status"); err == nil {
			time.Sleep(700 * time.Millisecond) // let services settle before reporting
			return cmdStatus()
		}
	}
	return fmt.Errorf("laman did not answer within 20s; see %s", logPath)
}

func logSince(path string, offset int64) string {
	b, err := os.ReadFile(path)
	if err != nil || int64(len(b)) < offset {
		return ""
	}
	return strings.TrimSpace(string(b[offset:]))
}

func cmdStop() error {
	if _, err := api("POST", "/stop"); err != nil {
		fmt.Println("laman is not running")
		return nil
	}
	for i := 0; i < 120; i++ {
		time.Sleep(250 * time.Millisecond)
		if _, err := api("GET", "/status"); err != nil {
			fmt.Println("laman stopped")
			return nil
		}
	}
	return errors.New("laman did not stop within 30s")
}

func cmdStatus() error {
	body, err := api("GET", "/status")
	if err != nil {
		fmt.Println("laman is not running. Start it with `laman start`.")
		return nil
	}
	var st daemon.Status
	if err := json.Unmarshal(body, &st); err != nil {
		return err
	}
	fmt.Printf("laman running (pid %d)  %s  default PHP %s", st.PID, st.WebServer, st.PHP)
	if st.MariaDB != "" {
		fmt.Printf("  MariaDB %s on 127.0.0.1:%d", st.MariaDB, st.MySQLPort)
	}
	if st.Redis != "" {
		fmt.Printf("  Redis %s on 127.0.0.1:%d", st.Redis, st.RedisPort)
	}
	if st.Postgres != "" {
		fmt.Printf("\nPostgreSQL %s on 127.0.0.1:%d (user postgres, no password)", st.Postgres, st.PostgresPort)
	}
	if st.Mailpit != "" {
		fmt.Printf("\nMail: SMTP 127.0.0.1:%d, inbox %s", st.SMTPPort, st.MailURL)
	}
	fmt.Println()
	fmt.Println()
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SERVICE\tSTATE\tPID\tRESTARTS\tNOTE")
	for _, s := range st.Services {
		note := s.Uptime
		if s.State != "running" && s.LastError != "" {
			note = s.LastError
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%s\n", s.Name, s.State, s.PID, s.Restarts, note)
	}
	for _, kind := range st.Off {
		fmt.Fprintf(tw, "%s\toff\t-\t-\t`laman enable %s` to start it\n", kind, kind)
	}
	tw.Flush()
	fmt.Println()
	https := st.HTTPSPort > 0 && st.HTTPSTrusted
	printSites(st.Sites, siteURL(https, st.HTTPPort, st.HTTPSPort))
	if len(st.Shares) > 0 {
		fmt.Println()
		for _, s := range st.Shares {
			fmt.Printf("Shared: %s at %s\n", s.Host, s.URL)
		}
	}
	if st.HostsStale {
		fmt.Println("\nThe hosts file is missing some sites. Run `laman hosts` so the URLs resolve.")
	}
	for _, n := range st.Notes {
		fmt.Println("\n" + n)
	}
	if st.HTTPSNote != "" {
		fmt.Println("\n" + st.HTTPSNote)
	} else if st.HTTPSPort > 0 && !st.HTTPSTrusted {
		fmt.Println("\nHTTPS is on, but browsers will warn until you run `laman trust` (one Windows prompt).")
	}
	return nil
}

func siteURL(https bool, httpPort, httpsPort int) func(string) string {
	if https {
		return func(h string) string { return caddyconf.URL(h, true, httpsPort) }
	}
	return func(h string) string { return caddyconf.URL(h, false, httpPort) }
}

func printSites(list []sites.Site, url func(host string) string) {
	if len(list) == 0 {
		cfg, _ := config.Load()
		fmt.Printf("No sites. Create a folder in %s, add a project folder with `laman folders add`, or run `laman link` in a project.\n", cfg.SitesDir)
		return
	}
	cfg, _ := config.Load()
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SITE\tURL\tPHP\tDOCUMENT ROOT")
	for _, s := range list {
		name := s.Name
		if s.Linked {
			name += " (linked)"
		}
		php := s.PHP
		if cfg != nil {
			if php == "" {
				php, _ = cfg.PHPFor(s.Name)
			}
			if _, pinned := cfg.Isolated[s.Name]; pinned {
				php += " (isolated)"
			}
		}
		root := s.Root
		switch s.Kind {
		case sites.KindNode:
			php, root = s.Framework, s.Path+"  ("+s.DevCmd+", `laman dev start "+s.Name+"`)"
		case sites.KindProxy:
			php, root = "proxy", "-> "+s.Proxy
		case sites.KindGroup:
			php, root = "-", s.Path+"  (projects inside: `laman folders add` it)"
		case sites.KindNone:
			php, root = "-", s.Path+"  (not a website)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", name, url(s.Host), php, root)
	}
	tw.Flush()
}

func cmdSites() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	https := false
	if cfg.HTTPSPort > 0 {
		if _, err := os.Stat(filepath.Join(config.CADir(), "ca.crt")); err == nil {
			if ca, err := certs.LoadCA(config.CADir()); err == nil {
				https = ca.Trusted()
			}
		}
	}
	printSites(discover(cfg), siteURL(https, cfg.HTTPPort, cfg.HTTPSPort))
	return nil
}

func cmdTrust(args []string) error {
	reset := len(args) > 0 && args[0] == "--reset"
	cfg, err := loadOrInit()
	if err != nil {
		return err
	}
	if reset {
		if ca, err := certs.LoadCA(config.CADir()); err == nil && ca.Trusted() {
			if err := ca.Uninstall(); err != nil {
				return fmt.Errorf("remove old CA: %w", err)
			}
		}
		os.RemoveAll(config.CADir())
		os.RemoveAll(config.CertDir())
		fmt.Println("Old CA and certificates removed")
	}
	ca, err := certs.LoadOrCreateCA(config.CADir(), []string{cfg.TLD, "localhost"})
	if err != nil {
		return err
	}
	if ca.Trusted() {
		fmt.Println("laman's local CA is already trusted")
	} else {
		fmt.Println("Windows will ask you to confirm installing laman's local CA. Click Yes.")
		fmt.Printf("It can only sign certificates for .%s and localhost.\n", cfg.TLD)
		if err := ca.Install(); err != nil {
			return fmt.Errorf("the certificate was not installed (was the prompt declined?): %w", err)
		}
		if !ca.Trusted() {
			return errors.New("the CA was added but Windows still does not trust it")
		}
		fmt.Println("Trusted. Fully quit and reopen your browser (Edge: also close it from the tray if Startup boost is on).")
	}
	if cfg.HTTPSPort == 0 {
		fmt.Println(`HTTPS is disabled; set "httpsPort": 443 in the config and restart laman.`)
	}
	notifyDaemon()
	return nil
}

func cmdUntrust() error {
	if _, err := os.Stat(filepath.Join(config.CADir(), "ca.crt")); err != nil {
		fmt.Println("laman has no CA yet")
		return nil
	}
	ca, err := certs.LoadCA(config.CADir())
	if err != nil {
		return err
	}
	if !ca.Trusted() {
		fmt.Println("laman's CA is not trusted; nothing to remove")
		return nil
	}
	if err := ca.Uninstall(); err != nil {
		return err
	}
	fmt.Println("Removed laman's CA from the Windows certificate store")
	notifyDaemon()
	return nil
}

// notifyDaemon asks a running daemon to pick up config changes.
func notifyDaemon() {
	if _, err := api("POST", "/reload"); err == nil {
		fmt.Println("laman reloaded")
	}
}

// notifyDaemonErr reloads a running daemon and reports a failed reload; it is
// a no-op when laman is not running (the change applies on next start).
func notifyDaemonErr() error {
	if _, err := api("GET", "/status"); err != nil {
		return nil
	}
	if _, err := api("POST", "/reload"); err != nil {
		return fmt.Errorf("laman could not apply the change: %w", err)
	}
	fmt.Println("laman reloaded")
	return nil
}

func cmdLink(args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	path, name := cwd, ""
	if len(args) > 0 {
		name = args[0]
	}
	if len(args) > 1 {
		if path, err = filepath.Abs(args[1]); err != nil {
			return err
		}
	}
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	if name == "" {
		name = filepath.Base(path)
	}
	name = sites.Slug(name)
	if name == "" {
		return errors.New("that name has no usable characters; pass one: laman link <name>")
	}
	cfg, err := loadOrInit()
	if err != nil {
		return err
	}
	cfg.Links[name] = path
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("Linked http://%s.%s -> %s\n", name, cfg.TLD, path)
	notifyDaemon()
	return nil
}

func cmdUnlink(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: laman unlink <name>")
	}
	cfg, err := loadOrInit()
	if err != nil {
		return err
	}
	name := sites.Slug(args[0])
	if _, ok := cfg.Links[name]; !ok {
		return fmt.Errorf("no link named %q", name)
	}
	delete(cfg.Links, name)
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("Unlinked %s\n", name)
	notifyDaemon()
	return nil
}

func cmdHosts(args []string) error {
	// The elevated copy is started with --home because an elevated process
	// does not inherit this shell's LAMAN_HOME.
	elevated := false
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--elevated":
			elevated = true
		case strings.HasPrefix(args[i], "--home="):
			os.Setenv("LAMAN_HOME", strings.TrimPrefix(args[i], "--home="))
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	names := sites.Hosts(discover(cfg))
	changed, err := hosts.Sync(names)
	if err == nil {
		if changed {
			fmt.Printf("Hosts file updated with %d site(s)\n", len(names))
			notifyDaemon()
		} else {
			fmt.Println("Hosts file is already up to date")
		}
		return nil
	}
	if !errors.Is(err, os.ErrPermission) || elevated {
		return err
	}
	fmt.Println("Asking Windows for admin rights to edit the hosts file...")
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	ps := fmt.Sprintf("Start-Process -FilePath '%s' -ArgumentList 'hosts','--elevated','\"--home=%s\"' -Verb RunAs -Wait -WindowStyle Hidden",
		psQuote(exe), psQuote(config.Home()))
	run := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
	if out, err := run.CombinedOutput(); err != nil {
		return fmt.Errorf("elevation was cancelled or failed: %s", strings.TrimSpace(string(out)))
	}
	if !hosts.InSync(names) {
		return errors.New("the hosts file is still out of date (was the admin prompt declined?)")
	}
	fmt.Printf("Hosts file updated with %d site(s)\n", len(names))
	notifyDaemon()
	return nil
}

// psQuote escapes a value for a single-quoted PowerShell string.
func psQuote(s string) string { return strings.ReplaceAll(s, "'", "''") }

func cmdConfig() error {
	cfg, err := loadOrInit()
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	fmt.Printf("%s\n%s\n", config.Path(), b)
	return nil
}

// cmdEditor lists the editors found, or saves the one the dashboard's
// "Open in editor" uses ("auto" = the first one found).
func cmdEditor(args []string) error {
	cfg, err := loadOrInit()
	if err != nil {
		return err
	}
	found := editor.Found()
	if len(args) == 0 {
		cur, ok := editor.Pick(cfg.Editor, found)
		if !ok {
			fmt.Println("No code editor found. laman looks for VS Code, Cursor, Windsurf, Zed, PhpStorm and Sublime Text.")
			return nil
		}
		for _, e := range found {
			mark := "  "
			if e.ID == cur.ID {
				mark = "* "
			}
			fmt.Printf("%s%-9s %-13s %s\n", mark, e.ID, e.Name, e.Path)
		}
		if cfg.Editor == "" {
			fmt.Println("\nAutomatic: the first one found. Pick another with `laman editor <name>`.")
		}
		return nil
	}
	id := strings.ToLower(args[0])
	if id == "auto" {
		id = ""
	} else if !slices.Contains(editor.IDs(), id) {
		return fmt.Errorf("unknown editor %q; choose one of %s, or auto", args[0], strings.Join(editor.IDs(), ", "))
	}
	cfg.Editor = id
	if err := cfg.Save(); err != nil {
		return err
	}
	if e, ok := editor.Pick(id, found); ok {
		if id != "" && e.ID != id {
			fmt.Printf("Saved %s, but it isn't installed; the dashboard uses %s until it is.\n", id, e.Name)
		} else {
			fmt.Printf("The dashboard opens projects in %s.\n", e.Name)
		}
	}
	if client.Running() {
		api("POST", "/reload")
	}
	return nil
}

// cmdTray starts the notification-area icon as lamanw.exe, which has no
// console window, and returns at once.
func cmdTray() error {
	if desktop.TrayRunning() {
		fmt.Println("The laman tray icon is already running.")
		return nil
	}
	exe, err := desktop.EnsureWindowed(desktop.CLIPath())
	if err != nil {
		return fmt.Errorf("prepare %s: %w", desktop.WindowedName, err)
	}
	if err := desktop.Spawn(exe); err != nil {
		return err
	}
	fmt.Println("laman is in your notification area (you may need to expand the ^ arrow).")
	fmt.Println("Tick \"Start with Windows\" in its menu to have it there every time you sign in.")
	return nil
}

func cmdDashboard() error {
	if !client.Running() {
		return errors.New("laman is not running; start it with `laman start` (the dashboard is served by laman itself)")
	}
	url := client.BaseURL() + "/"
	fmt.Println("Opening", url)
	return desktop.OpenURL(url)
}

// discover lists every site laman serves, proxies included.
func discover(cfg *config.Config) []sites.Site {
	list, _ := cfg.Discover()
	return list
}
