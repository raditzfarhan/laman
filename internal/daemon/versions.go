package daemon

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"laman/internal/config"
	"laman/internal/desktop"
	"laman/internal/install"
)

// Version switching for the dashboard and tray. Switching a database swaps
// only that server in place; PHP and the web server keep serving. Installs
// run the `laman install` CLI as a background job, so they behave exactly
// like the terminal (pinning, PATH for Node, PHP extensions). Upgrades run
// `laman upgrade` outside the daemon, because it restarts laman.

var kindNames = map[string]string{
	"php": "PHP", "node": "Node", "mariadb": "MariaDB", "postgres": "PostgreSQL", "redis": "Redis",
}

var versionKinds = []string{"php", "node", "mariadb", "postgres", "redis"}

func isServer(kind string) bool { return kind == "mariadb" || kind == "postgres" || kind == "redis" }

func pinnedLine(cfg *config.Config, kind string) *string {
	switch kind {
	case "mariadb":
		return &cfg.MariaDB
	case "postgres":
		return &cfg.Postgres
	case "redis":
		return &cfg.Redis
	}
	return nil
}

func serverPort(cfg *config.Config, kind string) int {
	switch kind {
	case "mariadb":
		return cfg.MySQLPort
	case "postgres":
		return cfg.PostgresPort
	case "redis":
		return cfg.RedisPort
	}
	return 0
}

// activeVersion is the version of kind laman uses by default.
func activeVersion(cfg *config.Config, kind string) string {
	switch kind {
	case "php":
		return cfg.DefaultPHP()
	case "node":
		return cfg.DefaultNode()
	}
	return config.Active(kind, *pinnedLine(cfg, kind))
}

type versionInfo struct {
	Kind      string   `json:"kind"`
	Name      string   `json:"name"`
	Active    string   `json:"active,omitempty"`
	Installed []string `json:"installed"`
	// Server kinds: whether laman is running it right now, and whether it
	// is turned on (runs with laman).
	Running bool `json:"running,omitempty"`
	Server  bool `json:"server,omitempty"`
	Enabled bool `json:"enabled,omitempty"`
}

func (d *daemon) versions() ([]versionInfo, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	var out []versionInfo
	for _, kind := range versionKinds {
		vi := versionInfo{Kind: kind, Name: kindNames[kind], Active: activeVersion(cfg, kind),
			Installed: config.Installed(kind), Server: isServer(kind)}
		if vi.Installed == nil {
			vi.Installed = []string{}
		}
		if vi.Server {
			d.mu.Lock()
			vi.Running = d.handles[kind] != nil
			d.mu.Unlock()
			vi.Enabled = cfg.Enabled(kind)
		}
		out = append(out, vi)
	}
	return out, nil
}

func portBusy(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return true
	}
	ln.Close()
	return false
}

// useVersion makes an installed version the default. For a database it
// stops that one server and starts the chosen version on the same port.
func (d *daemon) useVersion(kind, want string) (string, error) {
	if kindNames[kind] == "" {
		return "", fmt.Errorf("unknown component %q", kind)
	}
	v := config.Match(kind, strings.TrimPrefix(want, "v"))
	if v == "" {
		return "", fmt.Errorf("%s %s is not installed", kindNames[kind], want)
	}
	switch kind {
	case "php":
		return v, d.changeConfig(func(c *config.Config) error { c.PHP = v; return nil })
	case "node":
		cfg, err := config.Load()
		if err != nil {
			return "", err
		}
		cfg.Node = v
		if err := cfg.Save(); err != nil {
			return "", err
		}
		return v, desktop.Junction(config.NodeLink(), install.NodeDir(v))
	}
	return d.switchServer(kind, v)
}

// changeConfig edits config.json and refreshes sites and PHP pools.
func (d *daemon) changeConfig(change func(*config.Config) error) error {
	cfg, err := config.Load()
	if err == nil {
		err = change(cfg)
	}
	if err == nil {
		err = cfg.Save()
	}
	if err != nil {
		return err
	}
	return d.refresh()
}

func (d *daemon) switchServer(kind, v string) (string, error) {
	d.switchMu.Lock()
	defer d.switchMu.Unlock()
	cfg, err := config.Load()
	if err != nil {
		return "", err
	}
	if err := install.PinVersions(cfg); err != nil {
		return "", err
	}
	line := config.LineOf(kind, v)
	*pinnedLine(cfg, kind) = line
	v = config.Active(kind, line) // newest patch of that line
	// Prepare the new line's data before stopping the old server, so the
	// switch itself only takes as long as a stop and a start.
	switch kind {
	case "mariadb":
		err = install.InitMariaDB(v, cfg.MySQLPort)
	case "postgres":
		err = install.InitPostgres(v)
	}
	if err != nil {
		return "", err
	}
	if err := cfg.Save(); err != nil {
		return "", err
	}
	logf("switching %s to %s", kind, v)
	d.stopService(kind)

	d.mu.Lock()
	*pinnedLine(d.cfg, kind) = line
	switch kind {
	case "mariadb":
		d.mariadb, d.mariadbData = install.MariaDBInstallDir(v), install.MariaDBDataDir(v)
	case "postgres":
		d.postgres = v
	case "redis":
		d.redis, d.redisData = install.RedisDir(v), install.RedisDataDir(v)
	}
	d.skipped = dropNotes(d.skipped, kindNames[kind])
	d.mu.Unlock()

	if !cfg.Enabled(kind) {
		return v, nil // switched while turned off: it stays off
	}
	if port := serverPort(d.cfg, kind); portBusy(port) {
		note := fmt.Sprintf("%s is not running: port %d is already in use by another program (WSL, Docker or Laragon?). Stop that one and run `laman restart`, or change the port in config.json.", kindNames[kind], port)
		d.mu.Lock()
		d.skipped = append(d.skipped, note)
		d.mu.Unlock()
		return v, errors.New(note)
	}
	d.startService(kind, d.buildService(kind))
	return v, nil
}

func dropNotes(notes []string, prefix string) []string {
	var out []string
	for _, n := range notes {
		if !strings.HasPrefix(n, prefix+" ") {
			out = append(out, n)
		}
	}
	return out
}

// runJob runs `laman <args>` without a window as the one background job.
// The job message is the command's last line of output.
func (d *daemon) runJob(title string, args []string, after func()) error {
	j := &d.installJob
	j.mu.Lock()
	if j.running {
		j.mu.Unlock()
		return errBusy
	}
	j.running, j.message, j.err = true, title+"…", ""
	j.mu.Unlock()
	go func() {
		exe, err := os.Executable()
		var out []byte
		if err == nil {
			cmd := exec.Command(exe, args...)
			noWindow(cmd)
			out, err = cmd.CombinedOutput()
		}
		last := lastLine(string(out))
		j.mu.Lock()
		j.running = false
		if err != nil {
			j.message, j.err = "", strings.TrimPrefix(last, "laman: ")
			if j.err == "" {
				j.err = err.Error()
			}
			logf("dashboard: laman %s failed: %v: %s", strings.Join(args, " "), err, last)
		} else {
			// The CLI's closing lines are terminal hints; here the lists
			// update by themselves, so just say what happened.
			j.message = strings.NewReplacer("Installing", "Installed", "Updating", "Updated", "Removing", "Removed", "Creating", "Created").Replace(title)
		}
		j.mu.Unlock()
		if err == nil && after != nil {
			after()
		}
	}()
	return nil
}

var errBusy = errors.New("another install is still running")

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n")), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" && !strings.Contains(l, " MB") {
			return l
		}
	}
	return ""
}

// startInstalled starts a database server that was just installed for the
// first time, without a full restart.
func (d *daemon) startInstalled(kind string) {
	d.mu.Lock()
	running := d.handles[kind] != nil
	d.mu.Unlock()
	cfg, err := config.Load()
	if running || err != nil {
		return
	}
	if v := activeVersion(cfg, kind); v != "" {
		if _, err := d.switchServer(kind, v); err != nil {
			logf("could not start %s %s: %v", kind, v, err)
		}
	}
}

// availableLines caches what each source offers, for an hour.
type availableLines struct {
	mu    sync.Mutex
	lists map[string][]string
	at    map[string]time.Time
}

func (a *availableLines) get(kind string) ([]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lists == nil {
		a.lists, a.at = map[string][]string{}, map[string]time.Time{}
	}
	if l, ok := a.lists[kind]; ok && time.Since(a.at[kind]) < time.Hour {
		return l, nil
	}
	l, err := install.Lines(kind)
	if err != nil {
		return a.lists[kind], err
	}
	a.lists[kind], a.at[kind] = l, time.Now()
	return l, nil
}

func (d *daemon) versionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/versions", func(w http.ResponseWriter, r *http.Request) {
		list, err := d.versions()
		if err != nil {
			fail(w, http.StatusInternalServerError, "%v", err)
			return
		}
		writeJSON(w, struct {
			Versions []versionInfo `json:"versions"`
			Job      jobState      `json:"job"`
		}{list, d.installJob.state()})
	})
	mux.HandleFunc("GET /api/versions/available", func(w http.ResponseWriter, r *http.Request) {
		kind := r.URL.Query().Get("kind")
		if kindNames[kind] == "" {
			fail(w, http.StatusBadRequest, "unknown component %q", kind)
			return
		}
		l, err := d.available.get(kind)
		resp := struct {
			Lines []string `json:"lines"`
			Error string   `json:"error,omitempty"`
		}{Lines: l}
		if err != nil {
			resp.Error = "could not fetch the list: " + err.Error()
		}
		if resp.Lines == nil {
			resp.Lines = []string{}
		}
		writeJSON(w, resp)
	})
	mux.HandleFunc("POST /api/versions/use", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Kind, Version string }
		if err := readJSON(r, &req); err != nil {
			fail(w, http.StatusBadRequest, "bad request: %v", err)
			return
		}
		d.installJob.clear()
		v, err := d.useVersion(req.Kind, req.Version)
		if err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		writeJSON(w, struct {
			Kind    string `json:"kind"`
			Version string `json:"version"`
		}{req.Kind, v})
	})
	mux.HandleFunc("POST /api/versions/install", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Kind, Line string }
		if err := readJSON(r, &req); err != nil || kindNames[req.Kind] == "" || !safeArg(req.Line) {
			fail(w, http.StatusBadRequest, "bad request")
			return
		}
		args := []string{"install", req.Kind}
		if req.Line != "" {
			args = append(args, req.Line)
		}
		var after func()
		if isServer(req.Kind) {
			after = func() { d.startInstalled(req.Kind) }
		}
		title := strings.TrimSpace("Installing " + kindNames[req.Kind] + " " + req.Line)
		if err := d.runJob(title, args, after); err != nil {
			fail(w, http.StatusConflict, "%v", err)
			return
		}
		writeJSON(w, d.installJob.state())
	})
	mux.HandleFunc("POST /api/services/enable", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Kind    string
			Enabled bool
		}
		if err := readJSON(r, &req); err != nil {
			fail(w, http.StatusBadRequest, "bad request: %v", err)
			return
		}
		d.installJob.clear()
		if err := d.setEnabled(req.Kind, req.Enabled); err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		writeJSON(w, d.status())
	})
	mux.HandleFunc("POST /api/versions/upgrade", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Kind, Line string }
		if err := readJSON(r, &req); err != nil || !isServer(req.Kind) || req.Line == "" || !safeArg(req.Line) {
			fail(w, http.StatusBadRequest, "bad request")
			return
		}
		if err := d.startUpgrade(req.Kind, req.Line); err != nil {
			fail(w, http.StatusConflict, "%v", err)
			return
		}
		writeJSON(w, struct {
			Started bool `json:"started"`
		}{true})
	})
}

// installed reports whether an add-on server is installed at all.
func installed(kind string) bool {
	if kind == "mailpit" {
		return install.MailpitExe() != ""
	}
	return config.Latest(kind) != ""
}

// setEnabled turns an add-on server on or off: now, and for every later
// start of laman.
func (d *daemon) setEnabled(kind string, on bool) error {
	known := false
	for _, k := range serviceOrder {
		known = known || k == kind
	}
	if !known {
		return fmt.Errorf("unknown service %q (mariadb, postgres, redis, mailpit)", kind)
	}
	if on && !installed(kind) {
		return fmt.Errorf("%s is not installed; run `laman install %s`", serviceName(kind), kind)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.SetEnabled(kind, on)
	if err := cfg.Save(); err != nil {
		return err
	}
	d.mu.Lock()
	d.cfg.SetEnabled(kind, on)
	running := d.handles[kind] != nil
	if !on {
		d.skipped = dropNotes(d.skipped, serviceName(kind))
	}
	d.mu.Unlock()

	switch {
	case !on && running:
		logf("turning %s off", kind)
		d.stopService(kind)
	case on && !running:
		logf("turning %s on", kind)
		if kind == "mailpit" {
			return d.startMailpit()
		}
		// Same path as a version switch: prepares data, checks the port.
		_, err := d.switchServer(kind, config.Active(kind, *pinnedLine(cfg, kind)))
		return err
	}
	return nil
}

func (d *daemon) startMailpit() error {
	d.mu.Lock()
	d.mailpit = install.MailpitExe()
	d.skipped = dropNotes(d.skipped, "Mailpit")
	d.mu.Unlock()
	for _, port := range []int{d.cfg.SMTPPort, d.cfg.MailUIPort} {
		if portBusy(port) {
			note := fmt.Sprintf("Mailpit is not running: port %d is already in use by another program (WSL, Docker or Laragon?). Stop that one and run `laman restart`, or change the port in config.json.", port)
			d.mu.Lock()
			d.skipped = append(d.skipped, note)
			d.mu.Unlock()
			return errors.New(note)
		}
	}
	d.startService("mailpit", d.buildService("mailpit"))
	return nil
}

func serviceName(kind string) string {
	if kind == "mailpit" {
		return "Mailpit"
	}
	return kindNames[kind]
}

// safeArg accepts version lines like "8.4", "12.3", "18", "22.11.0".
func safeArg(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && c != '.' {
			return false
		}
	}
	return len(s) <= 20
}

// startUpgrade runs `laman upgrade <kind> <line>` detached and outside the
// daemon's job, because the upgrade stops and restarts this daemon. Its
// output goes to logs/upgrade.log.
func (d *daemon) startUpgrade(kind, line string) error {
	if d.installJob.state().Running {
		return errBusy
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logPath := filepath.Join(config.LogDir(), "upgrade.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Fprintf(f, "\n[laman %s] upgrade %s %s (from the dashboard)\n", time.Now().Format(time.RFC3339), kind, line)
	cmd := exec.Command(exe, "upgrade", kind, line)
	cmd.Stdout, cmd.Stderr = f, f
	if err := spawnOutsideJob(cmd); err != nil {
		return fmt.Errorf("could not start the upgrade outside laman (%v); run `laman upgrade %s %s` in a terminal instead", err, kind, line)
	}
	logf("dashboard started: laman upgrade %s %s (pid %d)", kind, line, cmd.Process.Pid)
	return cmd.Process.Release()
}
