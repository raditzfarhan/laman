package install

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"laman/internal/config"
)

func TestPHPFits(t *testing.T) {
	for _, c := range []struct {
		v, rng string
		want   bool
	}{
		{"8.5.12-1", ">=8.4", true},
		{"8.3.36-1", ">=8.4", false},
		{"8.3.36-1", ">=7.2,<8.4", true},
		{"8.4.27-1", ">=7.2,<8.4", false},
		{"8.4", "", true},
	} {
		if got := PHPFits(c.v, c.rng); got != c.want {
			t.Errorf("PHPFits(%q, %q) = %v", c.v, c.rng, got)
		}
	}
}

func TestServerEnv(t *testing.T) {
	t.Setenv("LAMAN_HOME", t.TempDir())
	if ServerEnv("dbzuper") != nil {
		t.Fatal("env before any was written")
	}
	os.MkdirAll(filepath.Dir(ServerEnvPath("dbzuper")), 0o755)
	if err := setServerEnv("dbzuper", map[string]string{"DBZUPER_AUTO_LOGIN_KEY": "abc123"}); err != nil {
		t.Fatal(err)
	}
	if got := ServerEnv("dbzuper"); len(got) != 1 || got["DBZUPER_AUTO_LOGIN_KEY"] != "abc123" {
		t.Fatalf("ServerEnv = %v", got)
	}
	// Hand edits that could break out of the Caddyfile are dropped.
	os.WriteFile(ServerEnvPath("dbzuper"), []byte("# c\r\nOK=1\r\nlower=x\r\nBAD=a\"b\r\nSPACE=a b\r\nBRACE=}\r\nEMPTY=\r\n"), 0o600)
	if got := ServerEnv("dbzuper"); len(got) != 1 || got["OK"] != "1" {
		t.Fatalf("ServerEnv kept unsafe lines: %v", got)
	}
	if err := RemoveApp("dbzuper"); err != nil {
		t.Fatal(err)
	}
	if ServerEnv("dbzuper") != nil {
		t.Error("RemoveApp kept the server env")
	}
}

func TestSetEnv(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(p, []byte("APP_URL=http://localhost:8000\r\n# DBZUPER_PASSWORD_POLICY=strong\r\nAPP_DEBUG=true\r\n"), 0o644)
	if err := setEnv(p, map[string]string{"APP_URL": "https://dbzuper.test", "DBZUPER_PASSWORD_POLICY": "basic", "NEW_ONE": "x"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	s := string(b)
	for _, want := range []string{"APP_URL=https://dbzuper.test\n", "DBZUPER_PASSWORD_POLICY=basic\n", "APP_DEBUG=true\n", "NEW_ONE=x\n"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Count(s, "APP_URL=") != 1 {
		t.Errorf("APP_URL duplicated:\n%s", s)
	}
}

func TestPHPRedisAdminConfig(t *testing.T) {
	sample := "<?php\n$config = array(\n  'servers' => array(\n    array(\n      'name'   => 'local server',\n      'port'   => 6379,\n    ),\n    /*array(\n      'port'   => 6379,\n    ),*/\n  ),\n  'allow_no_auth' => false,\n);\n"
	out, err := phpRedisAdminConfig(sample, 6390)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"'laman Redis'", "'port'   => 6390,", "'allow_no_auth' => true,"} {
		if !strings.Contains(out, want) {
			t.Errorf("config lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "6379") != 1 {
		t.Errorf("only the first server should change:\n%s", out)
	}
	if _, err := phpRedisAdminConfig("<?php $config = array();", 6390); err == nil {
		t.Error("a changed sample should be refused")
	}
}

// fakeApp: install writes "new" into db.txt and app.txt; keep carries db.txt.
func fakeApp(prepare func(dir string, c *AppCtx) error) *AppSpec {
	return &AppSpec{Name: "fake", keep: []string{"db.txt"}, prepare: prepare,
		install: func(dir, _ string, _ *AppCtx) error {
			os.MkdirAll(dir, 0o755)
			os.WriteFile(filepath.Join(dir, "db.txt"), []byte("staging"), 0o644)
			return os.WriteFile(filepath.Join(dir, "app.txt"), []byte("new"), 0o644)
		}}
}

// appHome points laman at a temp home and, when old is set, installs an
// old copy of the fake app (db.txt "old data", app.txt "old").
func appHome(t *testing.T, old bool) string {
	t.Helper()
	t.Setenv("LAMAN_HOME", t.TempDir())
	os.MkdirAll(config.AppsDir(), 0o755)
	dir := AppDir("fake")
	if old {
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "db.txt"), []byte("old data"), 0o644)
		os.WriteFile(filepath.Join(dir, "app.txt"), []byte("old"), 0o644)
	}
	return dir
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func noSideDirs(t *testing.T, dir string) {
	t.Helper()
	for _, s := range []string{".new", ".old"} {
		if exists(dir + s) {
			t.Errorf("%s left behind", dir+s)
		}
	}
}

func TestInstallAppPrepareSeesCarriedData(t *testing.T) {
	dir := appHome(t, true)
	spec := fakeApp(func(d string, _ *AppCtx) error {
		if got := readFile(t, filepath.Join(d, "db.txt")); got != "old data" {
			t.Errorf("prepare saw db.txt = %q", got)
		}
		return os.WriteFile(filepath.Join(d, "db.txt"), []byte("old data+migrated"), 0o644)
	})
	if _, err := InstallApp(spec, "1", &AppCtx{}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir, "db.txt")); got != "old data+migrated" {
		t.Errorf("db.txt = %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "app.txt")); got != "new" {
		t.Errorf("app.txt = %q", got)
	}
	noSideDirs(t, dir)
}

func TestInstallAppPrepareFailureKeepsOld(t *testing.T) {
	dir := appHome(t, true)
	spec := fakeApp(func(string, *AppCtx) error { return errors.New("migrate failed") })
	_, err := InstallApp(spec, "1", &AppCtx{})
	if err == nil || !strings.Contains(err.Error(), "migrate failed") {
		t.Fatalf("err = %v", err)
	}
	if got := readFile(t, filepath.Join(dir, "db.txt")); got != "old data" {
		t.Errorf("db.txt = %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "app.txt")); got != "old" {
		t.Errorf("app.txt = %q", got)
	}
	noSideDirs(t, dir)
}

func TestInstallAppWorkerHooks(t *testing.T) {
	var calls []string
	ctx := func(stopErr error) *AppCtx {
		calls = nil
		return &AppCtx{
			StopWorkers:  func() error { calls = append(calls, "stop"); return stopErr },
			StartWorkers: func() { calls = append(calls, "start") },
		}
	}
	record := func(err error) *AppSpec {
		return fakeApp(func(string, *AppCtx) error { calls = append(calls, "prepare"); return err })
	}
	want := func(label string, wantCalls ...string) {
		t.Helper()
		if strings.Join(calls, " ") != strings.Join(wantCalls, " ") {
			t.Errorf("%s: calls = %v, want %v", label, calls, wantCalls)
		}
	}

	appHome(t, true)
	if _, err := InstallApp(record(nil), "1", ctx(nil)); err != nil {
		t.Fatal(err)
	}
	want("success", "stop", "prepare", "start")

	appHome(t, true)
	if _, err := InstallApp(record(errors.New("boom")), "1", ctx(nil)); err == nil {
		t.Fatal("want error")
	}
	want("prepare fails", "stop", "prepare", "start")

	dir := appHome(t, true)
	_, err := InstallApp(record(nil), "1", ctx(errors.New("stuck")))
	if err == nil || !strings.Contains(err.Error(), "stuck") {
		t.Fatalf("err = %v", err)
	}
	want("stop fails", "stop", "start")
	if got := readFile(t, filepath.Join(dir, "app.txt")); got != "old" {
		t.Errorf("app.txt = %q", got)
	}
	noSideDirs(t, dir)
}

func TestInstallAppFreshSkipsHooks(t *testing.T) {
	dir := appHome(t, false)
	var calls []string
	c := &AppCtx{
		StopWorkers:  func() error { calls = append(calls, "stop"); return nil },
		StartWorkers: func() { calls = append(calls, "start") },
	}
	spec := fakeApp(func(string, *AppCtx) error { calls = append(calls, "prepare"); return nil })
	if _, err := InstallApp(spec, "1", c); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Errorf("calls = %v", calls)
	}
	if got := readFile(t, filepath.Join(dir, "app.txt")); got != "new" {
		t.Errorf("app.txt = %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "db.txt")); got != "staging" {
		t.Errorf("db.txt = %q", got)
	}
}

func TestInstallAppCarriesKeep(t *testing.T) {
	dir := appHome(t, true)
	os.WriteFile(filepath.Join(dir, "db.txt-wal"), []byte("old wal"), 0o644)
	spec := fakeApp(nil)
	spec.keep = []string{"db.txt", "db.txt-wal"}
	if _, err := InstallApp(spec, "1", &AppCtx{}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir, "db.txt-wal")); got != "old wal" {
		t.Errorf("wal = %q", got)
	}

	// Old has no sidecar but the fresh build ships one: it must not pair
	// with the carried database.
	dir = appHome(t, true)
	spec = fakeApp(nil)
	spec.keep = []string{"db.txt", "db.txt-wal"}
	inner := spec.install
	spec.install = func(d, v string, c *AppCtx) error {
		if err := inner(d, v, c); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(d, "db.txt-wal"), []byte("staging wal"), 0o644)
	}
	if _, err := InstallApp(spec, "1", &AppCtx{}); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(dir, "db.txt-wal")) {
		t.Error("staging's db.txt-wal survived the update")
	}
}

func TestInstallAppPrepareMutatesThenFailsLeavesOldData(t *testing.T) {
	dir := appHome(t, true)
	spec := fakeApp(func(d string, _ *AppCtx) error {
		p := filepath.Join(d, "db.txt")
		os.WriteFile(p, []byte(readFile(t, p)+"+half"), 0o644)
		return errors.New("migrate failed")
	})
	if _, err := InstallApp(spec, "1", &AppCtx{}); err == nil {
		t.Fatal("want error")
	}
	if got := readFile(t, filepath.Join(dir, "db.txt")); got != "old data" {
		t.Errorf("db.txt = %q", got)
	}
	noSideDirs(t, dir)
}

func TestInstallAppCarryFailureRestoresFolders(t *testing.T) {
	dir := appHome(t, true)
	os.MkdirAll(filepath.Join(dir, "data"), 0o755)
	os.WriteFile(filepath.Join(dir, "data", "f.txt"), []byte("kept"), 0o644)
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "sub", "x"), []byte("x"), 0o644)
	spec := fakeApp(nil)
	// "data" moves into staging first; "sub/x" then cannot be copied because
	// the new build ships "sub" as a file.
	spec.keep = []string{"data", "sub/x"}
	inner := spec.install
	spec.install = func(d, v string, c *AppCtx) error {
		if err := inner(d, v, c); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(d, "sub"), []byte("file"), 0o644)
	}
	_, err := InstallApp(spec, "1", &AppCtx{})
	if err == nil || !strings.Contains(err.Error(), "keep sub/x") {
		t.Fatalf("err = %v", err)
	}
	if got := readFile(t, filepath.Join(dir, "data", "f.txt")); got != "kept" {
		t.Errorf("data/f.txt = %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "app.txt")); got != "old" {
		t.Errorf("app.txt = %q", got)
	}
	noSideDirs(t, dir)
}
