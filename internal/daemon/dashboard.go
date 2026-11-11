package daemon

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"laman/internal/config"
	"laman/internal/desktop"
	"laman/internal/install"
)

//go:embed ui/index.html ui/app.js ui/theme.js ui/favicon.svg
var ui embed.FS

func isDashboardAsset(path string) bool {
	return path == "/" || path == "/app.js" || path == "/theme.js" || path == "/favicon.svg"
}

// job tracks the one PHP install the dashboard may run at a time.
type job struct {
	mu      sync.Mutex
	running bool
	message string
	err     string
}

type jobState struct {
	Running bool   `json:"running"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

// clear forgets the last finished job's message once the user does
// something else, so it does not describe a state that has moved on.
func (j *job) clear() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.running {
		j.message, j.err = "", ""
	}
}

func (j *job) state() jobState {
	j.mu.Lock()
	defer j.mu.Unlock()
	return jobState{j.running, j.message, j.err}
}

// lines caches the list of PHP lines windows.php.net offers.
type lines struct {
	mu      sync.Mutex
	list    []string
	fetched time.Time
}

func (l *lines) get() ([]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.list != nil && time.Since(l.fetched) < time.Hour {
		return l.list, nil
	}
	list, err := install.PHPLines()
	if err != nil {
		return l.list, err
	}
	l.list, l.fetched = list, time.Now()
	return list, nil
}

func (d *daemon) dashboardRoutes(mux *http.ServeMux) {
	serve := func(name, ctype string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			b, err := ui.ReadFile(name)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", ctype)
			w.Write(b)
		}
	}
	mux.HandleFunc("GET /{$}", serve("ui/index.html", "text/html; charset=utf-8"))
	mux.HandleFunc("GET /app.js", serve("ui/app.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /theme.js", serve("ui/theme.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /favicon.svg", serve("ui/favicon.svg", "image/svg+xml"))
	mux.HandleFunc("GET /api/php", d.apiPHP)
	mux.HandleFunc("POST /api/php/default", d.apiPHPDefault)
	mux.HandleFunc("POST /api/php/install", d.apiPHPInstall)
	mux.HandleFunc("POST /api/sites/php", d.apiSitePHP)
	mux.HandleFunc("GET /api/logs", d.apiLogs)
	d.versionRoutes(mux)
	// Opens the sites folder in Explorer on this machine (a web page cannot).
	mux.HandleFunc("POST /api/sites/open-folder", func(w http.ResponseWriter, r *http.Request) {
		if err := desktop.OpenFolder(d.cfg.SitesDir); err != nil {
			fail(w, http.StatusInternalServerError, "%v", err)
			return
		}
		writeJSON(w, struct{ OK bool }{true})
	})
	d.extRoutes(mux)
	// Writes the hosts file: `laman hosts` raises the Windows admin prompt
	// (UAC) itself, only ever because the user clicked for it.
	mux.HandleFunc("POST /api/hosts/sync", func(w http.ResponseWriter, r *http.Request) {
		if err := d.runJob("Updating the hosts file", []string{"hosts"}, func() { d.refresh() }); err != nil {
			fail(w, http.StatusConflict, "%v", err)
			return
		}
		writeJSON(w, d.installJob.state())
	})
	d.appRoutes(mux)
	d.shareRoutes(mux)
	d.queueRoutes(mux)
	d.shareSetupRoutes(mux)
	d.folderRoutes(mux)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
}

func fail(w http.ResponseWriter, code int, format string, a ...any) {
	http.Error(w, fmt.Sprintf(format, a...), code)
}

func (d *daemon) apiPHP(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Load()
	if err != nil {
		fail(w, http.StatusInternalServerError, "%v", err)
		return
	}
	usage := map[string][]string{}
	d.mu.Lock()
	for _, s := range d.sites {
		usage[s.PHP] = append(usage[s.PHP], s.Name)
	}
	d.mu.Unlock()
	resp := struct {
		Default   string              `json:"default"`
		Installed []string            `json:"installed"`
		Available []string            `json:"available"`
		Usage     map[string][]string `json:"usage"`
		Job       jobState            `json:"job"`
		Error     string              `json:"error,omitempty"`
	}{Default: cfg.DefaultPHP(), Installed: config.Installed("php"), Usage: usage, Job: d.installJob.state()}
	if resp.Available, err = d.phpLines.get(); err != nil {
		resp.Error = "could not reach windows.php.net: " + err.Error()
	}
	writeJSON(w, resp)
}

func (d *daemon) apiPHPDefault(w http.ResponseWriter, r *http.Request) {
	var req struct{ Version string }
	if err := readJSON(r, &req); err != nil {
		fail(w, http.StatusBadRequest, "bad request: %v", err)
		return
	}
	if config.Match("php", req.Version) != req.Version || req.Version == "" {
		fail(w, http.StatusBadRequest, "PHP %q is not installed", req.Version)
		return
	}
	d.updateConfig(w, func(c *config.Config) error { c.PHP = req.Version; return nil })
}

func (d *daemon) apiSitePHP(w http.ResponseWriter, r *http.Request) {
	var req struct{ Site, Version string }
	if err := readJSON(r, &req); err != nil {
		fail(w, http.StatusBadRequest, "bad request: %v", err)
		return
	}
	if req.Version != "" && config.Match("php", req.Version) != req.Version {
		fail(w, http.StatusBadRequest, "PHP %q is not installed", req.Version)
		return
	}
	d.updateConfig(w, func(c *config.Config) error {
		known := false
		d.mu.Lock()
		for _, s := range d.sites {
			known = known || s.Name == req.Site
		}
		d.mu.Unlock()
		if !known {
			return fmt.Errorf("no site named %q", req.Site)
		}
		if req.Version == "" {
			delete(c.Isolated, req.Site)
		} else {
			c.Isolated[req.Site] = req.Version
		}
		return nil
	})
}

// updateConfig applies a change to config.json and refreshes, so the
// dashboard and the CLI share one source of truth.
func (d *daemon) updateConfig(w http.ResponseWriter, change func(*config.Config) error) {
	cfg, err := config.Load()
	if err == nil {
		err = change(cfg)
	}
	if err == nil {
		err = cfg.Save()
	}
	if err != nil {
		fail(w, http.StatusBadRequest, "%v", err)
		return
	}
	if err := d.refresh(); err != nil {
		fail(w, http.StatusInternalServerError, "%v", err)
		return
	}
	writeJSON(w, d.status())
}

func (d *daemon) apiPHPInstall(w http.ResponseWriter, r *http.Request) {
	var req struct{ Line string }
	if err := readJSON(r, &req); err != nil || req.Line == "" || !safeArg(req.Line) {
		fail(w, http.StatusBadRequest, "bad request")
		return
	}
	if err := d.runJob("Installing PHP "+req.Line, []string{"install", "php", req.Line}, nil); err != nil {
		fail(w, http.StatusConflict, "%v", err)
		return
	}
	writeJSON(w, d.installJob.state())
}

var logNames = map[string]string{"php": "php.log", "caddy": "caddy.log", "mariadb": "mariadb.log", "mailpit": "mailpit.log", "redis": "redis-server.log", "postgres": "postgres.log", "upgrade": "upgrade.log", "laman": "laman.log"}

func (d *daemon) apiLogs(w http.ResponseWriter, r *http.Request) {
	file, ok := logNames[r.URL.Query().Get("name")]
	if !ok {
		fail(w, http.StatusBadRequest, "unknown log")
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	if n <= 0 || n > 2000 {
		n = 300
	}
	text, err := tail(filepath.Join(config.LogDir(), file), n)
	if errors.Is(err, os.ErrNotExist) {
		text, err = "(no log yet)", nil
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "%v", err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, tidyLog(text))
}

// tail returns the last n lines of a file, reading at most its last 512 KiB.
func tail(path string, n int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	const max = 512 << 10
	start := fi.Size() - max
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return "", err
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n"), "\n")
	if start > 0 && len(lines) > 0 {
		lines = lines[1:] // first line is probably cut in half
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n"), nil
}

func userHome() string { h, _ := os.UserHomeDir(); return h }

// tidyLog shortens the user's profile path to ~ so log lines fit on screen.
func tidyLog(text string) string {
	h := userHome()
	if h == "" {
		return text
	}
	return strings.NewReplacer(h, "~", filepath.ToSlash(h), "~").Replace(text)
}
