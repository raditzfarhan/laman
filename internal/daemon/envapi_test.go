package daemon

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"laman/internal/sites"
)

func envSite(t *testing.T, env string) (*daemon, *http.ServeMux, string) {
	t.Helper()
	t.Setenv("LAMAN_HOME", t.TempDir())
	dir := t.TempDir()
	if env != "" {
		writeFile(t, dir, ".env", env)
	}
	d, mux := detailFixture(t, sites.Site{Name: "acme", Host: "acme.test", Path: dir, Kind: sites.KindPHP})
	return d, mux, dir
}

func envText(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEnvRoutesRefuseApps(t *testing.T) {
	_, mux := detailFixture(t, sites.Site{Name: "db", Host: "db.test", App: "dbzuper", Path: t.TempDir()})
	if rec := call(mux, "GET", "/api/env?site=db.test", ""); rec.Code != 404 {
		t.Errorf("GET: %d", rec.Code)
	}
	if rec := call(mux, "POST", "/api/env/save", `{"site":"db.test","raw":"A=1"}`); rec.Code != 404 {
		t.Errorf("POST: %d", rec.Code)
	}
}

func TestEnvViewEndpoint(t *testing.T) {
	_, mux, _ := envSite(t, "APP_KEY=base64:z\nA=1\n")
	rec := call(mux, "GET", "/api/env?site=acme.test", "")
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "base64:z") {
		t.Error("secret leaked")
	}
	var got struct {
		Exists  bool
		Rows    []envRow
		Missing []envMissing
	}
	json.Unmarshal(rec.Body.Bytes(), &got)
	if !got.Exists || len(got.Rows) != 2 || !got.Rows[0].Masked || got.Rows[0].Value != "" {
		t.Errorf("%+v", got)
	}
	if !strings.Contains(rec.Body.String(), `"missing":[]`) {
		t.Errorf("missing not []: %s", rec.Body)
	}
}

func TestEnvValueLastWins(t *testing.T) {
	_, mux, _ := envSite(t, "DB_PASSWORD=a\nDB_PASSWORD=b\n")
	rec := call(mux, "GET", "/api/env/value?site=acme.test&key=DB_PASSWORD", "")
	if !strings.Contains(rec.Body.String(), `"value":"b"`) {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	if rec := call(mux, "GET", "/api/env/value?site=acme.test&key=NOPE", ""); rec.Code != 404 {
		t.Errorf("absent: %d", rec.Code)
	}
}

func TestEnvSaveConflict409(t *testing.T) {
	_, mux, _ := envSite(t, "A=1\n")
	rec := call(mux, "POST", "/api/env/save", `{"site":"acme.test","fingerprint":"bad","raw":"A=2\n"}`)
	if rec.Code != 409 {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}

func TestEnvSaveChangesAndRaw(t *testing.T) {
	_, mux, dir := envSite(t, "A=1\n")
	fp := envFingerprint([]byte("A=1\n"))
	rec := call(mux, "POST", "/api/env/save", `{"site":"acme.test","fingerprint":"`+fp+`","changes":[{"op":"set","key":"A","value":"2"}]}`)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body)
	}
	var r envSaveReply
	json.Unmarshal(rec.Body.Bytes(), &r)
	if envText(t, dir) != "A=2\n" || !r.Changed || r.Workers == nil {
		t.Errorf("%q %+v", envText(t, dir), r)
	}
	rec = call(mux, "POST", "/api/env/save", `{"site":"acme.test","fingerprint":"`+r.Fingerprint+`","raw":"Z=9\n"}`)
	if rec.Code != 200 || envText(t, dir) != "Z=9\n" {
		t.Errorf("%d %q", rec.Code, envText(t, dir))
	}
	rec = call(mux, "POST", "/api/env/save", `{"site":"acme.test","raw":"Y=1\n","changes":[{"op":"delete","key":"Z"}]}`)
	if rec.Code != 400 {
		t.Errorf("both: %d", rec.Code)
	}
	rec = call(mux, "POST", "/api/env/save", `{"site":"acme.test"}`)
	if rec.Code != 400 {
		t.Errorf("neither: %d", rec.Code)
	}
}

func TestEnvRestore(t *testing.T) {
	_, mux, dir := envSite(t, "A=1\n")
	rec := call(mux, "POST", "/api/env/save", `{"site":"acme.test","raw":"A=2\n"}`)
	var r envSaveReply
	json.Unmarshal(rec.Body.Bytes(), &r)
	if r.Backup == "" {
		t.Fatal(rec.Body)
	}
	rec = call(mux, "POST", "/api/env/restore", `{"site":"acme.test","fingerprint":"`+r.Fingerprint+`","backup":"`+r.Backup+`"}`)
	if rec.Code != 200 || envText(t, dir) != "A=1\n" {
		t.Fatalf("%d %s %q", rec.Code, rec.Body, envText(t, dir))
	}
	var r2 envSaveReply
	json.Unmarshal(rec.Body.Bytes(), &r2)
	if r2.Backup == "" || r2.Backup == r.Backup {
		t.Errorf("no new backup: %+v", r2)
	}
	if rec := call(mux, "POST", "/api/env/restore", `{"site":"acme.test","backup":"../x"}`); rec.Code != 400 {
		t.Errorf("bad id: %d", rec.Code)
	}
	rec = call(mux, "GET", "/api/env/backups?site=acme.test", "")
	var bs []envBackup
	json.Unmarshal(rec.Body.Bytes(), &bs)
	if len(bs) != 2 {
		t.Errorf("backups %d", len(bs))
	}
}

func TestEnvCreateAndAppKey(t *testing.T) {
	_, mux, dir := envSite(t, "")
	writeFile(t, dir, ".env.example", "APP_KEY=\r\nA=1\r\n")
	writeFile(t, dir, "artisan", "<?php")
	rec := call(mux, "POST", "/api/env/create", `{"site":"acme.test"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"needsKey":true`) {
		t.Fatal(rec.Code, rec.Body)
	}
	if envText(t, dir) != "APP_KEY=\r\nA=1\r\n" {
		t.Error("copy not exact")
	}
	if rec := call(mux, "POST", "/api/env/create", `{"site":"acme.test"}`); rec.Code != 409 {
		t.Errorf("create twice: %d", rec.Code)
	}
	rec = call(mux, "POST", "/api/env/appkey", `{"site":"acme.test"}`)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body)
	}
	if !regexp.MustCompile(`(?m)^APP_KEY="?base64:[A-Za-z0-9+/]{43}="?\r?$`).MatchString(envText(t, dir)) {
		t.Errorf("%q", envText(t, dir))
	}
	if rec := call(mux, "POST", "/api/env/appkey", `{"site":"acme.test"}`); rec.Code != 400 {
		t.Errorf("second: %d", rec.Code)
	}
}

func TestEnvConfigClearNoArtisan(t *testing.T) {
	_, mux, _ := envSite(t, "A=1\n")
	if rec := call(mux, "POST", "/api/env/config-clear", `{"site":"acme.test"}`); rec.Code != 400 {
		t.Errorf("%d", rec.Code)
	}
}

func TestEnvTooBig(t *testing.T) {
	_, mux, _ := envSite(t, "A="+strings.Repeat("x", 300<<10)+"\n")
	rec := call(mux, "GET", "/api/env?site=acme.test", "")
	if !strings.Contains(rec.Body.String(), `"tooBig":true`) || !strings.Contains(rec.Body.String(), `"rows":[]`) {
		t.Errorf("%.200s", rec.Body)
	}
	if rec := call(mux, "GET", "/api/env/value?site=acme.test&key=A", ""); rec.Code != 400 && rec.Code != 404 {
		t.Errorf("value: %d", rec.Code)
	}
}
