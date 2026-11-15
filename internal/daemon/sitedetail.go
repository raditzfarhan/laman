package daemon

import (
	"net/http"
	"path/filepath"

	"laman/internal/config"
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
	d.mu.Lock()
	running := func(kind string) bool { return d.handles[kind] != nil }
	r.Services = map[string]serviceFact{
		"mariadb":  {Version: cfg.MariaDB, Port: cfg.MySQLPort, Running: running("mariadb"), Off: !cfg.Enabled("mariadb")},
		"postgres": {Version: cfg.Postgres, Port: cfg.PostgresPort, Running: running("postgres"), Off: !cfg.Enabled("postgres")},
		"redis":    {Version: cfg.Redis, Port: cfg.RedisPort, Running: running("redis"), Off: !cfg.Enabled("redis")},
		"mailpit":  {Port: cfg.SMTPPort, UIPort: cfg.MailUIPort, Running: running("mailpit"), Off: !cfg.Enabled("mailpit")},
	}
	d.mu.Unlock()
	return r
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
