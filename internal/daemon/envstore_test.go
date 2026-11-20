package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"laman/internal/config"
	"laman/internal/sites"
)

func envStoreFixture(t *testing.T, env string) (*daemon, sites.Site) {
	t.Helper()
	t.Setenv("LAMAN_HOME", t.TempDir())
	dir := t.TempDir()
	if env != "" {
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &daemon{cfg: config.Default()}, sites.Site{Name: "acme", Host: "acme.test", Path: dir}
}

func setA(v string) func(string) (string, error) {
	return func(o string) (string, error) {
		return applyEnvChanges(o, []envChange{{"set", "A", v}})
	}
}

func TestWriteEnvBacksUpAndWrites(t *testing.T) {
	d, s := envStoreFixture(t, "A=1\n")
	saved, err := d.writeEnv(s, envFingerprint([]byte("A=1\n")), setA("2"))
	if err != nil || !saved.Changed {
		t.Fatal(saved, err)
	}
	if b, _ := os.ReadFile(filepath.Join(s.Path, ".env")); string(b) != "A=2\n" {
		t.Errorf(".env = %q", b)
	}
	if b, err := readEnvBackup("acme", saved.Backup); err != nil || string(b) != "A=1\n" {
		t.Errorf("backup = %q, %v", b, err)
	}
	if saved.Fingerprint != envFingerprint([]byte("A=2\n")) {
		t.Errorf("fingerprint = %s", saved.Fingerprint)
	}
}

func TestWriteEnvConflict(t *testing.T) {
	d, s := envStoreFixture(t, "A=1\n")
	_, err := d.writeEnv(s, envFingerprint([]byte("A=0\n")), setA("2"))
	if !errors.Is(err, errEnvChanged) {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(s.Path, ".env")); string(b) != "A=1\n" {
		t.Errorf(".env = %q", b)
	}
	if exists(envBackupDir("acme")) {
		t.Errorf("backup dir created")
	}
}

func TestWriteEnvMissingAndTooBig(t *testing.T) {
	d, s := envStoreFixture(t, "")
	if _, err := d.writeEnv(s, "", setA("2")); err == nil || !strings.Contains(err.Error(), "no .env") {
		t.Errorf("missing: %v", err)
	}
	big := "A=" + strings.Repeat("x", envMaxSize) + "\n"
	d, s = envStoreFixture(t, big)
	if _, err := d.writeEnv(s, "", setA("2")); err == nil {
		t.Errorf("too big: no error")
	}
}

func TestWriteEnvNoChangeNoBackup(t *testing.T) {
	d, s := envStoreFixture(t, "A=1\n")
	saved, err := d.writeEnv(s, "", func(o string) (string, error) { return o, nil })
	if err != nil || saved.Changed || saved.Backup != "" || saved.Fingerprint != envFingerprint([]byte("A=1\n")) {
		t.Fatal(saved, err)
	}
	if exists(envBackupDir("acme")) {
		t.Errorf("backup written")
	}
}

func TestWriteEnvKeepsEverythingElse(t *testing.T) {
	orig := "APP_NAME=Acme\r\nREVERB_HOST=\"localhost\"\r\n# REVERB_PORT=1\r\nREVERB_HOST_X=keep\r\n  export REVERB_SCHEME=http\r\nREVERB_APP_KEY=abc\r\nLAST=1"
	d, s := envStoreFixture(t, orig)
	kvs := []envKV{{"REVERB_HOST", "acme.test"}, {"REVERB_PORT", "443"}, {"REVERB_SCHEME", "https"}}
	changes := make([]envChange, len(kvs))
	for i, kv := range kvs {
		changes[i] = envChange{"set", kv.Key, kv.Value}
	}
	saved, err := d.writeEnv(s, "", func(o string) (string, error) { return applyEnvChanges(o, changes) })
	if err != nil || !saved.Changed {
		t.Fatal(saved, err)
	}
	got, _ := os.ReadFile(filepath.Join(s.Path, ".env"))
	want := "APP_NAME=Acme\r\nREVERB_HOST=\"acme.test\"\r\n# REVERB_PORT=1\r\nREVERB_HOST_X=keep\r\n  export REVERB_SCHEME=https\r\nREVERB_APP_KEY=abc\r\nLAST=1\r\nREVERB_PORT=443\r\n"
	if string(got) != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if ents, _ := os.ReadDir(s.Path); len(ents) != 1 {
		t.Errorf("project folder holds %d entries, want only .env", len(ents))
	}
}

func TestWriteEnvRefusesBareCR(t *testing.T) {
	orig := "A=1\rB=2\r\nC=3\n"
	d, s := envStoreFixture(t, orig)
	if _, err := d.writeEnv(s, "", setA("9")); err == nil {
		t.Fatal("want error")
	}
	if b, _ := os.ReadFile(filepath.Join(s.Path, ".env")); string(b) != orig {
		t.Fatalf("file changed: %q", b)
	}
	if exists(envBackupDir("acme")) {
		t.Fatal("backup written")
	}
}

func TestBackupRotation(t *testing.T) {
	t.Setenv("LAMAN_HOME", t.TempDir())
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local)
	var first []string
	for i := 0; i < 12; i++ {
		id, err := saveEnvBackup("acme", []byte{byte('a' + i)}, 0o644, start.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if i < 2 {
			first = append(first, id)
		}
	}
	list, err := listEnvBackups("acme")
	if err != nil || len(list) != envKeepBackups {
		t.Fatalf("list = %v, %v", list, err)
	}
	for _, id := range first {
		if exists(filepath.Join(envBackupDir("acme"), id+".env")) {
			t.Errorf("%s kept", id)
		}
	}
	if b, _ := readEnvBackup("acme", list[0].ID); string(b) != "l" {
		t.Errorf("newest = %q", b)
	}
	if !list[0].Time.Equal(start.Add(11*time.Second)) || list[0].Size != 1 {
		t.Errorf("newest entry = %+v", list[0])
	}
}

func TestBackupSameSecond(t *testing.T) {
	t.Setenv("LAMAN_HOME", t.TempDir())
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local)
	var ids []string
	for i := 0; i < 11; i++ {
		id, err := saveEnvBackup("acme", []byte{byte('a' + i)}, 0o644, now)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if ids[0] != "20260102-030405" || ids[1] != "20260102-030405-2" || ids[10] != "20260102-030405-11" {
		t.Fatalf("ids = %v", ids)
	}
	list, _ := listEnvBackups("acme")
	var got []string
	for _, b := range list {
		got = append(got, b.ID)
	}
	// Newest first, -11 and -10 before -9 (numeric, not lexical); the base id
	// was rotated out.
	want := []string{"20260102-030405-11", "20260102-030405-10", "20260102-030405-9", "20260102-030405-8", "20260102-030405-7",
		"20260102-030405-6", "20260102-030405-5", "20260102-030405-4", "20260102-030405-3", "20260102-030405-2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("list = %v", got)
	}
	if b, err := readEnvBackup("acme", "20260102-030405-2"); err != nil || string(b) != "b" {
		t.Errorf("-2 = %q, %v", b, err)
	}
}

func TestBackupSameSecondOrder(t *testing.T) {
	t.Setenv("LAMAN_HOME", t.TempDir())
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local)
	saveEnvBackup("acme", []byte("1"), 0o644, now)
	saveEnvBackup("acme", []byte("2"), 0o644, now)
	list, _ := listEnvBackups("acme")
	if len(list) != 2 || list[0].ID != "20260102-030405-2" || list[1].ID != "20260102-030405" {
		t.Fatalf("list = %+v", list)
	}
}

func TestBackupMigratesLegacy(t *testing.T) {
	t.Setenv("LAMAN_HOME", t.TempDir())
	legacy := filepath.Join(config.DataDir(), "env-backups", "acme.env")
	os.MkdirAll(filepath.Dir(legacy), 0o755)
	os.WriteFile(legacy, []byte("OLD=1\n"), 0o644)
	mt := time.Date(2025, 5, 6, 7, 8, 9, 0, time.Local)
	os.Chtimes(legacy, mt, mt)

	// A listing alone shows it.
	list, err := listEnvBackups("acme")
	if err != nil || len(list) != 1 || list[0].ID != "20250506-070809" || !list[0].Time.Equal(mt) {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if exists(legacy) {
		t.Error("legacy file still there")
	}

	// And a save migrates too.
	os.WriteFile(legacy, []byte("OLD=2\n"), 0o644)
	os.Chtimes(legacy, mt.Add(time.Hour), mt.Add(time.Hour))
	if _, err := saveEnvBackup("acme", []byte("NEW\n"), 0o644, time.Now()); err != nil {
		t.Fatal(err)
	}
	list, _ = listEnvBackups("acme")
	if len(list) != 3 || exists(legacy) {
		t.Fatalf("list = %+v", list)
	}
	if b, _ := readEnvBackup("acme", "20250506-080809"); string(b) != "OLD=2\n" {
		t.Errorf("migrated = %q", b)
	}
}

func TestReadEnvBackupRejectsBadID(t *testing.T) {
	t.Setenv("LAMAN_HOME", t.TempDir())
	for _, id := range []string{"../x", "x", "20260102-030405/../../x", ""} {
		if _, err := readEnvBackup("acme", id); err == nil {
			t.Errorf("%q accepted", id)
		}
	}
}

func TestSaveConflictAfterReverb(t *testing.T) {
	d, s := envStoreFixture(t, "A=1\n")
	fp := envFingerprint([]byte("A=1\n"))
	if saved, err := d.writeEnv(s, "", setA("2")); err != nil || !saved.Changed {
		t.Fatal(saved, err)
	}
	if _, err := d.writeEnv(s, fp, setA("3")); !errors.Is(err, errEnvChanged) {
		t.Fatalf("err = %v", err)
	}
}
