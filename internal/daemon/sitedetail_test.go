package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

func TestDetailSizesRefresh(t *testing.T) {
	_, mux := detailFixture(t, sites.Site{Name: "acme", Host: "acme.test", Path: t.TempDir(), Kind: sites.KindPHP})
	if rec := call(mux, "POST", "/api/sites/sizes", `{"site":"acme.test"}`); rec.Code != http.StatusOK {
		t.Errorf("refresh: status %d", rec.Code)
	}
	if rec := call(mux, "POST", "/api/sites/sizes", `{"site":"gone.test"}`); rec.Code != http.StatusNotFound {
		t.Errorf("refresh gone: status %d, want 404", rec.Code)
	}
}
