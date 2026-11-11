package daemon

import (
	"net/http"
	"sync"
	"time"

	"laman/internal/config"
	"laman/internal/install"
)

// Apps from the dashboard. Installs, updates and removals run the
// `laman app` CLI as the background job, so they behave exactly like the
// terminal; the CLI tells this daemon to start serving the result.

type appInfo struct {
	*install.AppSpec
	Installed string `json:"installed,omitempty"` // version, "" when not installed
	Latest    string `json:"latest,omitempty"`    // newest on offer, when known
	Default   bool   `json:"default,omitempty"`   // behind db.<tld>
}

// appLatest caches each app's newest version for an hour, fetched in the
// background so the page never waits on GitHub.
type appLatest struct {
	mu       sync.Mutex
	versions map[string]string
	at       time.Time
	fetching bool
}

func (a *appLatest) get() map[string]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.fetching && time.Since(a.at) > time.Hour {
		a.fetching = true
		go func() {
			found := map[string]string{}
			for _, spec := range install.AppCatalog {
				if v, err := install.LatestApp(spec); err == nil {
					found[spec.Name] = v
				}
			}
			a.mu.Lock()
			a.versions, a.at, a.fetching = found, time.Now(), false
			a.mu.Unlock()
		}()
	}
	out := map[string]string{}
	for k, v := range a.versions {
		out[k] = v
	}
	return out
}

func appArg(r *http.Request) (*install.AppSpec, bool) {
	var req struct{ Name string }
	if err := readJSON(r, &req); err != nil {
		return nil, false
	}
	spec := install.AppByName(req.Name)
	return spec, spec != nil
}

func (d *daemon) appRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/apps", func(w http.ResponseWriter, r *http.Request) {
		cfg, err := config.Load()
		if err != nil {
			fail(w, http.StatusInternalServerError, "%v", err)
			return
		}
		latest := d.appLatest.get()
		var list []appInfo
		for _, spec := range install.AppCatalog {
			list = append(list, appInfo{AppSpec: spec, Installed: cfg.Apps[spec.Name], Latest: latest[spec.Name],
				Default: cfg.DefaultDBClient() == spec.Name})
		}
		writeJSON(w, struct {
			Apps []appInfo `json:"apps"`
			TLD  string    `json:"tld"`
			Job  jobState  `json:"job"`
		}{list, cfg.TLD, d.installJob.state()})
	})
	job := func(verb string, args func(name string) []string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			spec, ok := appArg(r)
			if !ok {
				fail(w, http.StatusBadRequest, "bad request")
				return
			}
			if err := d.runJob(verb+" "+spec.Title, args(spec.Name), nil); err != nil {
				fail(w, http.StatusConflict, "%v", err)
				return
			}
			writeJSON(w, d.installJob.state())
		}
	}
	mux.HandleFunc("POST /api/apps/install", job("Installing", func(n string) []string { return []string{"app", "install", n} }))
	mux.HandleFunc("POST /api/apps/update", job("Updating", func(n string) []string { return []string{"app", "update", n} }))
	mux.HandleFunc("POST /api/apps/remove", job("Removing", func(n string) []string { return []string{"app", "remove", n, "--yes"} }))
	mux.HandleFunc("POST /api/apps/default", func(w http.ResponseWriter, r *http.Request) {
		spec, ok := appArg(r)
		if !ok || spec.Category != "database" {
			fail(w, http.StatusBadRequest, "bad request")
			return
		}
		d.installJob.clear()
		err := d.changeConfig(func(c *config.Config) error {
			if _, ok := c.Apps[spec.Name]; ok {
				c.DBClient = spec.Name
			}
			return nil
		})
		if err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		writeJSON(w, struct{ OK bool }{true})
	})
}
