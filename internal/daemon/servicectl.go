package daemon

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"laman/internal/config"
)

// Stopping a server from the dashboard lasts until it is started again or
// laman restarts; "Starts with laman" (setEnabled) is the lasting switch.
// PHP and the web server only restart: every site needs them.

func (d *daemon) serviceControlRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/services/control", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Kind, Action, Version string }
		if err := readJSON(r, &req); err != nil {
			fail(w, http.StatusBadRequest, "bad request: %v", err)
			return
		}
		d.installJob.clear()
		if err := d.controlService(req.Kind, req.Action, req.Version); err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		writeJSON(w, d.status())
	})
}

func (d *daemon) controlService(kind, action, version string) error {
	switch kind {
	case "caddy":
		if action != "restart" {
			return fmt.Errorf("the web server can only be restarted; Stop in the sidebar stops all of laman")
		}
		logf("reloading the web server")
		return d.refresh()
	case "php":
		if action != "restart" {
			return fmt.Errorf("PHP can only be restarted: every site needs it")
		}
		return d.restartPHPLine(version)
	}
	if !slices.Contains(serviceOrder, kind) {
		return fmt.Errorf("unknown service %q", kind)
	}
	if !installed(kind) {
		return fmt.Errorf("%s is not installed", serviceName(kind))
	}
	d.mu.Lock()
	enabled := d.cfg.Enabled(kind)
	running := d.handles[kind] != nil
	d.mu.Unlock()
	if !enabled {
		return fmt.Errorf("%s is turned off; switch on \"Starts with laman\" first", serviceName(kind))
	}
	switch action {
	case "stop":
		if running {
			logf("stopping %s (until started again)", kind)
			d.stopService(kind)
		}
		return nil
	case "start":
		if running {
			return nil
		}
		return d.startAddon(kind)
	case "restart":
		logf("restarting %s", kind)
		d.stopService(kind)
		return d.startAddon(kind)
	}
	return fmt.Errorf("action must be start, stop or restart")
}

// startAddon starts an enabled add-on server the way turning it on does.
func (d *daemon) startAddon(kind string) error {
	logf("starting %s", kind)
	if kind == "mailpit" {
		return d.startMailpit()
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	_, err = d.switchServer(kind, config.Active(kind, *pinnedLine(cfg, kind)))
	return err
}

// restartPHPLine restarts the workers of a PHP line ("8.5").
func (d *daemon) restartPHPLine(line string) error {
	d.mu.Lock()
	var version string
	for v := range d.pools {
		if v == line || strings.HasPrefix(v, line+".") {
			version = v
		}
	}
	d.mu.Unlock()
	if version == "" {
		return fmt.Errorf("no PHP %s workers are running", line)
	}
	return d.restartPHP(version)
}
