package daemon

import (
	"fmt"
	"net/http"

	"laman/internal/config"
	"laman/internal/install"
)

// PHP extensions from the dashboard: list, switch on/off, install and
// uninstall, per PHP version. Each change restarts only that version's
// workers.

// phpVersion resolves "8.4" or "8.4.27-1" to an installed version, or the
// default PHP when empty.
func phpVersion(want string) (string, error) {
	if want == "" {
		cfg, err := config.Load()
		if err != nil {
			return "", err
		}
		if v := cfg.DefaultPHP(); v != "" {
			return v, nil
		}
		return "", fmt.Errorf("no PHP installed")
	}
	if !safeArg(stripBuild(want)) {
		return "", fmt.Errorf("bad PHP version %q", want)
	}
	if v := config.Match("php", want); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("PHP %s is not installed", want)
}

// stripBuild drops the "-1" build suffix of PHP versions for validation.
func stripBuild(v string) string {
	for i := len(v) - 1; i >= 0; i-- {
		if v[i] == '-' {
			return v[:i]
		}
	}
	return v
}

func validExtName(s string) bool {
	if s == "" || len(s) > 40 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

func (d *daemon) extRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/ext", func(w http.ResponseWriter, r *http.Request) {
		v, err := phpVersion(r.URL.Query().Get("php"))
		if err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		list, err := install.ListExt(v)
		if err != nil {
			fail(w, http.StatusInternalServerError, "%v", err)
			return
		}
		writeJSON(w, struct {
			PHP  string        `json:"php"`
			Exts []install.Ext `json:"exts"`
			Job  jobState      `json:"job"`
		}{v, list, d.installJob.state()})
	})
	mux.HandleFunc("POST /api/ext/enable", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			PHP, Name string
			Enabled   bool
		}
		if err := readJSON(r, &req); err != nil || !validExtName(req.Name) {
			fail(w, http.StatusBadRequest, "bad request")
			return
		}
		d.installJob.clear()
		v, err := phpVersion(req.PHP)
		if err == nil {
			err = install.SetExt(v, req.Name, req.Enabled)
		}
		if err == nil {
			err = d.restartPHP(v)
		}
		if err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		writeJSON(w, struct{ OK bool }{true})
	})
	mux.HandleFunc("POST /api/ext/install", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ PHP, Name string }
		if err := readJSON(r, &req); err != nil || !validExtName(req.Name) {
			fail(w, http.StatusBadRequest, "bad request")
			return
		}
		v, err := phpVersion(req.PHP)
		if err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		// The CLI downloads, enables, and asks this daemon to restart PHP.
		if err := d.runJob("Installing "+req.Name+" for PHP "+stripBuild(v), []string{"ext", "install", req.Name, "--php", v}, nil); err != nil {
			fail(w, http.StatusConflict, "%v", err)
			return
		}
		writeJSON(w, d.installJob.state())
	})
	mux.HandleFunc("POST /api/ext/uninstall", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ PHP, Name string }
		if err := readJSON(r, &req); err != nil || !validExtName(req.Name) {
			fail(w, http.StatusBadRequest, "bad request")
			return
		}
		d.installJob.clear()
		v, err := phpVersion(req.PHP)
		if err == nil {
			err = d.uninstallExt(v, req.Name)
		}
		if err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		writeJSON(w, struct{ OK bool }{true})
	})
	mux.HandleFunc("POST /api/php/restart", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ PHP string }
		if err := readJSON(r, &req); err != nil {
			fail(w, http.StatusBadRequest, "bad request")
			return
		}
		v, err := phpVersion(req.PHP)
		if err == nil {
			err = d.restartPHP(v)
		}
		if err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		writeJSON(w, struct{ OK bool }{true})
	})
}

// uninstallExt turns the extension off and restarts the workers first:
// Windows will not delete a DLL a running php-cgi has loaded.
func (d *daemon) uninstallExt(version, name string) error {
	if err := install.SetExt(version, name, false); err != nil {
		return err
	}
	if err := d.restartPHP(version); err != nil {
		return err
	}
	return install.UninstallExt(version, name)
}
