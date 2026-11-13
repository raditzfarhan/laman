package daemon

import (
	"net/http"
	"path/filepath"
	"regexp"
	"strings"

	"laman/internal/config"
)

// The dashboard's "New project" runs `laman new` as the background job,
// so it does exactly what the command does.

var (
	newName    = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	newStarter = map[string]bool{"laravel": true, "laravel-react": true, "laravel-vue": true, "laravel-livewire": true,
		"wordpress": true, "php": true, "vite": true}
	newDB = map[string]bool{"": true, "mariadb": true, "postgres": true, "sqlite": true}
)

// newArgs turns a dashboard request into `laman new` arguments, refusing
// anything the form could not have sent.
func newArgs(cfg *config.Config, name, starter, db, php, folder string) ([]string, bool) {
	if !newName.MatchString(name) || len(name) > 50 || !newStarter[starter] || !newDB[db] || !safeArg(php) {
		return nil, false
	}
	args := []string{"new", name, starter}
	if db != "" {
		args = append(args, "--db", db)
	}
	if php != "" {
		args = append(args, "--php", php)
	}
	if folder != "" {
		found := false
		for _, d := range cfg.SitesDirs() {
			if strings.EqualFold(filepath.Clean(d), filepath.Clean(folder)) {
				found = true
			}
		}
		if !found {
			return nil, false
		}
		args = append(args, "--in", folder)
	}
	return args, true
}

func (d *daemon) newProjectRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/new", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Name, Starter, DB, PHP, Folder string }
		if err := readJSON(r, &req); err != nil {
			fail(w, http.StatusBadRequest, "bad request: %v", err)
			return
		}
		cfg, err := config.Load()
		if err != nil {
			fail(w, http.StatusInternalServerError, "%v", err)
			return
		}
		args, ok := newArgs(cfg, req.Name, req.Starter, req.DB, req.PHP, req.Folder)
		if !ok {
			fail(w, http.StatusBadRequest, "use lowercase letters, digits and dashes for the name, and pick a starter")
			return
		}
		if err := d.runJob("Creating "+req.Name, args, func() { d.refresh() }); err != nil {
			fail(w, http.StatusConflict, "%v", err)
			return
		}
		writeJSON(w, d.installJob.state())
	})
}
