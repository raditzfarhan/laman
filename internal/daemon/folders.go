package daemon

import (
	"net/http"
	"sync"

	"laman/internal/config"
	"laman/internal/desktop"
)

// Project folders from the dashboard: list (in Status), add (typed or
// picked in Windows' folder picker), remove, open in Explorer.

var pickLock sync.Mutex // one folder picker at a time

func (d *daemon) folderRoutes(mux *http.ServeMux) {
	edit := func(change func(c *config.Config, path string) (string, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var req struct{ Path string }
			if err := readJSON(r, &req); err != nil || req.Path == "" {
				fail(w, http.StatusBadRequest, "path required")
				return
			}
			cfg, err := config.Load()
			if err != nil {
				fail(w, http.StatusInternalServerError, "%v", err)
				return
			}
			path, err := change(cfg, req.Path)
			if err != nil {
				fail(w, http.StatusBadRequest, "%v", err)
				return
			}
			if err := cfg.Save(); err != nil {
				fail(w, http.StatusInternalServerError, "%v", err)
				return
			}
			if err := d.refresh(); err != nil {
				fail(w, http.StatusInternalServerError, "%v", err)
				return
			}
			writeJSON(w, struct{ Path string }{path})
		}
	}
	mux.HandleFunc("POST /api/folders/add", edit((*config.Config).AddSitesDir))
	mux.HandleFunc("POST /api/folders/remove", edit((*config.Config).RemoveSitesDir))

	// The dashboard's theme; the localhost page is rewritten to match.
	mux.HandleFunc("POST /api/theme", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Theme string }
		if err := readJSON(r, &req); err != nil || (req.Theme != "" && req.Theme != "light" && req.Theme != "dark") {
			fail(w, http.StatusBadRequest, "theme must be light, dark or empty")
			return
		}
		cfg, err := config.Load()
		if err != nil {
			fail(w, http.StatusInternalServerError, "%v", err)
			return
		}
		cfg.Theme = req.Theme
		if err := cfg.Save(); err != nil {
			fail(w, http.StatusInternalServerError, "%v", err)
			return
		}
		if err := d.refresh(); err != nil {
			fail(w, http.StatusInternalServerError, "%v", err)
			return
		}
		writeJSON(w, struct{ OK bool }{true})
	})

	// Name prefix for a folder's sites ("" for none).
	mux.HandleFunc("POST /api/folders/prefix", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Path, Prefix string }
		if err := readJSON(r, &req); err != nil || req.Path == "" {
			fail(w, http.StatusBadRequest, "path required")
			return
		}
		cfg, err := config.Load()
		if err != nil {
			fail(w, http.StatusInternalServerError, "%v", err)
			return
		}
		if _, _, err := cfg.SetFolderPrefix(req.Path, req.Prefix); err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		if err := cfg.Save(); err != nil {
			fail(w, http.StatusInternalServerError, "%v", err)
			return
		}
		if err := d.refresh(); err != nil {
			fail(w, http.StatusInternalServerError, "%v", err)
			return
		}
		writeJSON(w, struct{ OK bool }{true})
	})

	// Windows' folder picker, on this computer (a web page cannot browse
	// folders). Returns "" when the user cancels.
	mux.HandleFunc("POST /api/folders/pick", func(w http.ResponseWriter, r *http.Request) {
		if !pickLock.TryLock() {
			fail(w, http.StatusConflict, "the folder picker is already open")
			return
		}
		defer pickLock.Unlock()
		path, err := desktop.PickFolder("Choose a project folder: every folder inside it becomes a .test site")
		if err != nil {
			fail(w, http.StatusInternalServerError, "%v", err)
			return
		}
		writeJSON(w, struct{ Path string }{path})
	})

	mux.HandleFunc("POST /api/folders/open", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Path string }
		if err := readJSON(r, &req); err != nil {
			fail(w, http.StatusBadRequest, "path required")
			return
		}
		// Only project folders: the API is not a general "open anything".
		for _, dir := range d.cfg.SitesDirs() {
			if dir == req.Path {
				if err := desktop.OpenFolder(dir); err != nil {
					fail(w, http.StatusInternalServerError, "%v", err)
					return
				}
				writeJSON(w, struct{ OK bool }{true})
				return
			}
		}
		fail(w, http.StatusBadRequest, "not a project folder")
	})
}
