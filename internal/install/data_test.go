package install

import (
	"os"
	"path/filepath"
	"testing"

	"laman/internal/config"
)

func mkdirs(t *testing.T, paths ...string) {
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// An existing install (one shared data folder, nothing pinned) is pinned to
// the version it was running, and its data moves to that line's folder.
func TestPinAndMoveLegacyData(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LAMAN_HOME", home)
	mkdirs(t,
		filepath.Join(config.BinDir("mariadb"), "11.4.5"),
		filepath.Join(config.BinDir("mariadb"), "12.3.3"),
		filepath.Join(config.BinDir("redis"), "8.10.3"),
		filepath.Join(config.DataDir(), "mariadb", "mysql"),
		filepath.Join(config.DataDir(), "redis"),
	)
	os.WriteFile(filepath.Join(config.DataDir(), "redis", "dump.rdb"), []byte("x"), 0o644)

	cfg := config.Default()
	if err := PinVersions(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.MariaDB != "12.3" || cfg.Redis != "8.10" {
		t.Fatalf("pinned %q / %q, want 12.3 / 8.10", cfg.MariaDB, cfg.Redis)
	}
	if err := MoveLegacyData(cfg); err != nil {
		t.Fatal(err)
	}
	if !HasMariaDBData("12.3.3") {
		t.Fatal("MariaDB data did not move to mariadb-12.3")
	}
	if _, err := os.Stat(filepath.Join(RedisDataDir("8.10.3"), "dump.rdb")); err != nil {
		t.Fatal("Redis data did not move to redis-8.10")
	}
	// Running it again is a no-op.
	if err := MoveLegacyData(cfg); err != nil {
		t.Fatal(err)
	}
	// Installing a newer line later must not change the pin.
	mkdirs(t, filepath.Join(config.BinDir("mariadb"), "13.0.1"))
	PinVersions(cfg)
	if got := config.Active("mariadb", cfg.MariaDB); got != "12.3.3" {
		t.Fatalf("active MariaDB %s after installing 13.0, want 12.3.3", got)
	}
}

func TestMoveLegacyDataRefusesToMerge(t *testing.T) {
	t.Setenv("LAMAN_HOME", t.TempDir())
	mkdirs(t, filepath.Join(config.DataDir(), "mariadb"), filepath.Join(config.DataDir(), "mariadb-12.3"))
	cfg := config.Default()
	cfg.MariaDB = "12.3"
	if err := MoveLegacyData(cfg); err == nil {
		t.Fatal("expected an error when both folders exist")
	}
}

func TestCopyTree(t *testing.T) {
	dir := t.TempDir()
	src, dest := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	mkdirs(t, filepath.Join(src, "db", "x"))
	os.WriteFile(filepath.Join(src, "db", "x", "t.ibd"), []byte("rows"), 0o644)
	os.WriteFile(filepath.Join(src, "my.ini"), []byte("[mysqld]"), 0o644)
	if err := CopyTree(src, dest); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "db", "x", "t.ibd")); string(b) != "rows" {
		t.Fatalf("copied file = %q", b)
	}
	if err := CopyTree(src, dest); err == nil {
		t.Fatal("CopyTree overwrote an existing folder")
	}
	if _, err := os.Stat(dest + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("left a .tmp folder behind")
	}
}

func TestPickRedis(t *testing.T) {
	asset := func(v string) ghRelease {
		r := ghRelease{Tag: v}
		r.Assets = append(r.Assets, struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Digest string `json:"digest"`
		}{Name: "Redis-" + v + "-Windows-x64-msys2.zip"})
		return r
	}
	pre := asset("8.12.0")
	pre.Prerelease = true
	list := []ghRelease{pre, asset("8.10.3"), asset("8.10.2"), asset("7.4.6"), {Tag: "7.4.7"}}
	for want, exp := range map[string]string{"": "8.10.3", "8.10": "8.10.3", "7.4": "7.4.6", "7.4.6": "7.4.6"} {
		r, err := pickRedis(list, want)
		if err != nil || r.Tag != exp {
			t.Errorf("pickRedis(%q) = %s, %v; want %s", want, r.Tag, err, exp)
		}
	}
	if _, err := pickRedis(list, "6.2"); err == nil {
		t.Error("expected an error for a line with no build")
	}
}
