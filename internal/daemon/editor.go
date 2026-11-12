package daemon

import (
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"laman/internal/config"
	"laman/internal/desktop"
	"laman/internal/editor"
)

// Installed editors, looked up at most once a minute: the dashboard polls
// status every two seconds.
var editorCache struct {
	sync.Mutex
	found []editor.Editor
	at    time.Time
}

func foundEditors() []editor.Editor {
	editorCache.Lock()
	defer editorCache.Unlock()
	if editorCache.at.IsZero() || time.Since(editorCache.at) > time.Minute {
		editorCache.found, editorCache.at = editor.Found(), time.Now()
	}
	return editorCache.found
}

// editorName is the name of the editor "Open in editor" uses ("" = none).
func (d *daemon) editorName() string {
	if e, ok := editor.Pick(d.cfg.Editor, foundEditors()); ok {
		return e.Name
	}
	return ""
}

func (d *daemon) editorRoutes(mux *http.ServeMux) {
	// A site's folder in Explorer or in the editor. Only sites laman
	// serves: the API is not a general "open anything".
	mux.HandleFunc("POST /api/sites/reveal", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Site, In string }
		if err := readJSON(r, &req); err != nil || req.Site == "" {
			fail(w, http.StatusBadRequest, "site required")
			return
		}
		s, ok := d.findSite(req.Site)
		if !ok || s.Path == "" {
			fail(w, http.StatusNotFound, "%s has no folder on this computer", req.Site)
			return
		}
		switch req.In {
		case "folder":
			if err := desktop.OpenFolder(s.Path); err != nil {
				fail(w, http.StatusInternalServerError, "%v", err)
				return
			}
		case "editor":
			d.mu.Lock()
			choice := d.cfg.Editor
			d.mu.Unlock()
			e, ok := editor.Pick(choice, foundEditors())
			if !ok {
				fail(w, http.StatusNotFound, "no code editor found (laman looks for VS Code, Cursor, Windsurf, Zed, PhpStorm and Sublime Text)")
				return
			}
			// Outside laman's job, so the editor stays open when laman stops.
			cmd := editor.Command(e, s.Path)
			if err := spawnOutsideJob(cmd); err != nil {
				fail(w, http.StatusInternalServerError, "open %s: %v", e.Name, err)
				return
			}
			go cmd.Wait()
		default:
			fail(w, http.StatusBadRequest, "in must be folder or editor")
			return
		}
		writeJSON(w, struct{ OK bool }{true})
	})

	mux.HandleFunc("GET /api/editors", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		choice := d.cfg.Editor
		d.mu.Unlock()
		found := foundEditors()
		if found == nil {
			found = []editor.Editor{}
		}
		writeJSON(w, struct {
			Found  []editor.Editor `json:"found"`
			Choice string          `json:"choice"`
		}{found, choice})
	})

	mux.HandleFunc("POST /api/editor", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ ID string }
		if err := readJSON(r, &req); err != nil || (req.ID != "" && !slices.Contains(editor.IDs(), req.ID)) {
			fail(w, http.StatusBadRequest, "unknown editor")
			return
		}
		if err := saveEditor(req.ID); err != nil {
			fail(w, http.StatusInternalServerError, "%v", err)
			return
		}
		if err := d.refresh(); err != nil {
			fail(w, http.StatusInternalServerError, "%v", err)
			return
		}
		writeJSON(w, struct{ OK bool }{true})
	})
}

func saveEditor(id string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.Editor = id
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}
