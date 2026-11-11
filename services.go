package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"laman/internal/client"
	"laman/internal/config"
	"laman/internal/daemon"
	"laman/internal/install"
)

// MariaDB and Redis run one version at a time, chosen globally. Each line
// ("12.3", "8.10") keeps its own data folder, so switching never lets an
// older server open files a newer one wrote:
//
//   laman use mariadb 11.4      switch; a line without data starts empty
//   laman upgrade mariadb 13.0  copy the data to a newer line and switch
//
// The old line's folder is left untouched, as a way back.

var serviceNames = map[string]string{"mariadb": "MariaDB", "redis": "Redis", "postgres": "PostgreSQL"}

func pinned(cfg *config.Config, kind string) *string {
	switch kind {
	case "mariadb":
		return &cfg.MariaDB
	case "postgres":
		return &cfg.Postgres
	}
	return &cfg.Redis
}

func portOf(cfg *config.Config, kind string) int {
	switch kind {
	case "mariadb":
		return cfg.MySQLPort
	case "postgres":
		return cfg.PostgresPort
	}
	return cfg.RedisPort
}

func dataDir(kind, version string) string {
	switch kind {
	case "mariadb":
		return install.MariaDBDataDir(version)
	case "postgres":
		return install.PostgresDataDir(version)
	}
	return install.RedisDataDir(version)
}

func hasData(kind, version string) bool {
	switch kind {
	case "mariadb":
		return install.HasMariaDBData(version)
	case "postgres":
		return install.HasPostgresData(version)
	}
	_, err := os.Stat(filepath.Join(install.RedisDataDir(version), "dump.rdb"))
	return err == nil
}

func installKind(kind, want string) (string, error) {
	switch kind {
	case "mariadb":
		return install.MariaDB(want)
	case "postgres":
		return install.Postgres(want)
	}
	return install.Redis(want)
}

// initData creates an empty data folder where the server needs one
// (Redis creates its own).
func initData(cfg *config.Config, kind, version string) error {
	switch kind {
	case "mariadb":
		return install.InitMariaDB(version, cfg.MySQLPort)
	case "postgres":
		return install.InitPostgres(version)
	}
	return nil
}

// stopIfRunning stops laman and reports whether it had been running.
func stopIfRunning() (bool, error) {
	if !client.Running() {
		return false, nil
	}
	return true, cmdStop()
}

// cmdUseService switches the global MariaDB or Redis version.
func cmdUseService(kind, want string) error {
	name := serviceNames[kind]
	cfg, err := loadOrInit()
	if err != nil {
		return err
	}
	if err := install.PinVersions(cfg); err != nil {
		return err
	}
	v := config.Match(kind, want)
	if v == "" {
		return fmt.Errorf("%s %s is not installed; run `laman install %s %s`", name, want, kind, want)
	}
	cur := pinned(cfg, kind)
	if config.LineOf(kind, v) == *cur {
		fmt.Printf("Already using %s %s\n", name, *cur)
		return nil
	}
	if client.Running() {
		// The daemon swaps just this server; PHP and the sites keep running.
		if !hasData(kind, v) {
			fmt.Printf("%s %s has no data yet, so it starts empty. Your %s %s data stays in %s.\n",
				name, config.LineOf(kind, v), name, *cur, dataDir(kind, *cur))
		}
		old := *cur
		body, err := client.Do("POST", "/api/versions/use", map[string]string{"kind": kind, "version": v})
		if err != nil {
			return err
		}
		var resp struct{ Version string }
		json.Unmarshal(body, &resp)
		fmt.Printf("Now using %s %s (switch back with `laman use %s %s`)\n", name, resp.Version, kind, old)
		return nil
	}
	was, err := stopIfRunning()
	if err != nil {
		return err
	}
	// Old shared folders must reach their per-line name before the pin moves.
	if err := install.MoveLegacyData(cfg); err != nil {
		return err
	}
	old := *cur
	if !hasData(kind, v) {
		fmt.Printf("%s %s has no data yet, so it starts empty. Your %s %s data stays in %s.\n",
			name, config.LineOf(kind, v), name, old, dataDir(kind, old))
		if config.CompareVersions(config.LineOf(kind, v), old) > 0 {
			fmt.Printf("To bring that data along instead, run `laman use %s %s` and then `laman upgrade %s %s`.\n", kind, old, kind, config.LineOf(kind, v))
		}
	}
	*cur = config.LineOf(kind, v)
	if err := cfg.Save(); err != nil {
		return err
	}
	if err := initData(cfg, kind, v); err != nil {
		return err
	}
	fmt.Printf("Now using %s %s (switch back with `laman use %s %s`)\n", name, v, kind, old)
	if was {
		return cmdStart()
	}
	return nil
}

// cmdUpgrade moves MariaDB or Redis to a newer line, data included.
func cmdUpgrade(args []string) error {
	if len(args) < 1 || len(args) > 2 || serviceNames[args[0]] == "" {
		return errors.New("usage: laman upgrade mariadb|redis|postgres [version]")
	}
	kind, name, want := args[0], serviceNames[args[0]], ""
	if len(args) == 2 {
		want = args[1]
	}
	cfg, err := loadOrInit()
	if err != nil {
		return err
	}
	if err := install.PinVersions(cfg); err != nil {
		return err
	}
	cur := config.Active(kind, *pinned(cfg, kind))
	if cur == "" {
		return fmt.Errorf("%s is not installed; run `laman install %s`", name, kind)
	}
	if !cfg.Enabled(kind) {
		// The upgrade finishes against the running server (mariadb-upgrade,
		// vacuumdb), so it needs the service on.
		return fmt.Errorf("%s is turned off; run `laman enable %s` first", name, kind)
	}

	target, err := installKind(kind, want)
	if err != nil {
		return err
	}
	from, to := config.LineOf(kind, cur), config.LineOf(kind, target)
	switch c := config.CompareVersions(to, from); {
	case c == 0:
		if latest := config.Active(kind, from); latest != cur {
			fmt.Printf("%s %s is the newest %s release. Restarting to use it.\n", name, latest, from)
			return restartIfRunning()
		}
		fmt.Printf("%s %s is already the newest %s release.\n", name, cur, from)
		return nil
	case c < 0:
		if want == "" {
			return fmt.Errorf("you are on %s %s, newer than the default release (%s); give a version, e.g. `laman upgrade %s <version>`", name, from, to, kind)
		}
		return fmt.Errorf("%s %s is older than %s; use `laman use %s %s` to switch down (older servers cannot read newer data)", name, to, from, kind, to)
	}
	if hasData(kind, target) {
		return fmt.Errorf("%s %s already has its own data in %s. Run `laman use %s %s` to switch to it, or delete that folder to upgrade into it again",
			name, to, dataDir(kind, target), kind, to)
	}

	was, err := stopIfRunning()
	if err != nil {
		return err
	}
	// Until the pin moves, a failure leaves the old version in charge: bring
	// laman back up as it was, so a failed upgrade changes nothing.
	unchanged := func(err error) error {
		if was {
			if serr := cmdStart(); serr != nil {
				err = fmt.Errorf("%w; restarting laman also failed: %v", err, serr)
			}
		}
		return err
	}
	if err := install.MoveLegacyData(cfg); err != nil {
		return unchanged(err)
	}
	src, dest := dataDir(kind, cur), dataDir(kind, target)
	os.RemoveAll(dest) // an empty, never-used folder for this line
	if kind == "postgres" {
		// PostgreSQL's data format changes with each major, so files cannot
		// simply be copied: pg_upgrade converts them into a new cluster.
		fmt.Printf("Upgrading PostgreSQL %s data to %s with pg_upgrade\n", from, to)
		if err := install.UpgradePostgres(cur, target); err != nil {
			return unchanged(fmt.Errorf("%w (nothing was switched; PostgreSQL %s is unchanged)", err, from))
		}
	} else if _, err := os.Stat(src); err == nil {
		fmt.Printf("Copying %s %s data to %s\n", name, from, dest)
		if err := install.CopyTree(src, dest); err != nil {
			return unchanged(fmt.Errorf("copy data: %w (nothing was switched)", err))
		}
	}
	*pinned(cfg, kind) = to
	if err := cfg.Save(); err != nil {
		return unchanged(err)
	}
	rollback := fmt.Sprintf("Your %s %s data is unchanged in %s; `laman use %s %s` switches back to it.", name, from, src, kind, from)

	if err := cmdStart(); err != nil {
		return fmt.Errorf("%s %s did not start: %w\n%s", name, to, err, rollback)
	}
	if err := waitService(kind, portOf(cfg, kind), 90*time.Second); err != nil {
		return fmt.Errorf("%w\n%s", err, rollback)
	}
	if kind == "mariadb" {
		// MariaDB's documented upgrade step: update the system tables.
		tool := install.MariaDBBin(install.MariaDBInstallDir(target), "mariadb-upgrade", "mysql_upgrade")
		if tool != "" {
			fmt.Println("Running mariadb-upgrade")
			up := exec.Command(tool, "--silent", "-u", "root", "--protocol=tcp", "-h", "127.0.0.1", "-P", strconv.Itoa(cfg.MySQLPort))
			up.Stdout, up.Stderr = os.Stdout, os.Stderr
			if err := up.Run(); err != nil {
				fmt.Fprintf(os.Stderr, "warning: mariadb-upgrade failed: %v\n%s\n", err, rollback)
			}
		}
	}
	if kind == "postgres" {
		// pg_upgrade does not carry planner statistics over; rebuild them
		// as it recommends, cheapest stages first.
		fmt.Println("Rebuilding planner statistics (vacuumdb --analyze-in-stages)")
		vac := exec.Command(install.PostgresBin(target, "vacuumdb"), "-h", "127.0.0.1", "-p", strconv.Itoa(cfg.PostgresPort),
			"-U", "postgres", "--all", "--analyze-in-stages", "--quiet")
		vac.Stdout, vac.Stderr = os.Stdout, os.Stderr
		if err := vac.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: vacuumdb failed (queries may be slow until you run ANALYZE): %v\n", err)
		}
	}
	fmt.Printf("\nUpgraded %s %s -> %s.\n%s Delete that folder once you are happy.\n", name, from, to, rollback)
	if !was {
		return cmdStop()
	}
	return nil
}

func restartIfRunning() error {
	was, err := stopIfRunning()
	if err != nil || !was {
		return err
	}
	return cmdStart()
}

// waitService waits until a supervised service runs and accepts TCP
// connections on its port (a process can be up before it listens).
func waitService(name string, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	last := "not reported"
	for time.Now().Before(deadline) {
		if body, err := api("GET", "/status"); err == nil {
			var st daemon.Status
			if json.Unmarshal(body, &st) == nil {
				for _, s := range st.Services {
					if s.Name != name {
						continue
					}
					if s.State == "running" {
						c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
						if err == nil {
							c.Close()
							return nil
						}
						last = "running, not accepting connections yet"
						continue
					}
					last = s.State
					if s.LastError != "" {
						last += ": " + s.LastError
					}
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("%s did not come up (%s); see `laman status` and its log", name, last)
}

// installService installs a MariaDB or Redis version alongside the others.
// It only becomes active on a fresh setup; otherwise the user chooses with
// `laman upgrade` (bring the data) or `laman use` (switch).
func installService(cfg *config.Config, kind, want string) error {
	name := serviceNames[kind]
	if err := install.PinVersions(cfg); err != nil {
		return err
	}
	before := map[string]bool{}
	for _, iv := range config.Installed(kind) {
		before[iv] = true
	}
	v, err := installKind(kind, want)
	if err != nil {
		return err
	}
	cur := pinned(cfg, kind)
	switch {
	case *cur == "":
		*cur = config.LineOf(kind, v)
		if err := cfg.Save(); err != nil {
			return err
		}
		if err := initData(cfg, kind, v); err != nil {
			return err
		}
		fmt.Printf("Run `laman restart` to start %s %s.\n", name, v)
		if kind == "redis" {
			fmt.Printf("For Laravel, in .env: REDIS_HOST=127.0.0.1, REDIS_PORT=%d, then CACHE_STORE / QUEUE_CONNECTION / SESSION_DRIVER=redis as you like.\n", cfg.RedisPort)
		}
		if kind == "postgres" {
			fmt.Printf("For Laravel, in .env: DB_CONNECTION=pgsql, DB_HOST=127.0.0.1, DB_PORT=%d, DB_USERNAME=postgres, DB_PASSWORD= (empty).\n"+
				"PHP's pdo_pgsql is enabled. Create a database with `laman psql -c \"CREATE DATABASE myapp\"`.\n", cfg.PostgresPort)
		}
	case config.LineOf(kind, v) == *cur:
		if !before[v] && config.Active(kind, *cur) == v {
			fmt.Printf("Run `laman restart` to use %s %s.\n", name, v)
		}
	default:
		line := config.LineOf(kind, v)
		fmt.Printf("Installed alongside; laman still runs %s %s.\n", name, *cur)
		if config.CompareVersions(line, *cur) > 0 {
			fmt.Printf("  laman upgrade %s %s   move your data to %s and switch\n", kind, line, line)
		}
		fmt.Printf("  laman use %s %s       switch (%s keeps its own data)\n", kind, line, line)
	}
	return nil
}

// cmdPsql runs psql against laman's PostgreSQL as the postgres user.
func cmdPsql(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	v := config.Active("postgres", cfg.Postgres)
	if v == "" {
		return errors.New("PostgreSQL is not installed; run `laman install postgres`")
	}
	pre := []string{"-h", "127.0.0.1", "-p", strconv.Itoa(cfg.PostgresPort), "-U", "postgres"}
	return runPassthrough(install.PostgresBin(v, "psql"), append(pre, args...)...)
}

// cmdEnable turns an add-on server on or off: right away if laman is
// running, and for every later start.
func cmdEnable(args []string, on bool) error {
	verb := map[bool]string{true: "enable", false: "disable"}[on]
	if len(args) != 1 {
		return fmt.Errorf("usage: laman %s mariadb|postgres|redis|mailpit", verb)
	}
	kind := args[0]
	if kind == "postgresql" || kind == "pgsql" || kind == "pg" {
		kind = "postgres"
	}
	if kind == "mail" {
		kind = "mailpit"
	}
	name := serviceNames[kind]
	if kind == "mailpit" {
		name = "Mailpit"
	}
	if name == "" {
		return fmt.Errorf("unknown service %q (mariadb, postgres, redis, mailpit)", args[0])
	}
	if client.Running() {
		if _, err := client.Do("POST", "/api/services/enable", map[string]any{"kind": kind, "enabled": on}); err != nil {
			return err
		}
	} else {
		cfg, err := loadOrInit()
		if err != nil {
			return err
		}
		cfg.SetEnabled(kind, on)
		if err := cfg.Save(); err != nil {
			return err
		}
	}
	state := map[bool]string{true: "on: it starts with laman", false: "off: laman leaves it stopped"}[on]
	fmt.Printf("%s is %s.\n", name, state)
	return nil
}
