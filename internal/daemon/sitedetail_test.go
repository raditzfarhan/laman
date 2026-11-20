package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"laman/internal/config"
	"laman/internal/sites"
)

func detailFixture(t *testing.T, list ...sites.Site) (*daemon, *http.ServeMux) {
	t.Helper()
	d := &daemon{cfg: config.Default(), sites: list}
	old := detailConfig
	detailConfig = func(d *daemon) *config.Config { return d.cfg }
	t.Cleanup(func() { detailConfig = old })
	mux := http.NewServeMux()
	d.siteDetailRoutes(mux)
	return d, mux
}

func call(mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func TestDetailUnknownSite(t *testing.T) {
	_, mux := detailFixture(t, sites.Site{Name: "db", Host: "db.test", App: "dbzuper", Path: t.TempDir()})
	for _, host := range []string{"gone.test", "db.test"} { // missing, and an app site
		if rec := call(mux, "GET", "/api/sites/detail?site="+host, ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", host, rec.Code)
		}
	}
}

func TestDetailLaravelSite(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".env", "APP_KEY=x\nDB_CONNECTION=mysql\nDB_DATABASE=acme\nCACHE_STORE=redis\n")
	writeFile(t, dir, "composer.json", "{}")
	writeFile(t, dir, "package.json", "{}")
	_, mux := detailFixture(t, sites.Site{Name: "acme", Host: "acme.test", Path: dir, Kind: sites.KindPHP})

	rec := call(mux, "GET", "/api/sites/detail?site=acme.test", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got siteDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Facts.DB == nil || got.Facts.DB.Kind != "mariadb" || got.Facts.DB.Name != "acme" {
		t.Errorf("facts.db = %+v", got.Facts.DB)
	}
	want := []packageFact{{Kind: "composer"}, {Kind: "node"}}
	if len(got.Packages) != 2 || got.Packages[0] != want[0] || got.Packages[1] != want[1] {
		t.Errorf("packages = %+v, want %+v", got.Packages, want)
	}
	if got.Node == nil || got.Node.PM != "npm" {
		t.Errorf("node = %+v", got.Node)
	}
	if got.Size.State != "measuring" && got.Size.State != "done" {
		t.Errorf("size.state = %q", got.Size.State)
	}
	for _, k := range []string{"mariadb", "postgres", "redis", "mailpit"} {
		if _, ok := got.Services[k]; !ok {
			t.Errorf("services has no %s", k)
		}
	}
	// JSON is lower camelCase like the rest of the API.
	for _, key := range []string{`"facts"`, `"packages"`, `"present"`, `"pm"`, `"running"`, `"http"`} {
		if !strings.Contains(rec.Body.String(), key) {
			t.Errorf("reply has no %s key: %s", key, rec.Body)
		}
	}
}

func TestWorkersFactSuggests(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "artisan", "<?php\n")
	writeFile(t, dir, "composer.json", `{"require":{"laravel/reverb":"^1.0"}}`)
	writeFile(t, dir, ".env", "APP_KEY=x\nQUEUE_CONNECTION=redis\n")
	_, mux := detailFixture(t, sites.Site{Name: "acme", Host: "acme.test", Path: dir, Kind: sites.KindPHP})

	rec := call(mux, "GET", "/api/sites/detail?site=acme.test", "")
	var got siteDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Workers == nil {
		t.Fatalf("detail: %v %s", err, rec.Body)
	}
	w := got.Workers
	if !w.Laravel || !w.Queue.Suggest || w.Queue.On || w.Queue.Inline || w.Queue.Status.State != workerOff {
		t.Errorf("queue = %+v (laravel %v)", w.Queue, w.Laravel)
	}
	if !w.Reverb.Installed || !w.Reverb.Suggest || w.Reverb.On || !w.Reverb.NeedsInstall || w.Reverb.Port != 0 {
		t.Errorf("reverb = %+v", w.Reverb)
	}
	for _, key := range []string{`"workers"`, `"laravel"`, `"suggest"`, `"needsInstall"`, `"envOK"`} {
		if !strings.Contains(rec.Body.String(), key) {
			t.Errorf("reply has no %s key: %s", key, rec.Body)
		}
	}
}

func TestWorkersFactReverbOn(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "artisan", "<?php\n")
	writeFile(t, dir, "composer.json", `{"require":{"laravel/reverb":"^1.0"}}`)
	writeFile(t, dir, ".env", "APP_KEY=x\nQUEUE_CONNECTION=sync\nREVERB_APP_KEY=abc\n"+
		"BROADCAST_CONNECTION=reverb\nREVERB_HOST=127.0.0.1\nREVERB_PORT=8101\nREVERB_SCHEME=http\n"+
		"REVERB_SERVER_HOST=127.0.0.1\nREVERB_SERVER_PORT=8101\nVITE_REVERB_HOST=acme.test\nVITE_REVERB_PORT=8443\nVITE_REVERB_SCHEME=http\n")
	d, mux := detailFixture(t, sites.Site{Name: "acme", Host: "acme.test", Path: dir, Kind: sites.KindPHP})
	d.cfg.HTTPSPort = 8443
	d.certHosts = map[string]bool{"acme.test": true}
	d.cfg.Reverb = map[string]bool{"acme": true}
	d.cfg.ReverbPorts = map[string]int{"acme": 8101}

	var got siteDetail
	json.Unmarshal(call(mux, "GET", "/api/sites/detail?site=acme.test", "").Body.Bytes(), &got)
	q, r := got.Workers.Queue, got.Workers.Reverb
	if !q.Inline || q.Suggest || q.Note != "Jobs run inline (QUEUE_CONNECTION=sync)" {
		t.Errorf("queue = %+v", q)
	}
	if !r.On || r.Suggest || r.NeedsInstall || r.Port != 8101 || r.URL != "wss://acme.test:8443/app" {
		t.Errorf("reverb = %+v", r)
	}
	if r.EnvOK || len(r.EnvMismatch) != 1 || r.EnvMismatch[0] != "VITE_REVERB_SCHEME" {
		t.Errorf("env check: ok %v, mismatch %v", r.EnvOK, r.EnvMismatch)
	}
}

func TestDetailSizesRefresh(t *testing.T) {
	_, mux := detailFixture(t, sites.Site{Name: "acme", Host: "acme.test", Path: t.TempDir(), Kind: sites.KindPHP})
	if rec := call(mux, "POST", "/api/sites/sizes", `{"site":"acme.test"}`); rec.Code != http.StatusOK {
		t.Errorf("refresh: status %d", rec.Code)
	}
	if rec := call(mux, "POST", "/api/sites/sizes", `{"site":"gone.test"}`); rec.Code != http.StatusNotFound {
		t.Errorf("refresh gone: status %d, want 404", rec.Code)
	}
}

func TestInstallBusy(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", "{}")
	d, mux := detailFixture(t, sites.Site{Name: "acme", Host: "acme.test", Path: dir, Kind: sites.KindPHP})
	d.installJob.running = true
	rec := call(mux, "POST", "/api/sites/install", `{"site":"acme.test","kind":"node"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "another install is still running") {
		t.Errorf("status %d %q, want 409 busy", rec.Code, rec.Body)
	}
}

func TestInstallBadKind(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", "{}")
	_, mux := detailFixture(t, sites.Site{Name: "acme", Host: "acme.test", Path: dir, Kind: sites.KindPHP})
	for body, want := range map[string]int{
		`{"site":"acme.test","kind":"pip"}`:      http.StatusBadRequest,
		`{"site":"acme.test","kind":"composer"}`: http.StatusBadRequest, // no composer.json
		`{"site":"gone.test","kind":"node"}`:     http.StatusNotFound,
	} {
		if rec := call(mux, "POST", "/api/sites/install", body); rec.Code != want {
			t.Errorf("%s: status %d, want %d", body, rec.Code, want)
		}
	}
}

// A package installed outside the dashboard (in a terminal) is measured
// again instead of showing the old 0 B.
func TestDetailRemeasuresWhenPackagesAppear(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", "{}")
	d, mux := detailFixture(t, sites.Site{Name: "acme", Host: "acme.test", Path: dir, Kind: sites.KindNode})
	d.sizes.get(dir)
	deadline := time.Now().Add(2 * time.Second)
	for d.sizes.get(dir).State != "done" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	writeFile(t, dir, "node_modules/x/index.js", "module.exports = 1")
	var got siteDetail
	json.Unmarshal(call(mux, "GET", "/api/sites/detail?site=acme.test", "").Body.Bytes(), &got)
	if got.Size.State != "measuring" {
		t.Errorf("size.state = %q after node_modules appeared, want measuring", got.Size.State)
	}
}

// A failed composer install ends with a generic hint; the job shows the
// line that says what went wrong.
func TestLastLineSkipsComposerHint(t *testing.T) {
	out := "Loading composer repositories\r\n  Problem 1\r\n    - Root composer.json requires acme/nope, it could not be found in any version.\r\n\r\nPotential causes:\r\n - A typo in the package name\r\n - It's a private package and you forgot to add a custom repository to find it\r\n\r\nRead <https://getcomposer.org/doc/articles/troubleshooting.md> for further common problems.\r\n"
	if got, want := lastLine(out), "- Root composer.json requires acme/nope, it could not be found in any version."; got != want {
		t.Errorf("lastLine = %q, want %q", got, want)
	}
}
