package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"laman/internal/client"
	"laman/internal/config"
	"laman/internal/hosts"
	"laman/internal/install"
	"laman/internal/sites"
)

// `laman new` starts a project in a project folder with laman's own PHP,
// Composer and Node, so it runs at <name>.test straight away. Laravel and
// WordPress also get a database on laman's MariaDB or PostgreSQL and their
// settings pointed at laman's servers.

type starter struct {
	name, title string
	pkg         string // Composer package for Laravel starters
	assets      bool   // run npm install + npm run build afterwards
	needsDB     bool
	minPHP      string
}

var starters = []starter{
	{name: "laravel", title: "Laravel", pkg: "laravel/laravel", needsDB: true, minPHP: "8.3"},
	{name: "laravel-react", title: "Laravel with the React starter kit", pkg: "laravel/react-starter-kit", assets: true, needsDB: true, minPHP: "8.3"},
	{name: "laravel-vue", title: "Laravel with the Vue starter kit", pkg: "laravel/vue-starter-kit", assets: true, needsDB: true, minPHP: "8.3"},
	{name: "laravel-livewire", title: "Laravel with the Livewire starter kit", pkg: "laravel/livewire-starter-kit", assets: true, needsDB: true, minPHP: "8.3"},
	{name: "wordpress", title: "WordPress", needsDB: true},
	{name: "php", title: "plain PHP"},
	{name: "vite", title: "a Vite app"},
}

func starterByName(name string) *starter {
	for i := range starters {
		if starters[i].name == name {
			return &starters[i]
		}
	}
	return nil
}

func (s *starter) laravel() bool { return s.pkg != "" }

// newOpts is what `laman new` was asked for.
type newOpts struct {
	name, starter, db, php, in, template string
}

const newUsage = `usage: laman new <name> [starter] [--db mariadb|postgres|sqlite] [--php 8.4] [--in <project folder>] [--template react-ts]
starters: laravel (default), laravel-react, laravel-vue, laravel-livewire, wordpress, php, vite`

func parseNew(args []string) (*newOpts, error) {
	o := &newOpts{}
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		key, val, hasVal := strings.Cut(a, "=")
		switch key {
		case "--db", "--php", "--in", "--template":
			if !hasVal {
				if i+1 >= len(args) {
					return nil, fmt.Errorf("%s needs a value\n%s", key, newUsage)
				}
				i++
				val = args[i]
			}
			switch key {
			case "--db":
				o.db = val
			case "--php":
				o.php = val
			case "--in":
				o.in = val
			case "--template":
				o.template = val
			}
		default:
			if strings.HasPrefix(a, "-") {
				return nil, fmt.Errorf("unknown option %s\n%s", a, newUsage)
			}
			pos = append(pos, a)
		}
	}
	if len(pos) == 0 || len(pos) > 2 {
		return nil, errors.New(newUsage)
	}
	o.name = pos[0]
	o.starter = "laravel"
	if len(pos) == 2 {
		o.starter = strings.ToLower(pos[1])
	}
	if o.starter == "wp" {
		o.starter = "wordpress"
	}
	switch o.db {
	case "postgresql", "pgsql", "pg":
		o.db = "postgres"
	case "mysql":
		o.db = "mariadb"
	}
	return o, nil
}

// checkName accepts a folder name that is also a usable site name.
func checkName(name string) error {
	if name == "" {
		return errors.New("give the project a name, e.g. `laman new shop`")
	}
	if s := sites.Slug(name); s != name {
		if s == "" {
			return fmt.Errorf("%q has no letters or digits to use as a name", name)
		}
		return fmt.Errorf("use lowercase letters, digits and dashes for the name, e.g. %q", s)
	}
	if len(name) > 50 {
		return errors.New("keep the name to 50 characters or fewer")
	}
	return nil
}

// dbName turns a project name into a database name.
func dbName(name string) string { return strings.ReplaceAll(name, "-", "_") }

func cmdNew(args []string) error {
	o, err := parseNew(args)
	if err != nil {
		return err
	}
	st := starterByName(o.starter)
	if st == nil {
		return fmt.Errorf("unknown starter %q\n%s", o.starter, newUsage)
	}
	if err := checkName(o.name); err != nil {
		return err
	}
	if o.template != "" && st.name != "vite" {
		return errors.New("--template only goes with the vite starter")
	}
	cfg, err := loadOrInit()
	if err != nil {
		return err
	}

	// Where it goes.
	parent := cfg.SitesDir
	if o.in != "" {
		abs, err := filepath.Abs(o.in)
		if err != nil {
			return err
		}
		parent = ""
		for _, d := range cfg.SitesDirs() {
			if strings.EqualFold(filepath.Clean(d), filepath.Clean(abs)) {
				parent = d
			}
		}
		if parent == "" {
			return fmt.Errorf("%s is not one of your project folders (see `laman folders`)", abs)
		}
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	dest := filepath.Join(parent, o.name)
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("%s already exists", dest)
	}
	siteName := o.name
	for _, f := range cfg.Folders() {
		if strings.EqualFold(filepath.Clean(f.Dir), filepath.Clean(parent)) && f.Prefix != "" {
			siteName = f.Prefix + "-" + o.name
		}
	}
	for _, s := range discover(cfg) {
		if s.Name == siteName {
			return fmt.Errorf("there is already a site called %s (%s)", s.Host, s.Path)
		}
	}
	for _, a := range install.AppCatalog {
		if a.Name == siteName || siteName == "db" {
			return fmt.Errorf("%s.%s is taken by a laman app; pick another name", siteName, cfg.TLD)
		}
	}

	// Which PHP.
	php := cfg.DefaultPHP()
	if o.php != "" {
		if php = config.Match("php", o.php); php == "" {
			return fmt.Errorf("PHP %s is not installed; run `laman install php %s`", o.php, o.php)
		}
	}
	if st.name != "vite" && php == "" {
		return errors.New("no PHP installed; run `laman install php`")
	}
	if st.minPHP != "" && !install.PHPFits(php, ">="+st.minPHP) {
		return fmt.Errorf("%s needs PHP %s or newer and this would use %s; add --php with a newer installed version, or run `laman install php`", st.title, st.minPHP, php)
	}
	phpExe := filepath.Join(config.BinDir("php"), php, "php.exe")

	// Which Node, for Vite and the starter kits' front-end build.
	nodeDir := install.NodeDir(cfg.DefaultNode())
	if (st.name == "vite" || st.assets) && nodeDir == "" {
		return errors.New("this starter needs Node; run `laman install node`")
	}

	// Which database.
	db := ""
	if st.needsDB {
		if db, err = pickDB(cfg, st, o.db); err != nil {
			return err
		}
		if db != "sqlite" {
			if err := checkDBFree(cfg, db, dbName(o.name)); err != nil {
				return err
			}
		}
	} else if o.db != "" {
		return fmt.Errorf("the %s starter does not use a database; leave out --db", st.name)
	}

	log, err := openNewLog()
	if err != nil {
		return err
	}
	defer log.Close()
	out := io.MultiWriter(os.Stdout, log)
	fmt.Fprintf(out, "Creating %s (%s) in %s\n", o.name, st.title, dest)
	r := &runner{out: out, phpExe: phpExe, node: nodeDir}

	// Make the files. If this fails, nothing of the new folder is kept.
	var made error
	switch {
	case st.laravel():
		args := []string{"create-project", st.pkg, o.name, "--prefer-dist", "--no-interaction"}
		if st.name != "laravel" {
			args = append(args, "--stability=dev") // the starter kits ship from main, as `laravel new` uses them
		}
		made = r.composer(parent, args...)
	case st.name == "wordpress":
		_, made = install.WordPress(dest)
	case st.name == "php":
		made = writePlainPHP(dest, o.name)
	case st.name == "vite":
		tpl := o.template
		if tpl == "" {
			tpl = "react-ts"
		}
		if !safeName(tpl) {
			return fmt.Errorf("unexpected template name %q", tpl)
		}
		if made = r.npm(parent, "create", "vite@latest", o.name, "--", "--template", tpl, "--no-interactive", "--no-immediate"); made == nil {
			if _, err := os.Stat(filepath.Join(dest, "package.json")); err != nil {
				made = errors.New("create-vite finished without making package.json")
			}
		}
	}
	if made != nil {
		if rmErr := os.RemoveAll(dest); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			fmt.Fprintf(out, "Could not remove the half-made %s: %v\n", dest, rmErr)
		}
		return fmt.Errorf("could not create %s: %v (full output in %s)", o.name, firstLine(made), log.Name())
	}

	// From here on the project exists; a failed step is reported, not undone.
	host := siteName + "." + cfg.TLD
	url := strings.TrimSuffix(appURL(cfg, siteName), "/")
	var notes []string
	switch {
	case st.laravel():
		notes = append(notes, setupLaravel(cfg, r, dest, url, db, dbName(o.name))...)
		if st.assets {
			if err := r.npm(dest, "install"); err != nil {
				notes = append(notes, "npm install failed; run `laman npm install` in the project")
			} else if err := r.npm(dest, "run", "build"); err != nil {
				notes = append(notes, "the front-end build failed; run `laman npm run build` (or `laman npm run dev`) in the project")
			}
		}
	case st.name == "wordpress":
		notes = append(notes, setupWordPress(cfg, r.out, dest, dbName(o.name))...)
	case st.name == "vite":
		if err := r.npm(dest, "install"); err != nil {
			notes = append(notes, "npm install failed; run `laman npm install` in the project")
		}
	}

	if o.php != "" && php != cfg.DefaultPHP() {
		cfg.Isolated[siteName] = php
		if err := cfg.Save(); err != nil {
			notes = append(notes, fmt.Sprintf("could not pin %s to PHP %s: %v", host, php, err))
		}
	}
	notifyDaemon()

	fmt.Fprintln(out)
	for _, n := range notes {
		fmt.Fprintln(out, "Note: "+n)
	}
	if !client.Running() {
		fmt.Fprintln(out, "Start laman (`laman start`) to open it.")
	} else if !hosts.InSync(sites.Hosts(discover(cfg))) {
		fmt.Fprintln(out, "If the address does not open yet, run `laman hosts` once (asks for admin).")
	}
	if st.name == "wordpress" {
		fmt.Fprintf(out, "Finish the WordPress setup at %s/wp-admin/install.php\n", url)
	}
	fmt.Fprintf(out, "Created %s at %s\n", o.name, url)
	return nil
}

// pickDB settles the database for a starter: the one asked for, else
// MariaDB, else PostgreSQL, else (Laravel only) SQLite.
func pickDB(cfg *config.Config, st *starter, want string) (string, error) {
	have := func(kind string) bool { return config.Active(kind, *pinned(cfg, kind)) != "" }
	usable := func(kind string) bool { return have(kind) && cfg.Enabled(kind) }
	switch want {
	case "":
		switch {
		case usable("mariadb"):
			return "mariadb", nil
		case usable("postgres") && st.laravel():
			return "postgres", nil
		case st.laravel():
			return "sqlite", nil
		}
		return "", errors.New("WordPress needs MariaDB; run `laman install mariadb`")
	case "sqlite":
		if !st.laravel() {
			return "", errors.New("WordPress needs MariaDB")
		}
		return want, nil
	case "mariadb", "postgres":
		if want == "postgres" && !st.laravel() {
			return "", errors.New("WordPress needs MariaDB")
		}
		if !have(want) {
			return "", fmt.Errorf("%s is not installed; run `laman install %s`", serverTitle(want), want)
		}
		if !cfg.Enabled(want) {
			return "", fmt.Errorf("%s is turned off; run `laman enable %s`", serverTitle(want), want)
		}
		return want, nil
	}
	return "", fmt.Errorf("unknown database %q; use mariadb, postgres or sqlite", want)
}

func serverTitle(kind string) string { return serviceNames[kind] }

func dbPort(cfg *config.Config, kind string) int {
	if kind == "postgres" {
		return cfg.PostgresPort
	}
	return cfg.MySQLPort
}

// checkDBFree makes sure the server is up and the database does not exist
// yet, before any files are made.
func checkDBFree(cfg *config.Config, kind, name string) error {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(dbPort(cfg, kind)), 2*time.Second)
	if err != nil {
		return fmt.Errorf("%s is not running; start laman (`laman start`) first", serverTitle(kind))
	}
	conn.Close()
	exists, err := dbExists(cfg, kind, name)
	if err != nil {
		return fmt.Errorf("could not ask %s about its databases: %w", serverTitle(kind), err)
	}
	if exists {
		return fmt.Errorf("%s already has a database called %s; pick another project name", serverTitle(kind), name)
	}
	return nil
}

// dbSQL runs one statement on laman's MariaDB or PostgreSQL and returns
// its output. name has been checked to be [a-z0-9_] only.
func dbSQL(cfg *config.Config, kind, sql string) (string, error) {
	var cmd *exec.Cmd
	port := strconv.Itoa(dbPort(cfg, kind))
	if kind == "postgres" {
		psql := install.PostgresBin(config.Active("postgres", cfg.Postgres), "psql")
		cmd = exec.Command(psql, "-h", "127.0.0.1", "-p", port, "-U", "postgres", "-d", "postgres", "-tAc", sql)
	} else {
		cli := install.MariaDBBin(install.MariaDBInstallDir(config.Active("mariadb", cfg.MariaDB)), "mariadb", "mysql")
		if cli == "" {
			return "", errors.New("the MariaDB client is missing")
		}
		cmd = exec.Command(cli, "-h", "127.0.0.1", "-P", port, "-u", "root", "-N", "-B", "-e", sql)
	}
	noConsole(cmd)
	// Only stdout is the answer: MariaDB's client warns about TLS on stderr.
	var stderr strings.Builder
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(b)), nil
}

func dbExists(cfg *config.Config, kind, name string) (bool, error) {
	q := "SELECT 1 FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = '" + name + "'"
	if kind == "postgres" {
		q = "SELECT 1 FROM pg_database WHERE datname = '" + name + "'"
	}
	out, err := dbSQL(cfg, kind, q)
	return out != "", err
}

func createDB(cfg *config.Config, kind, name string) error {
	q := "CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"
	if kind == "postgres" {
		q = `CREATE DATABASE "` + name + `"`
	}
	_, err := dbSQL(cfg, kind, q)
	return err
}

// setupLaravel points a fresh Laravel app at laman: its address, mail to
// Mailpit, Redis's port, and the database. Laravel's own create-project
// scripts have already made .env, the app key and an SQLite database.
func setupLaravel(cfg *config.Config, r *runner, dir, url, db, name string) []string {
	var notes []string
	env := filepath.Join(dir, ".env")
	values := map[string]string{
		"APP_URL":     url,
		"MAIL_MAILER": "smtp",
		"MAIL_HOST":   "127.0.0.1",
		"MAIL_PORT":   strconv.Itoa(cfg.SMTPPort),
		"REDIS_HOST":  "127.0.0.1",
		"REDIS_PORT":  strconv.Itoa(cfg.RedisPort),
	}
	if db != "sqlite" {
		if err := createDB(cfg, db, name); err != nil {
			return append(notes, fmt.Sprintf("could not create the %s database %s (%v); the app still uses SQLite", serverTitle(db), name, err))
		}
		fmt.Fprintf(r.out, "Created the %s database %s\n", serverTitle(db), name)
		values["DB_CONNECTION"] = map[string]string{"mariadb": "mariadb", "postgres": "pgsql"}[db]
		values["DB_HOST"] = "127.0.0.1"
		values["DB_PORT"] = strconv.Itoa(dbPort(cfg, db))
		values["DB_DATABASE"] = name
		values["DB_USERNAME"] = map[string]string{"mariadb": "root", "postgres": "postgres"}[db]
		values["DB_PASSWORD"] = ""
	}
	if err := install.SetEnv(env, values); err != nil {
		return append(notes, fmt.Sprintf("could not update .env: %v", err))
	}
	if db != "sqlite" {
		if err := r.php(dir, "artisan", "migrate", "--force", "--no-interaction"); err != nil {
			notes = append(notes, "the migrations failed; run `laman php artisan migrate` in the project")
		} else {
			os.Remove(filepath.Join(dir, "database", "database.sqlite"))
		}
	}
	return notes
}

// setupWordPress makes the database and wp-config.php. The rest is
// WordPress's own five-minute install in the browser.
func setupWordPress(cfg *config.Config, out io.Writer, dir, name string) []string {
	if err := createDB(cfg, "mariadb", name); err != nil {
		return []string{fmt.Sprintf("could not create the database %s (%v); create it, then run the WordPress setup", name, err)}
	}
	fmt.Fprintf(out, "Created the MariaDB database %s\n", name)
	sample, err := os.ReadFile(filepath.Join(dir, "wp-config-sample.php"))
	if err != nil {
		return []string{"wp-config-sample.php is missing; the WordPress setup will ask for the database details (user root, no password, host 127.0.0.1:" + strconv.Itoa(cfg.MySQLPort) + ")"}
	}
	conf := install.WPConfig(string(sample), name, "root", "", "127.0.0.1:"+strconv.Itoa(cfg.MySQLPort), install.Salt)
	if err := os.WriteFile(filepath.Join(dir, "wp-config.php"), []byte(conf), 0o644); err != nil {
		return []string{fmt.Sprintf("could not write wp-config.php: %v", err)}
	}
	return nil
}

func writePlainPHP(dir, name string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	page := `<!doctype html>
<html lang="en">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>` + name + `</title>
<body style="font: 16px/1.5 system-ui, sans-serif; max-width: 40rem; margin: 4rem auto; padding: 0 1rem">
<h1>` + name + `</h1>
<p>Running on PHP <?= PHP_VERSION ?>. Edit <code>index.php</code> to get started.</p>
</body>
</html>
`
	return os.WriteFile(filepath.Join(dir, "index.php"), []byte(page), 0o644)
}

// runner runs the tools a starter needs with laman's PHP, Node and npm
// first on PATH, sending their output to out.
type runner struct {
	out    io.Writer
	phpExe string // php.exe
	node   string // Node folder, or ""
}

func (r *runner) run(dir, exe string, args ...string) error {
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	path := filepath.Dir(r.phpExe)
	if r.node != "" {
		path += ";" + r.node + ";" + install.NpmPrefix()
	}
	cmd.Env = append(os.Environ(), "PATH="+path+";"+os.Getenv("PATH"),
		"COMPOSER_NO_INTERACTION=1", "CI=1", "NO_COLOR=1", "npm_config_yes=true", "npm_config_fund=false", "npm_config_update_notifier=false")
	cmd.Stdout, cmd.Stderr = r.out, r.out
	noConsole(cmd)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", strings.TrimSuffix(filepath.Base(exe), ".exe"), strings.Join(args, " "), err)
	}
	return nil
}

func (r *runner) php(dir string, args ...string) error { return r.run(dir, r.phpExe, args...) }

func (r *runner) composer(dir string, args ...string) error {
	phar := install.ComposerPhar()
	if phar == "" {
		if _, err := install.Composer(); err != nil {
			return err
		}
		if phar = install.ComposerPhar(); phar == "" {
			return errors.New("composer.phar is missing after installing Composer")
		}
	}
	return r.run(dir, r.phpExe, append([]string{phar}, args...)...)
}

func (r *runner) npm(dir string, args ...string) error {
	cli := filepath.Join(r.node, "node_modules", "npm", "bin", "npm-cli.js")
	return r.run(dir, filepath.Join(r.node, "node.exe"), append([]string{cli}, args...)...)
}

func openNewLog() (*os.File, error) {
	if err := os.MkdirAll(config.LogDir(), 0o755); err != nil {
		return nil, err
	}
	return os.Create(filepath.Join(config.LogDir(), "new.log"))
}

func firstLine(err error) string {
	s, _, _ := strings.Cut(err.Error(), "\n")
	return s
}

// safeName is true for a template or package name with nothing a shell
// or npm could read as an option or a path.
func safeName(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}
