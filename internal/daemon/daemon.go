// Package daemon runs laman's background process: it serves sites with an
// embedded Caddy, supervises php-cgi workers and MariaDB, watches the sites
// directory and exposes a small control API on 127.0.0.1.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	_ "github.com/caddyserver/caddy/v2/modules/standard"

	"laman/internal/caddyconf"
	"laman/internal/certs"
	"laman/internal/client"
	"laman/internal/config"
	"laman/internal/hosts"
	"laman/internal/install"
	"laman/internal/sites"
)

type Status struct {
	PID          int             `json:"pid"`
	Home         string          `json:"home"`
	HTTPPort     int             `json:"httpPort"`
	MySQLPort    int             `json:"mysqlPort,omitempty"`
	PHP          string          `json:"php"`
	WebServer    string          `json:"webServer"`
	MariaDB      string          `json:"mariadb,omitempty"`
	Node         string          `json:"node,omitempty"`     // default version
	Postgres     string          `json:"postgres,omitempty"` // version
	PostgresPort int             `json:"postgresPort,omitempty"`
	Redis        string          `json:"redis,omitempty"` // version
	RedisPort    int             `json:"redisPort,omitempty"`
	Mailpit      string          `json:"mailpit,omitempty"` // version
	SMTPPort     int             `json:"smtpPort,omitempty"`
	MailURL      string          `json:"mailUrl,omitempty"`
	Sites        []sites.Site    `json:"sites"`
	Services     []ServiceStatus `json:"services"`
	HostsStale   bool            `json:"hostsStale"`
	Notes        []string        `json:"notes,omitempty"`
	UserHome     string          `json:"userHome"`
	SitesDir     string          `json:"sitesDir"`
	SitesDirs    []string        `json:"sitesDirs"`        // every project folder, the main one first
	Theme        string          `json:"theme"`            // "light", "dark" or "" (follow the system)
	Editor       string          `json:"editor,omitempty"` // the editor "Open in editor" uses, by name ("" = none installed)
	Folders      []sites.Folder  `json:"folders"`
	Dev          []DevStatus     `json:"dev"`               // dev servers started since laman started
	DevAuto      map[string]bool `json:"devAuto,omitempty"` // site names whose dev server starts with laman   // the same, with name prefixes

	// Off lists installed add-on servers that are turned off.
	Off []string `json:"off,omitempty"`

	// Shares: sites reachable from the internet through `laman share`.
	Shares []ShareInfo `json:"shares,omitempty"`
	// ShareDomain: shares go to <site>.<ShareDomain> (set up), else random links.
	ShareDomain string `json:"shareDomain,omitempty"`

	HTTPSPort    int    `json:"httpsPort,omitempty"` // 0 when HTTPS is off
	HTTPSTrusted bool   `json:"httpsTrusted"`
	HTTPSNote    string `json:"httpsNote,omitempty"`

	Update UpdateInfo `json:"update"` // laman's own version and any newer release
}

type daemon struct {
	cfg     *config.Config
	mariadb string // install dir
	mailpit string // mailpit.exe
	redis   string // install dir
	// Data folders of the running MariaDB and Redis lines.
	mariadbData, redisData string
	postgres               string   // running PostgreSQL version, or ""
	skipped                []string // add-on servers left off at start, and why
	// Add-on servers by kind ("mariadb", "postgres", "redis", "mailpit"),
	// each with its own context so one can be swapped while the rest run.
	// PHP workers live in pools. Guarded by mu.
	handles  map[string]*handle
	svcWG    sync.WaitGroup
	switchMu sync.Mutex // one version switch at a time
	ctx      context.Context

	refreshMu sync.Mutex    // serialises refresh (watcher vs. /reload)
	dbCounts  dbCountsCache // last read of the database queue tables
	applied   string        // Caddyfile currently loaded into Caddy
	raw       []sites.Site

	mu         sync.Mutex
	pools      map[string]*pool // by PHP version
	sites      []sites.Site     // with PHP versions filled in
	defaultPHP string
	notes      []string // problems worth showing in `laman status`
	hostsStale bool
	tls        tlsState
	webErr     string

	quit       chan struct{}
	installJob job
	phpLines   lines
	available  availableLines
	appLatest  appLatest
	shares     map[string]*share     // by site host; guarded by mu
	domain     *namedTunnel          // the own-domain tunnel while it runs; guarded by mu
	setup      shareSetup            // the dashboard's `laman share setup` run
	devs       map[string]*devServer // Node dev servers by site host; guarded by mu
	devRefresh bool                  // a refresh for a dev server change is queued; guarded by mu
	quitOnce   sync.Once
	updates    updater // laman's own newer release, if any
}

func Run() error {
	log.SetFlags(log.LstdFlags)
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	for _, dir := range []string{config.LogDir(), config.CaddyDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	d := &daemon{cfg: cfg, quit: make(chan struct{}), pools: map[string]*pool{}, shares: map[string]*share{}}
	if cfg.DefaultPHP() == "" {
		return errors.New("no PHP installed; run `laman install php`")
	}

	// Claim the control port first: it doubles as the single-instance lock,
	// so nothing below can touch data a running instance is using.
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.ControlPort))
	if err != nil {
		return fmt.Errorf("control port %d is busy (is laman already running?): %w", cfg.ControlPort, err)
	}
	if err := d.resolveInstalls(); err != nil {
		ln.Close()
		return err
	}
	if err := d.checkPorts(); err != nil {
		ln.Close()
		return err
	}
	if err := killChildrenOnExit(); err != nil {
		logf("warning: could not create job object: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	d.ctx = ctx
	d.handles = map[string]*handle{}
	for _, kind := range serviceOrder {
		if !cfg.Enabled(kind) {
			continue // turned off: `laman enable` or the dashboard starts it
		}
		if s := d.buildService(kind); s != nil {
			d.startService(kind, s)
		}
	}
	stopAll := func() {
		cancel() // every service context derives from ctx
		d.stopAllDev()
		for _, p := range d.pools {
			p.stop()
		}
		d.svcWG.Wait()
	}
	if err := d.refresh(); err != nil {
		stopAll()
		ln.Close()
		return err
	}
	srv := &http.Server{Handler: d.routes()}
	go srv.Serve(ln)
	go d.watch(ctx)
	go d.startAutoDev()
	go d.watchUpdates(ctx)
	logf("laman started (pid %d): Caddy %s, mariadb %q", os.Getpid(), caddyVersion(), d.mariadb)

	<-d.quit
	logf("stopping")
	if err := caddy.Stop(); err != nil {
		logf("stopping caddy: %v", err)
	}
	d.refreshMu.Lock() // no refresh may start or stop pools from here on
	stopAll()
	shutdownCtx, c2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer c2()
	srv.Shutdown(shutdownCtx)
	logf("stopped")
	return nil
}

func caddyVersion() string {
	simple, _ := caddy.Version()
	return simple
}

// resolveInstalls picks the MariaDB and Redis versions to run. It runs
// before any server starts, so it is where old shared data folders are
// moved to their per-line names and a new line's data is initialised.
func (d *daemon) resolveInstalls() error {
	if err := install.PinVersions(d.cfg); err != nil {
		logf("warning: could not pin MariaDB/Redis versions: %v", err)
	}
	if err := install.MoveLegacyData(d.cfg); err != nil {
		return err
	}
	if v := config.Active("mariadb", d.cfg.MariaDB); v != "" {
		if err := install.InitMariaDB(v, d.cfg.MySQLPort); err != nil {
			return err
		}
		d.mariadb, d.mariadbData = install.MariaDBInstallDir(v), install.MariaDBDataDir(v)
	}
	if v := config.Active("postgres", d.cfg.Postgres); v != "" {
		if err := install.InitPostgres(v); err != nil {
			return err
		}
		d.postgres = v
	}
	if v := config.Active("redis", d.cfg.Redis); v != "" {
		d.redis, d.redisData = install.RedisDir(v), install.RedisDataDir(v)
	}
	d.mailpit = install.MailpitExe()
	return nil
}

// defaultPoolPorts is slot 0, where the first pool starts; checked up front
// so a clash with another program fails `laman start` clearly.
func (d *daemon) defaultPoolPorts() []int {
	out := make([]int, d.cfg.PHPWorkers)
	for i := range out {
		out[i] = d.cfg.PHPBasePort + i
	}
	return out
}

func (d *daemon) checkPorts() error {
	type want struct {
		port int
		what string
	}
	wants := []want{{d.cfg.HTTPPort, "HTTP (httpPort)"}}
	if d.cfg.HTTPSPort > 0 {
		wants = append(wants, want{d.cfg.HTTPSPort, "HTTPS (httpsPort, 0 disables)"})
	}
	for _, p := range d.defaultPoolPorts() {
		wants = append(wants, want{p, "php-cgi (phpBasePort)"})
	}
	// The web server and PHP are laman; a clash there stops it. A clash on
	// an add-on server (often another copy running in Laragon, WSL or
	// Docker) only skips that server, with a note saying why.
	busy := func(port int) bool {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return true
		}
		ln.Close()
		return false
	}
	optional := []struct {
		kind  string
		on    *string
		ports []int
		what  string
	}{
		{"mariadb", &d.mariadb, []int{d.cfg.MySQLPort}, "MariaDB (mysqlPort)"},
		{"postgres", &d.postgres, []int{d.cfg.PostgresPort}, "PostgreSQL (postgresPort)"},
		{"redis", &d.redis, []int{d.cfg.RedisPort}, "Redis (redisPort)"},
		{"mailpit", &d.mailpit, []int{d.cfg.SMTPPort, d.cfg.MailUIPort}, "Mailpit (smtpPort, mailUiPort)"},
	}
	for _, o := range optional {
		if *o.on == "" || !d.cfg.Enabled(o.kind) {
			continue // not installed, or turned off: its port is not ours to need
		}
		for _, p := range o.ports {
			if busy(p) {
				note := fmt.Sprintf("%s is not running: port %d is already in use by another program (WSL, Docker or Laragon?). Stop that one and run `laman restart`, or change the port in config.json.", strings.SplitN(o.what, " (", 2)[0], p)
				logf("%s", note)
				d.skipped = append(d.skipped, note)
				*o.on = ""
				break
			}
		}
	}
	for _, w := range wants {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", w.port))
		if err != nil {
			return fmt.Errorf("port %d for %s is already in use. Stop whatever holds it (Laragon?) or change it in %s", w.port, w.what, config.Path())
		}
		ln.Close()
	}
	return nil
}

// serviceOrder is the order add-on servers start and are listed in.
var serviceOrder = []string{"mariadb", "postgres", "redis", "mailpit"}

type handle struct {
	svc    *Service
	cancel context.CancelFunc
	done   chan struct{}
}

func (d *daemon) startService(kind string, s *Service) {
	ctx, cancel := context.WithCancel(d.ctx)
	h := &handle{svc: s, cancel: cancel, done: make(chan struct{})}
	d.mu.Lock()
	d.handles[kind] = h
	d.mu.Unlock()
	d.svcWG.Add(1)
	go func() {
		defer d.svcWG.Done()
		defer close(h.done)
		s.Run(ctx)
	}()
}

// stopService stops one add-on server and waits for it to exit.
func (d *daemon) stopService(kind string) {
	d.mu.Lock()
	h := d.handles[kind]
	delete(d.handles, kind)
	d.mu.Unlock()
	if h != nil {
		h.cancel()
		<-h.done
	}
}

// buildService describes the add-on server of a kind as currently resolved
// (d.mariadb and friends), or returns nil when it is not installed.
func (d *daemon) buildService(kind string) *Service {
	switch kind {
	case "mariadb":
		if d.mariadb == "" {
			return nil
		}
		server := install.MariaDBBin(d.mariadb, "mariadbd", "mysqld")
		admin := install.MariaDBBin(d.mariadb, "mariadb-admin", "mysqladmin")
		port := strconv.Itoa(d.cfg.MySQLPort)
		data := d.mariadbData
		return &Service{
			Name: "mariadb",
			Build: func() *exec.Cmd {
				return exec.Command(server, "--defaults-file="+filepath.Join(data, "my.ini"),
					"--datadir="+data, "--port="+port, "--bind-address=127.0.0.1", "--console")
			},
			Stop: func(cmd *exec.Cmd, done <-chan struct{}) {
				if admin != "" {
					a := exec.Command(admin, "-u", "root", "--protocol=tcp", "-h", "127.0.0.1", "-P", port, "shutdown")
					noWindow(a)
					a.Run()
				}
				killAfter(cmd, done, 20*time.Second)
			},
		}
	case "postgres":
		if d.postgres == "" {
			return nil
		}
		server := install.PostgresBin(d.postgres, "postgres")
		pgctl := install.PostgresBin(d.postgres, "pg_ctl")
		data := install.PostgresDataDir(d.postgres)
		port := strconv.Itoa(d.cfg.PostgresPort)
		return &Service{
			Name: "postgres",
			Build: func() *exec.Cmd {
				return exec.Command(server, "-D", data, "-p", port,
					"-c", "listen_addresses=127.0.0.1", "-c", "logging_collector=off")
			},
			Stop: func(cmd *exec.Cmd, done <-chan struct{}) {
				// Fast shutdown: roll back open transactions, checkpoint, exit.
				a := exec.Command(pgctl, "stop", "-D", data, "-m", "fast", "-w", "-t", "20")
				noWindow(a)
				a.Run()
				killAfter(cmd, done, 25*time.Second)
			},
		}
	case "redis":
		if d.redis == "" {
			return nil
		}
		dir := d.redis
		server := filepath.Join(dir, "redis-server.exe")
		cli := filepath.Join(dir, "redis-cli.exe")
		data := d.redisData
		os.MkdirAll(data, 0o755)
		port := strconv.Itoa(d.cfg.RedisPort)
		return &Service{
			Name: "redis",
			Build: func() *exec.Cmd {
				// No config file: everything on the command line. The MSYS2
				// build reads Windows paths written with forward slashes.
				cmd := exec.Command(server, "--port", port, "--bind", "127.0.0.1",
					"--dir", filepath.ToSlash(data), "--dbfilename", "dump.rdb",
					"--save", "3600 1 300 100 60 10000", "--appendonly", "no",
					"--protected-mode", "yes", "--daemonize", "no",
					// The MSYS2 build buffers stdout when it is not a console,
					// so its own messages would never reach redis.log.
					"--logfile", filepath.ToSlash(filepath.Join(config.LogDir(), "redis-server.log")))
				cmd.Dir = dir
				return cmd
			},
			Stop: func(cmd *exec.Cmd, done <-chan struct{}) {
				// SHUTDOWN saves the dataset to dump.rdb before exiting.
				a := exec.Command(cli, "-h", "127.0.0.1", "-p", port, "shutdown")
				noWindow(a)
				a.Run()
				killAfter(cmd, done, 10*time.Second)
			},
		}
	case "mailpit":
		if d.mailpit == "" {
			return nil
		}
		exe := d.mailpit
		db := filepath.Join(config.DataDir(), "mailpit", "mailpit.db")
		os.MkdirAll(filepath.Dir(db), 0o755)
		smtp := fmt.Sprintf("127.0.0.1:%d", d.cfg.SMTPPort)
		ui := fmt.Sprintf("127.0.0.1:%d", d.cfg.MailUIPort)
		return &Service{
			Name: "mailpit",
			Build: func() *exec.Cmd {
				// Accept any SMTP login so apps configured with a username
				// and password (as for a real mail provider) still deliver.
				return exec.Command(exe, "--smtp", smtp, "--listen", ui, "--database", db,
					"--smtp-auth-accept-any", "--smtp-auth-allow-insecure", "--disable-version-check")
			},
			// SQLite in WAL mode survives a hard stop, and Windows has no
			// gentle signal to send a console program in another group.
			Stop: killNow,
		}
	}
	return nil
}

// refresh rediscovers sites, issues certificates, rewrites the Caddyfile and
// landing page, loads the config into Caddy when it changed, and tries to
// sync the hosts file. caddy.Load is synchronous and graceful: when it
// returns, the new config is serving.
func (d *daemon) refresh() error {
	d.refreshMu.Lock()
	defer d.refreshMu.Unlock()
	if fresh, err := config.Load(); err == nil {
		// Only fields that take effect without a restart.
		d.cfg.SitesDir, d.cfg.MoreSitesDirs, d.cfg.FolderPrefix = fresh.SitesDir, fresh.MoreSitesDirs, fresh.FolderPrefix
		d.cfg.TLD, d.cfg.Links = fresh.TLD, fresh.Links
		d.cfg.PHP, d.cfg.Isolated = fresh.PHP, fresh.Isolated
		d.cfg.Apps, d.cfg.DBClient = fresh.Apps, fresh.DBClient
		d.cfg.Theme, d.cfg.Proxies, d.cfg.DevAuto = fresh.Theme, fresh.Proxies, fresh.DevAuto
		d.cfg.Editor = fresh.Editor
	}
	raw, clashes := d.cfg.Discover()

	// Work out which PHP version each site needs and make sure a pool runs
	// for every one of them. The default pool always runs so new folders
	// work immediately.
	defaultPHP := d.cfg.DefaultPHP()
	if defaultPHP == "" {
		return errors.New("no PHP installed; run `laman install php`")
	}
	need := map[string]bool{defaultPHP: true}
	var notes []string
	for _, c := range clashes {
		notes = append(notes, fmt.Sprintf("Two project folders hold %q: %s is served as %s.%s, %s is not. Rename one, or serve it under another name with `laman link <name>` in that folder.",
			filepath.Base(c.Skipped), c.Served, c.Name, d.cfg.TLD, c.Skipped))
	}
	list := make([]sites.Site, len(raw))
	for i, s := range raw {
		// db.<tld> is the default database app: same PHP as that app.
		s.App = d.cfg.AppFor(s.Name)
		key := s.Name
		if s.App != "" {
			key = s.App
		}
		v, note := d.cfg.PHPFor(key)
		if note != "" {
			notes = append(notes, note)
		}
		s.PHP, need[v] = v, true
		_, s.Isolated = d.cfg.Isolated[key]
		list[i] = s
	}
	stalePools, err := d.reconcilePools(need)
	if err != nil {
		return err
	}
	// Stop retired pools only after Caddy stops routing to them.
	defer func() {
		for _, p := range stalePools {
			p.stop()
		}
	}()

	htmlDir := filepath.Join(config.CaddyDir(), "html")
	if err := os.MkdirAll(htmlDir, 0o755); err != nil {
		return err
	}
	in := caddyconf.Input{
		HTMLDir:    htmlDir,
		LogFile:    filepath.Join(config.LogDir(), "caddy.log"),
		StorageDir: filepath.Join(config.DataDir(), "caddy"),
		HTTPPort:   d.cfg.HTTPPort,
		PHPPorts:   d.pools[defaultPHP].ports,
		SitePorts:  map[string][]int{},
		Sites:      list,
		SiteEnv:    map[string]map[string]string{},
		Shares:     d.sharePorts(),
		DomainPort: d.domainPort(),
	}
	for _, s := range list {
		in.SitePorts[s.Host] = d.pools[s.PHP].ports
		if s.App != "" {
			if env := install.ServerEnv(s.App); len(env) > 0 {
				in.SiteEnv[s.Host] = env
			}
		}
	}
	tlsState, certsChanged := d.issueCerts(&in, list)

	https, port := tlsState.trusted, d.cfg.HTTPPort
	if https {
		port = d.cfg.HTTPSPort
	}
	dash := fmt.Sprintf("http://127.0.0.1:%d/", d.cfg.ControlPort)
	if err := os.WriteFile(filepath.Join(htmlDir, "index.html"), []byte(caddyconf.Landing(caddyconf.LandingInput{
		Sites: list, Folders: d.cfg.Folders(), HTTPS: https, Port: port,
		Dashboard: dash, Theme: d.cfg.Theme,
	})), 0o644); err != nil {
		return err
	}
	// Pages for sites with nothing to serve right now: a stopped dev
	// server, a proxy whose port is quiet, a group of projects, or a folder
	// that is not a website.
	noticeDir := filepath.Join(htmlDir, "notice")
	if err := os.MkdirAll(noticeDir, 0o755); err != nil {
		return err
	}
	for _, s := range list {
		if s.Kind == sites.KindPHP || s.Kind == sites.KindStatic || s.Kind == "" {
			continue
		}
		n := caddyconf.NoticeInput{Site: s, Dashboard: dash, Theme: d.cfg.Theme}
		switch s.Kind {
		case sites.KindNode:
			dev := d.devState(s.Host)
			n.State, n.Port, n.Output = dev.State, dev.Port, dev.Error
		case sites.KindGroup:
			n.Sub = sites.SubProjects(s.Path)
		}
		if err := os.WriteFile(filepath.Join(noticeDir, s.Name+".html"), []byte(caddyconf.Notice(n)), 0o644); err != nil {
			return err
		}
	}
	in.DevPorts = d.devPorts()

	conf := caddyconf.Render(in)
	if conf != d.applied || certsChanged {
		if err := os.WriteFile(filepath.Join(config.CaddyDir(), "Caddyfile"), []byte(conf), 0o600); err != nil { // may hold app keys
			return err
		}
		js, err := caddyconf.Adapt(conf)
		if err == nil {
			// Force a reload when only certificate files changed on disk.
			err = caddy.Load(js, certsChanged)
		}
		if err != nil {
			d.mu.Lock()
			d.webErr = err.Error()
			d.mu.Unlock()
			return fmt.Errorf("caddy: %w", err)
		}
		d.applied = conf
		logf("caddy loaded config with %d site(s)", len(list))
	}

	stale := false
	names := sites.Hosts(list)
	if !hosts.InSync(names) {
		if _, err := hosts.Sync(names); err != nil {
			stale = true
			if !errors.Is(err, os.ErrPermission) {
				logf("hosts sync: %v", err)
			}
		}
	}

	d.raw = raw
	d.mu.Lock()
	d.sites, d.hostsStale, d.tls, d.webErr = list, stale, tlsState, ""
	d.defaultPHP, d.notes = defaultPHP, notes
	d.mu.Unlock()
	return nil
}

type tlsState struct {
	enabled bool
	trusted bool
	note    string // a problem worth showing in `laman status`
}

// issueCerts makes sure every site (and the default server) has a current
// certificate from the local CA and fills the TLS fields of in. Failures
// degrade to HTTP-only rather than stopping the daemon.
func (d *daemon) issueCerts(in *caddyconf.Input, list []sites.Site) (tlsState, bool) {
	if d.cfg.HTTPSPort <= 0 {
		return tlsState{}, false
	}
	ca, err := certs.LoadOrCreateCA(config.CADir(), []string{d.cfg.TLD, "localhost"})
	if err != nil {
		logf("HTTPS disabled: %v", err)
		return tlsState{note: "HTTPS is off: " + err.Error()}, false
	}
	st := tlsState{enabled: true, trusted: ca.Trusted()}
	dir := config.CertDir()
	crt, key, changed, err := ca.Leaf(dir, "localhost", []net.IP{net.IPv4(127, 0, 0, 1).To4(), net.IPv6loopback})
	if err != nil {
		logf("HTTPS disabled: %v", err)
		return tlsState{note: "HTTPS is off: " + err.Error()}, false
	}
	in.HTTPSPort, in.DefaultCert, in.Certs = d.cfg.HTTPSPort, caddyconf.Cert{Crt: crt, Key: key}, map[string]caddyconf.Cert{}
	for _, s := range list {
		if !ca.Permits(s.Host) {
			st.note = fmt.Sprintf("The local CA does not cover .%s (the TLD changed). Run `laman trust --reset`.", d.cfg.TLD)
			continue
		}
		crt, key, ch, err := ca.Leaf(dir, s.Host, nil)
		if err != nil {
			logf("certificate for %s: %v", s.Host, err)
			continue
		}
		changed = changed || ch
		in.Certs[s.Host] = caddyconf.Cert{Crt: crt, Key: key}
	}
	return st, changed
}

// watch picks up new, renamed and removed project folders, and does a full
// refresh every few minutes so certificate renewals and a newly trusted CA
// are noticed while the daemon keeps running.
func (d *daemon) watch(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	lastFull := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		d.refreshMu.Lock()
		fresh, _ := d.cfg.Discover()
		same := sites.Equal(fresh, d.raw)
		d.refreshMu.Unlock()
		if !same || time.Since(lastFull) > 5*time.Minute {
			lastFull = time.Now()
			if err := d.refresh(); err != nil {
				logf("refresh: %v", err)
			}
		}
	}
}

func (d *daemon) status() Status {
	d.mu.Lock()
	defer d.mu.Unlock()
	st := Status{
		PID: os.Getpid(), Home: config.Home(), HTTPPort: d.cfg.HTTPPort,
		PHP: d.defaultPHP, WebServer: "Caddy " + caddyVersion(), UserHome: userHome(), SitesDir: d.cfg.SitesDir, SitesDirs: d.cfg.SitesDirs(), Folders: d.cfg.Folders(), Theme: d.cfg.Theme, Editor: d.editorName(),
		Dev: []DevStatus{}, DevAuto: d.cfg.DevAuto,
		Sites: d.sites, HostsStale: d.hostsStale, Notes: append(append([]string{}, d.skipped...), d.notes...),
		HTTPSTrusted: d.tls.trusted, HTTPSNote: d.tls.note, Shares: d.shareInfo(),
		Update: d.updates.get(),
	}
	if cfg, err := config.Load(); err == nil && cfg.ShareTunnel != "" {
		st.ShareDomain = cfg.ShareDomain
	}
	for _, s := range d.devs { // s.status takes only the server's own lock
		st.Dev = append(st.Dev, s.status())
	}
	if d.tls.enabled {
		st.HTTPSPort = d.cfg.HTTPSPort
	}
	if d.mariadb != "" && d.cfg.Enabled("mariadb") {
		st.MariaDB, st.MySQLPort = filepath.Base(d.mariadb), d.cfg.MySQLPort
	}
	if d.postgres != "" && d.cfg.Enabled("postgres") {
		st.Postgres, st.PostgresPort = d.postgres, d.cfg.PostgresPort
	}
	if d.redis != "" && d.cfg.Enabled("redis") {
		st.Redis, st.RedisPort = filepath.Base(d.redis), d.cfg.RedisPort
	}
	// Node is not a service; read the default fresh so `laman use node`
	// shows up without a restart.
	if cfg, err := config.Load(); err == nil {
		st.Node = cfg.DefaultNode()
	}
	if d.mailpit != "" && d.cfg.Enabled("mailpit") {
		st.Mailpit, st.SMTPPort = filepath.Base(filepath.Dir(d.mailpit)), d.cfg.SMTPPort
		st.MailURL = fmt.Sprintf("http://127.0.0.1:%d/", d.cfg.MailUIPort)
	}
	web := ServiceStatus{Name: "caddy", State: "running", PID: os.Getpid(), Uptime: "in-process"}
	if d.webErr != "" {
		web.State, web.LastError, web.Uptime = "error", d.webErr, ""
	}
	st.Services = append(st.Services, web)
	for _, s := range d.poolServices() {
		st.Services = append(st.Services, s.Status())
	}
	for _, kind := range serviceOrder {
		if h := d.handles[kind]; h != nil {
			st.Services = append(st.Services, h.svc.Status())
		} else if !d.cfg.Enabled(kind) && installed(kind) {
			st.Off = append(st.Off, kind)
		} else if installed(kind) {
			// Stopped from the dashboard (or its port was taken): on, not running.
			st.Services = append(st.Services, ServiceStatus{Name: kind, State: "stopped"})
		}
	}
	return st
}

func (d *daemon) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, d.status())
	})
	mux.HandleFunc("POST /reload", func(w http.ResponseWriter, r *http.Request) {
		if err := d.refresh(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, d.status())
	})
	mux.HandleFunc("POST /stop", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]bool{"stopping": true})
		d.quitOnce.Do(func() { close(d.quit) })
	})
	d.dashboardRoutes(mux)
	d.devRoutes(mux)
	d.updateRoutes(mux)
	return guard(mux, d.cfg.ControlPort)
}

// guard rejects requests a web page could forge: browsers cannot attach a
// custom header cross-origin without a CORS preflight (which we never
// approve), and the Host check defeats DNS rebinding. Only the dashboard's
// static files are served without the header; everything they do goes
// through the API, which requires it.
func guard(next http.Handler, port int) http.Handler {
	allowed := map[string]bool{
		fmt.Sprintf("127.0.0.1:%d", port): true,
		fmt.Sprintf("localhost:%d", port): true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		h.Set("Cache-Control", "no-store")
		static := r.Method == http.MethodGet && isDashboardAsset(r.URL.Path)
		if !allowed[r.Host] || (!static && r.Header.Get(client.Header) != "1") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
