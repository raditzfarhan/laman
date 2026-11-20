package daemon

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReverbEnv(t *testing.T) {
	want := []envKV{
		{"BROADCAST_CONNECTION", "reverb"},
		{"REVERB_HOST", "127.0.0.1"},
		{"REVERB_PORT", "8101"},
		{"REVERB_SCHEME", "http"},
		{"REVERB_SERVER_HOST", "127.0.0.1"},
		{"REVERB_SERVER_PORT", "8101"},
		{"VITE_REVERB_HOST", "acme.test"},
		{"VITE_REVERB_PORT", "443"},
		{"VITE_REVERB_SCHEME", "https"},
	}
	if got := reverbEnv("acme.test", "https", 443, 8101); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestWriteEnvKeysKeepsEverythingElse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	orig := "APP_NAME=Acme\r\nREVERB_HOST=\"localhost\"\r\n# REVERB_PORT=1\r\nREVERB_HOST_X=keep\r\n  export REVERB_SCHEME=http\r\nREVERB_APP_KEY=abc\r\nLAST=1"
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	kvs := []envKV{{"REVERB_HOST", "acme.test"}, {"REVERB_PORT", "443"}, {"REVERB_SCHEME", "https"}}
	backup := filepath.Join(t.TempDir(), "env-backups", "acme.env")
	if wrote, err := writeEnvKeys(path, backup, kvs); err != nil || !wrote {
		t.Fatal(wrote, err)
	}
	got, _ := os.ReadFile(path)
	want := "APP_NAME=Acme\r\nREVERB_HOST=acme.test\r\n# REVERB_PORT=1\r\nREVERB_HOST_X=keep\r\n  export REVERB_SCHEME=https\r\nREVERB_APP_KEY=abc\r\nLAST=1\r\nREVERB_PORT=443\r\n"
	if string(got) != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	bak, _ := os.ReadFile(backup)
	if string(bak) != orig {
		t.Fatalf("backup %q", bak)
	}
	// Nothing new in the project folder besides .env itself.
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Errorf("project folder holds %d entries, want only .env", len(ents))
	}
}

func TestEnvMismatch(t *testing.T) {
	vals := map[string]string{"A": `"x"`, "B": "'y'", "C": "z"}
	got := envMismatch(vals, []envKV{{"A", "x"}, {"B", "y"}, {"C", "q"}, {"D", "1"}})
	if !reflect.DeepEqual(got, []string{"C", "D"}) {
		t.Fatalf("got %v", got)
	}
}

func TestWriteEnvKeysNoChangeKeepsBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	orig := "A=1\nREVERB_HOST=old\n"
	os.WriteFile(path, []byte(orig), 0o644)
	kvs := []envKV{{"REVERB_HOST", "acme.test"}}
	backup := filepath.Join(t.TempDir(), "acme.env")
	if wrote, err := writeEnvKeys(path, backup, kvs); err != nil || !wrote {
		t.Fatal(wrote, err)
	}
	if wrote, err := writeEnvKeys(path, backup, kvs); err != nil || wrote {
		t.Fatal("second write:", wrote, err)
	}
	bak, _ := os.ReadFile(backup)
	if string(bak) != orig {
		t.Fatalf("backup %q", bak)
	}
}

func TestWriteEnvKeysRefusesBareCR(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	orig := "A=1\rB=2\r\nC=3\n"
	os.WriteFile(path, []byte(orig), 0o644)
	backup := filepath.Join(t.TempDir(), "acme.env")
	if _, err := writeEnvKeys(path, backup, []envKV{{"A", "9"}}); err == nil {
		t.Fatal("want error")
	}
	got, _ := os.ReadFile(path)
	if string(got) != orig {
		t.Fatalf("file changed: %q", got)
	}
	if _, err := os.Stat(backup); err == nil {
		t.Fatal("backup written")
	}
}
