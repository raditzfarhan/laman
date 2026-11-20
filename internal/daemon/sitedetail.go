package daemon

import (
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"laman/internal/caddyconf"
	"laman/internal/config"
	"laman/internal/install"
	"laman/internal/sites"
)

// The site detail page: what one project runs on, read from its own files
// and laman's state.

type siteDetail struct {
	Site     sites.Site             `json:"site"`
	Facts    projectFacts           `json:"facts"`
	Node     *nodeFact              `json:"node,omitempty"` // when package.json exists
	Packages []packageFact          `json:"packages"`       // composer, then node; only those with a manifest
	Size     sizeState              `json:"size"`
	Ports    webPorts               `json:"ports"`
	Services map[string]serviceFact `json:"services"` // mariadb, postgres, redis, mailpit
	Workers  *workersFact           `json:"workers"`
}

// workersFact: the site's queue worker and Reverb server. Only laravel is
// filled for a project without artisan.
type workersFact struct {
	Laravel bool             `json:"laravel"`
	Queue   queueWorkerFact  `json:"queue"`
	Reverb  reverbWorkerFact `json:"reverb"`
}

type queueWorkerFact struct {
	On      bool         `json:"on"` // switched on
	Status  WorkerStatus `json:"status"`
	Suggest bool         `json:"suggest"` // the .env queues jobs, and the worker is off
	Inline  bool         `json:"inline"`  // QUEUE_CONNECTION=sync/null: nothing to run
	Note    string       `json:"note,omitempty"`
}

type reverbWorkerFact struct {
	On           bool         `json:"on"`
	Status       WorkerStatus `json:"status"`
	Installed    bool         `json:"installed"` // laravel/reverb in composer.json or vendor
	Suggest      bool         `json:"suggest"`   // installed, and off
	Port         int          `json:"port,omitempty"`
	URL          string       `json:"url,omitempty"` // what browsers connect to, e.g. wss://acme.test/app
	EnvOK        bool         `json:"envOK"`         // on, and .env holds laman's values
	EnvMismatch  []string     `json:"envMismatch"`   // on: the keys that differ
	NeedsInstall bool         `json:"needsInstall"`  // no REVERB_APP_KEY: run php artisan reverb:install
}

type nodeFact struct {
	Version string `json:"version"`
	From    string `json:"from,omitempty"` // .nvmrc, .node-version or package.json; "" = the default
	PM      string `json:"pm"`
}

type packageFact struct {
	Kind    string `json:"kind"` // composer or node
	Present bool   `json:"present"`
	Size    int64  `json:"size"`
}

type webPorts struct {
	HTTP  int `json:"http"`
	HTTPS int `json:"https"`
}

type serviceFact struct {
	Version string `json:"version,omitempty"`
	Port    int    `json:"port"`
	UIPort  int    `json:"uiPort,omitempty"` // Mailpit's inbox
	Running bool   `json:"running"`
	Off     bool   `json:"off"` // switched off in laman's settings
}

// detailConfig is laman's current config (the daemon's own copy is from
// startup); tests swap it.
var detailConfig = func(d *daemon) *config.Config {
	if c, err := config.Load(); err == nil {
		return c
	}
	return d.cfg
}

func (d *daemon) siteDetail(s sites.Site) siteDetail {
	cfg := detailConfig(d)
	ports := localPorts{MySQL: cfg.MySQLPort, Postgres: cfg.PostgresPort, Redis: cfg.RedisPort, SMTP: cfg.SMTPPort, MailUI: cfg.MailUIPort}
	r := siteDetail{
		Site:     s,
		Facts:    readProjectFacts(s.Path, ports),
		Packages: []packageFact{},
		Size:     d.sizes.get(s.Path),
		Ports:    webPorts{HTTP: cfg.HTTPPort, HTTPS: cfg.HTTPSPort},
	}
	if exists(filepath.Join(s.Path, "package.json")) {
		v, from := cfg.NodeFor(s.Path)
		if from != "" {
			from = filepath.Base(from)
		}
		r.Node = &nodeFact{Version: v, From: from, PM: sites.PackageManager(s.Path)}
	}
	if exists(filepath.Join(s.Path, "composer.json")) {
		r.Packages = append(r.Packages, packageFact{Kind: "composer", Present: exists(filepath.Join(s.Path, "vendor")), Size: r.Size.Sizes.Vendor})
	}
	if r.Node != nil {
		r.Packages = append(r.Packages, packageFact{Kind: "node", Present: exists(filepath.Join(s.Path, "node_modules")), Size: r.Size.Sizes.NodeModules})
	}
	// A package folder appeared or went since the last measurement (e.g.
	// `npm install` in a terminal): measure again rather than show old sizes.
	if r.Size.State == "done" {
		for _, p := range r.Packages {
			was := r.Size.Sizes.HasVendor
			if p.Kind == "node" {
				was = r.Size.Sizes.HasNodeModules
			}
			if p.Present != was {
				d.sizes.refresh(s.Path)
				r.Size.State = "measuring"
				break
			}
		}
	}
	d.mu.Lock()
	running := func(kind string) bool { return d.handles[kind] != nil }
	r.Services = map[string]serviceFact{
		"mariadb":  {Version: cfg.MariaDB, Port: cfg.MySQLPort, Running: running("mariadb"), Off: !cfg.Enabled("mariadb")},
		"postgres": {Version: cfg.Postgres, Port: cfg.PostgresPort, Running: running("postgres"), Off: !cfg.Enabled("postgres")},
		"redis":    {Version: cfg.Redis, Port: cfg.RedisPort, Running: running("redis"), Off: !cfg.Enabled("redis")},
		"mailpit":  {Port: cfg.SMTPPort, UIPort: cfg.MailUIPort, Running: running("mailpit"), Off: !cfg.Enabled("mailpit")},
	}
	d.mu.Unlock()
	r.Workers = d.workersFact(s, cfg)
	return r
}

// workersFact reads the site's files only: no PHP runs here.
func (d *daemon) workersFact(s sites.Site, cfg *config.Config) *workersFact {
	f := &workersFact{Laravel: exists(filepath.Join(s.Path, "artisan")), Reverb: reverbWorkerFact{EnvMismatch: []string{}}}
	if !f.Laravel {
		return f
	}
	vals, hasEnv := readEnvFile(s.Path)
	_, appKey := vals["APP_KEY"]
	plan := planQueue(s.Path, vals, nil)
	q := &f.Queue
	q.On, q.Status, q.Inline, q.Note = cfg.QueueWorkers[s.Name], d.workerState(workerQueue, s.Host), plan.Inline, plan.Note
	q.Suggest = hasEnv && appKey && !plan.Inline && !q.On

	rv := &f.Reverb
	rv.On, rv.Status = cfg.Reverb[s.Name], d.workerState(workerReverb, s.Host)
	rv.Installed = composerRequires(s.Path, "laravel/reverb")
	rv.Suggest = rv.Installed && !rv.On
	rv.Port = cfg.ReverbPorts[s.Name]
	rv.NeedsInstall = vals["REVERB_APP_KEY"] == ""
	if rv.On && rv.Port > 0 {
		scheme, port := d.siteEndpoint(s.Host)
		rv.URL = "ws" + strings.TrimPrefix(caddyconf.URL(s.Host, scheme == "https", port), "http") + "app"
		rv.EnvMismatch = envMismatch(vals, reverbEnv(s.Host, scheme, port, rv.Port))
		if rv.EnvMismatch == nil {
			rv.EnvMismatch = []string{}
		}
		rv.EnvOK = len(rv.EnvMismatch) == 0
	}
	return f
}

// forgetGoneSizes drops size measurements of folders no site uses any more.
func (d *daemon) forgetGoneSizes() {
	d.mu.Lock()
	live := map[string]bool{}
	for _, s := range d.sites {
		live[s.Path] = true
	}
	d.mu.Unlock()
	d.sizes.mu.Lock()
	for root := range d.sizes.entries {
		if !live[root] {
			delete(d.sizes.entries, root)
		}
	}
	d.sizes.mu.Unlock()
}

func (d *daemon) siteDetailRoutes(mux *http.ServeMux) {
	d.siteInstallRoutes(mux)
	d.envRoutes(mux)
	mux.HandleFunc("GET /api/sites/detail", func(w http.ResponseWriter, r *http.Request) {
		host := r.URL.Query().Get("site")
		s, ok := d.siteByHost(host)
		if !ok {
			d.forgetGoneSizes()
			fail(w, http.StatusNotFound, "no site %s", host)
			return
		}
		writeJSON(w, d.siteDetail(s))
	})
	mux.HandleFunc("POST /api/sites/sizes", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Site string }
		if err := readJSON(r, &req); err != nil {
			fail(w, http.StatusBadRequest, "bad request: %v", err)
			return
		}
		s, ok := d.siteByHost(req.Site)
		if !ok {
			fail(w, http.StatusNotFound, "no site %s", req.Site)
			return
		}
		d.sizes.refresh(s.Path)
		writeJSON(w, struct {
			OK bool `json:"ok"`
		}{true})
	})
}

// installCmd builds the command that installs a project's missing
// packages: Composer through laman (the site's PHP), or the project's own
// Node package manager with the project's Node first on PATH.
func installCmd(s sites.Site, kind string) (cmd *exec.Cmd, title string, err error) {
	switch kind {
	case "composer":
		if !exists(filepath.Join(s.Path, "composer.json")) {
			return nil, "", errors.New(s.Host + " has no composer.json")
		}
		exe, err := os.Executable()
		if err != nil {
			return nil, "", err
		}
		cmd = exec.Command(exe, "composer", "install", "--no-interaction")
		title = "Installing Composer packages for " + s.Host
	case "node":
		if !exists(filepath.Join(s.Path, "package.json")) {
			return nil, "", errors.New(s.Host + " has no package.json")
		}
		pm := sites.PackageManager(s.Path)
		if pm == "bun" && !install.HaveBun() {
			return nil, "", errors.New("bun is not installed; run laman install bun")
		}
		cmd = shellCmd(pm + " install")
		cmd.Env = devEnv(s.Path, 0)
		title = "Installing node_modules for " + s.Host
	default:
		return nil, "", errors.New("unknown package kind " + kind)
	}
	cmd.Dir = s.Path
	return cmd, title, nil
}

func (d *daemon) siteInstallRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/sites/install", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Site, Kind string }
		if err := readJSON(r, &req); err != nil {
			fail(w, http.StatusBadRequest, "bad request: %v", err)
			return
		}
		s, ok := d.siteByHost(req.Site)
		if !ok {
			fail(w, http.StatusNotFound, "no site %s", req.Site)
			return
		}
		cmd, title, err := installCmd(s, req.Kind)
		if err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		if err := d.runJobCmd(title, cmd, func() { d.sizes.refresh(s.Path) }); err != nil {
			fail(w, http.StatusConflict, "%v", err)
			return
		}
		writeJSON(w, d.installJob.state())
	})
}
