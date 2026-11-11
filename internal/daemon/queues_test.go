package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaravelSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Laravel":        "laravel",
		"My Blog":        "my_blog",
		"my-shop":        "my_shop",
		"  Acme & Co!! ": "acme_co",
		"ops@home":       "ops_at_home",
	} {
		if got := laravelSlug(in); got != want {
			t.Errorf("laravelSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReadLaravelEnv(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) { os.WriteFile(filepath.Join(dir, ".env"), []byte(s), 0o644) }

	write("APP_NAME=\"My Blog\"\r\nAPP_KEY=base64:x\r\nQUEUE_CONNECTION=redis\r\nREDIS_DB=2\r\n")
	e, ok := readLaravelEnv(dir)
	if !ok || e.prefix != "my_blog_database_" || e.db != 2 || e.driver != "redis" {
		t.Errorf("got %+v %v", e, ok)
	}

	write("APP_KEY=x\nREDIS_PREFIX=\nQUEUE_CONNECTION=redis\n")
	if e, _ := readLaravelEnv(dir); e.prefix != "" {
		t.Errorf("an empty REDIS_PREFIX is a prefix too: %+v", e)
	}

	write("FOO=bar\n")
	if _, ok := readLaravelEnv(dir); ok {
		t.Error("a .env without APP_KEY is not Laravel")
	}
}

func TestDBTargetFor(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "database"), 0o755)

	got, why := dbTargetFor("a.test", dir, map[string]string{"DB_CONNECTION": "mysql", "DB_DATABASE": "shop", "DB_HOST": "localhost"}, true)
	if why != "" || got.Conn != "mysql" || got.Host != "127.0.0.1" || got.Port != 3306 || got.Database != "shop" ||
		got.Username != "root" || got.Table != "jobs" || got.Failed != "failed_jobs" || !got.Jobs {
		t.Errorf("mysql: %+v %q", got, why)
	}
	got, why = dbTargetFor("a.test", dir, map[string]string{"DB_CONNECTION": "mysql", "DB_QUEUE_CONNECTION": "pgsql", "DB_QUEUE_TABLE": "queue_jobs"}, true)
	if why != "" || got.Conn != "pgsql" || got.Port != 5432 || got.Table != "queue_jobs" {
		t.Errorf("DB_QUEUE_CONNECTION: %+v %q", got, why)
	}
	if _, why = dbTargetFor("a.test", dir, map[string]string{"DB_CONNECTION": "mysql", "DB_HOST": "db.example.com"}, true); !strings.Contains(why, "db.example.com") {
		t.Errorf("remote host: %q", why)
	}
	if _, why = dbTargetFor("a.test", dir, map[string]string{"DB_CONNECTION": "mysql", "DB_QUEUE_TABLE": "jobs; drop"}, true); why == "" {
		t.Error("an odd table name was accepted")
	}
	if _, why = dbTargetFor("a.test", dir, map[string]string{}, true); why == "" {
		t.Error("no DB_CONNECTION and no SQLite file should not be checked")
	}
	db := filepath.Join(dir, "database", "database.sqlite")
	os.WriteFile(db, nil, 0o644)
	if got, why = dbTargetFor("a.test", dir, map[string]string{}, true); why != "" || got.Conn != "sqlite" || got.Database != db {
		t.Errorf("default SQLite: %+v %q", got, why)
	}
	if got, why = dbTargetFor("a.test", dir, map[string]string{"DB_CONNECTION": "sqlite", "DB_DATABASE": "database/database.sqlite"}, true); why != "" || got.Database != db {
		t.Errorf("relative SQLite path: %+v %q", got, why)
	}
	if _, why = dbTargetFor("a.test", dir, map[string]string{"DB_CONNECTION": "sqlsrv"}, true); why == "" {
		t.Error("sqlsrv should not be checked")
	}
}
