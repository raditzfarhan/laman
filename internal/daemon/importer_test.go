package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"laman/internal/config"
	"laman/internal/laragon"
)

// importFixture sets up a throwaway LAMAN_HOME and a fake Laragon folder,
// and stubs laman's database list and the job start.
type importFixture struct {
	root string
	mux  *http.ServeMux
	args []string
	env  []string
	ran  int
}

func newImportFixture(t *testing.T, lamanDBs []string, dbErr error) *importFixture {
	t.Helper()
	t.Setenv("LAMAN_HOME", t.TempDir())
	f := &importFixture{root: t.TempDir()}
	os.WriteFile(filepath.Join(f.root, "laragon.exe"), nil, 0o644)
	os.MkdirAll(filepath.Join(f.root, "usr"), 0o755)
	os.WriteFile(filepath.Join(f.root, "usr", "laragon.ini"), []byte("[mysql]\nVersion=mysql-8.4.0-winx64\n"), 0o644)
	os.MkdirAll(filepath.Join(f.root, "www"), 0o755)
	for _, db := range []string{"acme", "shop"} {
		os.MkdirAll(filepath.Join(f.root, "data", "mysql-8.4", db), 0o755)
	}

	oldDBs, oldRun, oldSrv := lamanDatabases, runImportJob, runningServers
	t.Cleanup(func() { lamanDatabases, runImportJob, runningServers = oldDBs, oldRun, oldSrv })
	lamanDatabases = func(*config.Config) ([]string, error) { return lamanDBs, dbErr }
	runImportJob = func(d *daemon, args, env []string, before func()) error {
		if before != nil {
			before()
		}
		f.args, f.env = args, env
		f.ran++
		return nil
	}
	f.mux = http.NewServeMux()
	(&daemon{cfg: config.Default()}).importRoutes(f.mux)
	return f
}

func (f *importFixture) do(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec
}

func testPlan() laragon.Plan {
	return laragon.Plan{DBs: []laragon.PlanDB{
		{Name: "acme"}, {Name: "shop"}, {Name: "blog", Exists: true},
	}}
}

func TestImportArgs(t *testing.T) {
	plan := testPlan()
	got, err := importArgs(`C:\laragon`, []string{"acme", "shop"}, plan)
	want := []string{"import", "laragon", "--from", `C:\laragon`, "--yes", "--db", "acme,shop"}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("importArgs = %q, %v", got, err)
	}
	got, err = importArgs(`C:\laragon`, nil, plan)
	want = []string{"import", "laragon", "--from", `C:\laragon`, "--yes", "--no-db"}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("importArgs empty = %q, %v", got, err)
	}
	for _, bad := range [][]string{{"nope"}, {"blog"}, {"acme", "x,y"}, {"--no-db"}} {
		if args, err := importArgs(`C:\laragon`, bad, plan); err == nil {
			t.Errorf("importArgs(%q) accepted: %q", bad, args)
		}
	}
	for _, from := range []string{"-x", "--yes", "", `\server\share`, `\?\C:\x`, `rel\dir`, "C:\\a\nb"} {
		if _, err := importArgs(from, nil, plan); err == nil {
			t.Errorf("importArgs from %q accepted", from)
		}
	}
}

func TestImportArgsFromRequest(t *testing.T) {
	f := newImportFixture(t, []string{"blog"}, nil)
	body := func(from string, dbs string, pw string) string {
		b, _ := json.Marshal(map[string]any{"from": from, "password": pw})
		return strings.Replace(string(b), "}", `,"dbs":`+dbs+"}", 1)
	}

	// A database that is not in the plan is refused, and nothing runs.
	if rec := f.do("POST", "/api/import/laragon", body(f.root, `["nope"]`, "")); rec.Code != 400 || f.ran != 0 {
		t.Errorf("unknown db: %d %s ran=%d", rec.Code, rec.Body, f.ran)
	}
	// One laman already has is refused too.
	if rec := f.do("POST", "/api/import/laragon", body(f.root, `["blog"]`, "")); rec.Code != 400 || f.ran != 0 {
		t.Errorf("existing db: %d ran=%d", rec.Code, f.ran)
	}
	// A folder that is not Laragon.
	if rec := f.do("POST", "/api/import/laragon", body(t.TempDir(), `[]`, "")); rec.Code != 400 || f.ran != 0 {
		t.Errorf("not laragon: %d ran=%d", rec.Code, f.ran)
	}

	// Empty dbs: --no-db, and the password goes in the environment only.
	rec := f.do("POST", "/api/import/laragon", body(f.root, `[]`, "s3cret"))
	if rec.Code != 200 || f.ran != 1 {
		t.Fatalf("empty dbs: %d %s ran=%d", rec.Code, rec.Body, f.ran)
	}
	if !slices.Contains(f.args, "--no-db") || !slices.Contains(f.args, "--yes") || slices.Contains(f.args, "--db") {
		t.Errorf("args = %q", f.args)
	}
	if strings.Contains(strings.Join(f.args, " "), "s3cret") {
		t.Errorf("password in args: %q", f.args)
	}
	if !slices.Equal(f.env, []string{"LAMAN_LARAGON_PASSWORD=s3cret"}) {
		t.Errorf("env = %q", f.env)
	}

	rec = f.do("POST", "/api/import/laragon", body(f.root, `["acme","shop"]`, ""))
	if rec.Code != 200 || f.args[len(f.args)-2] != "--db" || f.args[len(f.args)-1] != "acme,shop" {
		t.Errorf("two dbs: %d %s args=%q", rec.Code, rec.Body, f.args)
	}
}

func TestImportPlanEndpoint(t *testing.T) {
	f := newImportFixture(t, []string{"acme"}, nil)
	rec := f.do("GET", "/api/import/laragon?from="+f.root, "")
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var got struct {
		Root       string
		MariaDBOff bool `json:"mariadbOff"`
	}
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Root != f.root || got.MariaDBOff {
		t.Errorf("plan = %+v", got)
	}
	for _, k := range []string{`"root":`, `"blocked":`, `"folders":[{"dir":`, `"dbs":[{"name":"acme"`, `"mariadbOff":false`} {
		if !strings.Contains(rec.Body.String(), k) {
			t.Errorf("plan JSON lacks %s: %s", k, rec.Body)
		}
	}

	f = newImportFixture(t, nil, os.ErrDeadlineExceeded)
	rec = f.do("GET", "/api/import/laragon?from="+f.root, "")
	if !strings.Contains(rec.Body.String(), `"mariadbOff":true`) {
		t.Errorf("mariadb off not flagged: %s", rec.Body)
	}
	// With laman's MariaDB off, copying databases is refused.
	if rec := f.do("POST", "/api/import/laragon", `{"from":`+quote(f.root)+`,"dbs":["acme"]}`); rec.Code != 409 || f.ran != 0 {
		t.Errorf("dbs with mariadb off: %d ran=%d", rec.Code, f.ran)
	}
}

func quote(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestImportLogEndpoint(t *testing.T) {
	f := newImportFixture(t, nil, nil)
	var got struct {
		Lines  []string
		Result json.RawMessage
	}
	rec := f.do("GET", "/api/import/laragon/log", "")
	json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != 200 || string(got.Result) != "null" || len(got.Lines) != 0 || !strings.Contains(rec.Body.String(), `"lines":[]`) {
		t.Errorf("empty: %d %s", rec.Code, rec.Body)
	}

	os.MkdirAll(config.LogDir(), 0o755)
	var log strings.Builder
	for i := 1; i <= 60; i++ {
		log.WriteString("line " + string(rune('A'+i%26)) + "\r\n")
	}
	log.WriteString("last\n")
	os.WriteFile(filepath.Join(config.LogDir(), "import-laragon.log"), []byte(log.String()), 0o644)
	os.WriteFile(filepath.Join(config.LogDir(), "import-laragon.json"), []byte(`{"copied":["acme"],"needsPassword":false}`), 0o644)
	got.Lines, got.Result = nil, nil
	rec = f.do("GET", "/api/import/laragon/log", "")
	json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Lines) != 50 || got.Lines[49] != "last" || strings.Contains(got.Lines[0], "\r") {
		t.Errorf("lines = %d, last %q", len(got.Lines), got.Lines[len(got.Lines)-1])
	}
	if !strings.Contains(string(got.Result), `"copied"`) {
		t.Errorf("result = %s", got.Result)
	}
}

func TestDismissSetsNotice(t *testing.T) {
	f := newImportFixture(t, nil, nil)
	if rec := f.do("POST", "/api/import/laragon/dismiss", ""); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	cfg, err := config.Load()
	if err != nil || cfg.LaragonNotice != "dismissed" {
		t.Errorf("notice = %q, %v", cfg.LaragonNotice, err)
	}
}

func TestImportBlockedIs409(t *testing.T) {
	f := newImportFixture(t, nil, nil)
	runningServers = func(*laragon.Laragon) []string { return []string{"httpd.exe"} }
	rec := f.do("POST", "/api/import/laragon", `{"from":`+quote(f.root)+`,"dbs":[]}`)
	if rec.Code != 409 || f.ran != 0 || !strings.Contains(rec.Body.String(), "httpd.exe") {
		t.Errorf("blocked: %d %s ran=%d", rec.Code, rec.Body, f.ran)
	}
}

func TestImportClearsOldResult(t *testing.T) {
	f := newImportFixture(t, nil, nil)
	os.MkdirAll(config.LogDir(), 0o755)
	res := filepath.Join(config.LogDir(), "import-laragon.json")
	os.WriteFile(res, []byte(`{}`), 0o644)
	f.do("POST", "/api/import/laragon", `{"from":`+quote(f.root)+`,"dbs":[]}`)
	if _, err := os.Stat(res); err == nil || f.ran != 1 {
		t.Errorf("old result kept (err %v) ran=%d", err, f.ran)
	}
}

func TestStatusLaragon(t *testing.T) {
	t.Setenv("LAMAN_HOME", t.TempDir())
	d := &daemon{cfg: config.Default(), laragonFound: true}
	if st := d.status().Laragon; !st.Found || !st.Notice {
		t.Errorf("found: %+v", st)
	}
	cfg := config.Default()
	cfg.LaragonNotice = "dismissed"
	cfg.Save()
	if st := d.status().Laragon; !st.Found || st.Notice {
		t.Errorf("dismissed: %+v", st)
	}
	d.laragonFound = false
	cfg.LaragonNotice = ""
	cfg.Save()
	if st := d.status().Laragon; st.Found || st.Notice {
		t.Errorf("not found: %+v", st)
	}
}

func TestImportBusyKeepsResult(t *testing.T) {
	f := newImportFixture(t, nil, nil)
	runImportJob = func(d *daemon, args, env []string, before func()) error {
		return d.runJobStart("x", args, env, before, nil) // the real busy check
	}
	d := &daemon{cfg: config.Default()}
	d.installJob.running = true
	f.mux = http.NewServeMux()
	d.importRoutes(f.mux)
	os.MkdirAll(config.LogDir(), 0o755)
	res := filepath.Join(config.LogDir(), "import-laragon.json")
	os.WriteFile(res, []byte(`{}`), 0o644)
	rec := f.do("POST", "/api/import/laragon", `{"from":`+quote(f.root)+`,"dbs":[]}`)
	if rec.Code != 409 {
		t.Errorf("busy: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(res); err != nil {
		t.Errorf("result deleted by a refused POST: %v", err)
	}
}

// The plan names a database an interrupted import may have half-loaded,
// and leaves the record for the command to clear.
func TestImportPlanNotesInterruptedCopy(t *testing.T) {
	f := newImportFixture(t, []string{"acme"}, nil)
	if err := laragon.MarkCopying("acme"); err != nil {
		t.Fatal(err)
	}
	rec := f.do("GET", "/api/import/laragon?from="+f.root, "")
	if !strings.Contains(rec.Body.String(), `"notes":["acme may be incomplete from an interrupted import`) {
		t.Errorf("plan lacks the note: %s", rec.Body)
	}
	if _, err := os.Stat(filepath.Join(config.LogDir(), "import-laragon.copying")); err != nil {
		t.Errorf("the plan removed the record: %v", err)
	}
}
