package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"laman/internal/config"
	"laman/internal/update"
	"laman/internal/version"
)

// laman's own updates: the daemon looks for a newer release on GitHub soon
// after it starts and then once a day, and the dashboard offers it. The
// install itself is `laman update`, run outside the daemon because it
// stops and restarts laman.

type UpdateInfo struct {
	Current   string `json:"current"`             // this build's version ("dev" for local builds)
	Available string `json:"available,omitempty"` // a newer release, if any
	URL       string `json:"url,omitempty"`       // its release notes
	Checked   int64  `json:"checked,omitempty"`   // unix time of the last successful check
	Error     string `json:"error,omitempty"`     // why the last check failed
	Running   bool   `json:"running,omitempty"`   // `laman update` was started from the dashboard
}

type updater struct {
	mu      sync.Mutex
	info    UpdateInfo
	started time.Time // when the dashboard started `laman update`
}

func (u *updater) get() UpdateInfo {
	u.mu.Lock()
	defer u.mu.Unlock()
	i := u.info
	i.Current = version.Version
	// A successful update restarts the daemon; one still here after a few
	// minutes failed (logs/update.log says why), so offer it again.
	i.Running = !u.started.IsZero() && time.Since(u.started) < 5*time.Minute
	return i
}

func (d *daemon) checkUpdate(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r, err := update.Latest(ctx)
	u := &d.updates
	u.mu.Lock()
	defer u.mu.Unlock()
	switch {
	case errors.Is(err, update.ErrNoReleases):
		u.info.Error, u.info.Available, u.info.URL = "", "", ""
		u.info.Checked = time.Now().Unix()
	case err != nil:
		u.info.Error = err.Error()
	default:
		u.info.Error, u.info.Checked = "", time.Now().Unix()
		if update.Newer(r.Version, version.Version) {
			u.info.Available, u.info.URL = r.Version, r.URL
		} else {
			u.info.Available, u.info.URL = "", ""
		}
	}
}

// watchUpdates checks a minute after start, then every 24 hours. Local
// builds and configs with noUpdateCheck never check.
func (d *daemon) watchUpdates(ctx context.Context) {
	if version.Dev() {
		return
	}
	wait := time.Minute
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if cfg, err := config.Load(); err == nil && !cfg.NoUpdateCheck {
			d.checkUpdate(ctx)
		}
		wait = 24 * time.Hour
	}
}

// startSelfUpdate runs `laman update` detached and outside the daemon's
// job: it stops this daemon, swaps the files and starts laman again.
func (d *daemon) startSelfUpdate() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logPath := filepath.Join(config.LogDir(), "upgrade.log") // shown in Logs > Upgrade
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Fprintf(f, "\n[laman %s] update from %s (from the dashboard)\n", time.Now().Format(time.RFC3339), version.Version)
	cmd := exec.Command(exe, "update")
	cmd.Stdout, cmd.Stderr = f, f
	if err := spawnOutsideJob(cmd); err != nil {
		return fmt.Errorf("could not start the update outside laman (%v); run `laman update` in a terminal instead", err)
	}
	logf("dashboard started: laman update (pid %d)", cmd.Process.Pid)
	d.updates.mu.Lock()
	d.updates.started = time.Now()
	d.updates.mu.Unlock()
	return cmd.Process.Release()
}

func (d *daemon) updateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/update", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, d.updates.get())
	})
	// Check now (the dashboard's "Check for updates").
	mux.HandleFunc("POST /api/update/check", func(w http.ResponseWriter, r *http.Request) {
		if version.Dev() {
			fail(w, http.StatusConflict, "this is a development build of laman; it doesn't update itself")
			return
		}
		d.checkUpdate(r.Context())
		writeJSON(w, d.updates.get())
	})
	mux.HandleFunc("POST /api/update", func(w http.ResponseWriter, r *http.Request) {
		if version.Dev() {
			fail(w, http.StatusConflict, "this is a development build of laman; it doesn't update itself")
			return
		}
		if d.updates.get().Available == "" {
			fail(w, http.StatusConflict, "no newer laman release is known; check first")
			return
		}
		if err := d.startSelfUpdate(); err != nil {
			fail(w, http.StatusInternalServerError, "%v", err)
			return
		}
		writeJSON(w, d.updates.get())
	})
}
