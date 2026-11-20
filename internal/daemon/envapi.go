package daemon

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"laman/internal/config"
	"laman/internal/sites"
)

// The dashboard's .env API. Secret values leave only through /api/env/value
// (one key) and /api/env/raw; Laman apps get 404 from every route.

type envSaveReply struct {
	envSaved
	ConfigCached bool     `json:"configCached"`
	Workers      []string `json:"workers"` // active worker kinds that need a restart
	DevRunning   bool     `json:"devRunning"`
}

// envEditError marks a failure of the edit itself (bad input), not of I/O.
type envEditError struct{ error }

func (e envEditError) Unwrap() error { return e.error }

func envConfigCached(s sites.Site) bool {
	return exists(filepath.Join(s.Path, "bootstrap", "cache", "config.php"))
}

// envSite resolves the request's site, answering 404 itself.
func (d *daemon) envSite(w http.ResponseWriter, host string) (sites.Site, bool) {
	s, ok := d.siteByHost(host)
	if !ok {
		fail(w, http.StatusNotFound, "no site %s", host)
	}
	return s, ok
}

// envReadable stats .env; it answers 400 itself when .env is absent or too big.
func envReadable(w http.ResponseWriter, s sites.Site) bool {
	fi, err := os.Stat(filepath.Join(s.Path, ".env"))
	switch {
	case errors.Is(err, os.ErrNotExist):
		fail(w, http.StatusBadRequest, "no .env")
	case err != nil:
		fail(w, http.StatusInternalServerError, "%v", err)
	case fi.Size() > envMaxSize:
		fail(w, http.StatusBadRequest, ".env is larger than %d KB", envMaxSize>>10)
	default:
		return true
	}
	return false
}

func (d *daemon) envReply(s sites.Site, saved envSaved) envSaveReply {
	r := envSaveReply{envSaved: saved, ConfigCached: envConfigCached(s), Workers: []string{}}
	for _, kind := range []string{workerQueue, workerReverb} {
		if wk := d.worker(kind, s.Host); wk != nil {
			wk.mu.Lock()
			act := wk.active()
			wk.mu.Unlock()
			if act {
				r.Workers = append(r.Workers, kind)
			}
		}
	}
	r.DevRunning = d.devState(s.Host).State == devRunning
	return r
}

func envFail(w http.ResponseWriter, err error) {
	var ee envEditError
	switch {
	case errors.Is(err, errEnvChanged):
		fail(w, http.StatusConflict, "%v", err)
	case errors.As(err, &ee):
		fail(w, http.StatusBadRequest, "%v", err)
	default:
		fail(w, http.StatusInternalServerError, "%v", err)
	}
}

// readEnvIfSmall reads a site's .env, unless it is absent or too big.
func readEnvIfSmall(s sites.Site) (text string, has, tooBig bool, err error) {
	path := filepath.Join(s.Path, ".env")
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, false, nil
	}
	if err != nil {
		return "", false, false, err
	}
	if fi.Size() > envMaxSize {
		return "", true, true, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, false, nil
	}
	return string(b), true, false, err
}

func (d *daemon) envRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/env", func(w http.ResponseWriter, r *http.Request) {
		s, ok := d.envSite(w, r.URL.Query().Get("site"))
		if !ok {
			return
		}
		text, has, big, err := readEnvIfSmall(s)
		if err != nil {
			fail(w, http.StatusInternalServerError, "%v", err)
			return
		}
		out := struct {
			Exists        bool         `json:"exists"`
			ExampleExists bool         `json:"exampleExists"`
			TooBig        bool         `json:"tooBig"`
			Fingerprint   string       `json:"fingerprint"`
			Rows          []envRow     `json:"rows"`
			Missing       []envMissing `json:"missing"`
			ConfigCached  bool         `json:"configCached"`
		}{Exists: has, TooBig: big, Rows: []envRow{}, Missing: []envMissing{}, ConfigCached: envConfigCached(s)}
		example := filepath.Join(s.Path, ".env.example")
		exFi, exErr := os.Stat(example)
		out.ExampleExists = exErr == nil && exFi.Mode().IsRegular()
		if has && !big {
			ex := ""
			if out.ExampleExists && exFi.Size() <= envMaxSize {
				b, _ := os.ReadFile(example)
				ex = string(b)
			}
			out.Fingerprint = envFingerprint([]byte(text))
			rows, missing := buildEnvView(text, ex)
			if rows != nil {
				out.Rows = rows
			}
			if missing != nil {
				out.Missing = missing
			}
		}
		writeJSON(w, out)
	})

	mux.HandleFunc("GET /api/env/value", func(w http.ResponseWriter, r *http.Request) {
		s, ok := d.envSite(w, r.URL.Query().Get("site"))
		if !ok || !envReadable(w, s) {
			return
		}
		text, _, _, err := readEnvIfSmall(s)
		if err != nil {
			fail(w, http.StatusInternalServerError, "%v", err)
			return
		}
		key := r.URL.Query().Get("key")
		found, val := false, ""
		for _, l := range parseEnv(text) {
			if l.Key == key {
				found, val = true, l.Value
			}
		}
		if !found {
			fail(w, http.StatusNotFound, "%s is not set", key)
			return
		}
		writeJSON(w, map[string]string{"value": val})
	})

	mux.HandleFunc("GET /api/env/raw", func(w http.ResponseWriter, r *http.Request) {
		s, ok := d.envSite(w, r.URL.Query().Get("site"))
		if !ok || !envReadable(w, s) {
			return
		}
		text, _, _, err := readEnvIfSmall(s)
		if err != nil {
			fail(w, http.StatusInternalServerError, "%v", err)
			return
		}
		writeJSON(w, map[string]string{"text": text, "fingerprint": envFingerprint([]byte(text))})
	})

	mux.HandleFunc("GET /api/env/backups", func(w http.ResponseWriter, r *http.Request) {
		s, ok := d.envSite(w, r.URL.Query().Get("site"))
		if !ok {
			return
		}
		list, err := listEnvBackups(s.Name)
		if err != nil {
			fail(w, http.StatusInternalServerError, "%v", err)
			return
		}
		writeJSON(w, list)
	})

	mux.HandleFunc("POST /api/env/save", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Site        string      `json:"site"`
			Fingerprint string      `json:"fingerprint"`
			Changes     []envChange `json:"changes"`
			Raw         *string     `json:"raw"`
		}
		if err := readJSON(r, &req); err != nil {
			fail(w, http.StatusBadRequest, "bad request: %v", err)
			return
		}
		s, ok := d.envSite(w, req.Site)
		if !ok {
			return
		}
		if (req.Raw == nil) == (req.Changes == nil) {
			fail(w, http.StatusBadRequest, "send either changes or raw")
			return
		}
		if !envReadable(w, s) {
			return
		}
		saved, err := d.writeEnv(s, req.Fingerprint, func(orig string) (string, error) {
			if req.Raw != nil {
				if len(*req.Raw) > envMaxSize {
					return "", envEditError{errors.New(".env text is too large")}
				}
				return *req.Raw, nil
			}
			out, err := applyEnvChanges(orig, req.Changes)
			if err != nil {
				return "", envEditError{err}
			}
			return out, nil
		})
		if err != nil {
			envFail(w, err)
			return
		}
		writeJSON(w, d.envReply(s, saved))
	})

	mux.HandleFunc("POST /api/env/restore", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Site, Fingerprint, Backup string }
		if err := readJSON(r, &req); err != nil {
			fail(w, http.StatusBadRequest, "bad request: %v", err)
			return
		}
		s, ok := d.envSite(w, req.Site)
		if !ok {
			return
		}
		data, err := readEnvBackup(s.Name, req.Backup)
		if err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		if !envReadable(w, s) {
			return
		}
		saved, err := d.writeEnv(s, req.Fingerprint, func(string) (string, error) { return string(data), nil })
		if err != nil {
			envFail(w, err)
			return
		}
		writeJSON(w, d.envReply(s, saved))
	})

	mux.HandleFunc("POST /api/env/create", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Site string }
		if err := readJSON(r, &req); err != nil {
			fail(w, http.StatusBadRequest, "bad request: %v", err)
			return
		}
		s, ok := d.envSite(w, req.Site)
		if !ok {
			return
		}
		l := d.envLock(s.Name)
		l.Lock()
		defer l.Unlock()
		path := filepath.Join(s.Path, ".env")
		if exists(path) {
			fail(w, http.StatusConflict, ".env already exists")
			return
		}
		ex, err := os.ReadFile(filepath.Join(s.Path, ".env.example"))
		if errors.Is(err, os.ErrNotExist) {
			fail(w, http.StatusBadRequest, "no .env.example to copy")
			return
		}
		if err != nil {
			fail(w, http.StatusInternalServerError, "%v", err)
			return
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			fail(w, http.StatusConflict, ".env already exists")
			return
		}
		if err != nil {
			fail(w, http.StatusInternalServerError, "%v", err)
			return
		}
		_, werr := f.Write(ex)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			os.Remove(path)
			fail(w, http.StatusInternalServerError, "%v", werr)
			return
		}
		needsKey := false
		if exists(filepath.Join(s.Path, "artisan")) {
			for _, ln := range parseEnv(string(ex)) {
				if ln.Key == "APP_KEY" {
					needsKey = ln.Value == "" // the last assignment is the live one
				}
			}
		}
		writeJSON(w, map[string]any{"fingerprint": envFingerprint(ex), "needsKey": needsKey})
	})

	mux.HandleFunc("POST /api/env/appkey", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Site, Fingerprint string }
		if err := readJSON(r, &req); err != nil {
			fail(w, http.StatusBadRequest, "bad request: %v", err)
			return
		}
		s, ok := d.envSite(w, req.Site)
		if !ok || !envReadable(w, s) {
			return
		}
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			fail(w, http.StatusInternalServerError, "%v", err)
			return
		}
		val := "base64:" + base64.StdEncoding.EncodeToString(key)
		saved, err := d.writeEnv(s, req.Fingerprint, func(orig string) (string, error) {
			for _, l := range parseEnv(orig) {
				if l.Key == "APP_KEY" && l.Value != "" {
					return "", envEditError{errors.New("APP_KEY is already set")}
				}
			}
			out, err := applyEnvChanges(orig, []envChange{{Op: "set", Key: "APP_KEY", Value: val}})
			if err != nil {
				return "", envEditError{err}
			}
			return out, nil
		})
		if err != nil {
			envFail(w, err)
			return
		}
		writeJSON(w, d.envReply(s, saved))
	})

	mux.HandleFunc("POST /api/env/config-clear", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Site string }
		if err := readJSON(r, &req); err != nil {
			fail(w, http.StatusBadRequest, "bad request: %v", err)
			return
		}
		s, ok := d.envSite(w, req.Site)
		if !ok {
			return
		}
		if !exists(filepath.Join(s.Path, "artisan")) {
			fail(w, http.StatusBadRequest, "this project has no artisan file")
			return
		}
		php := s.PHP
		if php == "" {
			php = d.cfg.DefaultPHP()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, filepath.Join(config.BinDir("php"), php, "php.exe"),
			"artisan", "config:clear", "--no-interaction", "--no-ansi")
		cmd.Dir = s.Path
		noWindow(cmd)
		out, err := cmd.CombinedOutput()
		text := strings.TrimSpace(string(out))
		if err != nil {
			fail(w, http.StatusInternalServerError, "php artisan config:clear failed: %s", firstNonEmpty(text, err.Error()))
			return
		}
		writeJSON(w, map[string]any{"ok": true, "output": text})
	})

	mux.HandleFunc("POST /api/env/restart-workers", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Site string }
		if err := readJSON(r, &req); err != nil {
			fail(w, http.StatusBadRequest, "bad request: %v", err)
			return
		}
		s, ok := d.envSite(w, req.Site)
		if !ok {
			return
		}
		if err := d.restartSiteWorkers(s.Name); err != nil {
			fail(w, http.StatusInternalServerError, "%v", err)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
}
