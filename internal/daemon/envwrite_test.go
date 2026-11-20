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
	want := "APP_NAME=Acme\r\nREVERB_HOST=\"acme.test\"\r\n# REVERB_PORT=1\r\nREVERB_HOST_X=keep\r\n  export REVERB_SCHEME=https\r\nREVERB_APP_KEY=abc\r\nLAST=1\r\nREVERB_PORT=443\r\n"
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

func applyOne(t *testing.T, text string, c envChange) string {
	t.Helper()
	got, err := applyEnvChanges(text, []envChange{c})
	if err != nil {
		t.Fatalf("apply %+v on %q: %v", c, text, err)
	}
	return got
}

func checkApply(t *testing.T, text string, c envChange, want string) {
	t.Helper()
	if got := applyOne(t, text, c); got != want {
		t.Errorf("%q + %+v = %q, want %q", text, c, got, want)
	}
}

func TestApplySetKeepsComment(t *testing.T) {
	checkApply(t, "A=1 # one\n", envChange{"set", "A", "2"}, "A=2 # one\n")
}

func TestApplySetSpacing(t *testing.T) {
	checkApply(t, "export A = 1\n", envChange{"set", "A", "2"}, "export A = 2\n")
}

func TestApplySetKeepsQuoteStyle(t *testing.T) {
	checkApply(t, "N=\"Acme #1\"\n", envChange{"set", "N", "Shop #2"}, "N=\"Shop #2\"\n")
	checkApply(t, "S='x'\n", envChange{"set", "S", "y"}, "S='y'\n")
	checkApply(t, "S='x' # c\n", envChange{"set", "S", "it's"}, "S=\"it's\" # c\n")
	checkApply(t, "Q=\"x\"\n", envChange{"set", "Q", "y"}, "Q=\"y\"\n")
	checkApply(t, "Q=\"x\"\n", envChange{"set", "Q", ""}, "Q=\"\"\n")
}

func TestApplySetQuotesWhenNeeded(t *testing.T) {
	checkApply(t, "A=1\n", envChange{"set", "A", "two words"}, "A=\"two words\"\n")
	checkApply(t, "A=1\n", envChange{"set", "A", `say "hi"`}, "A=\"say \\\"hi\\\"\"\n")
	checkApply(t, "A=1\n", envChange{"set", "A", "a=b"}, "A=\"a=b\"\n")
	checkApply(t, "A=1\n", envChange{"set", "A", "$HOME"}, "A=\"$HOME\"\n")
	checkApply(t, "A=1\n", envChange{"set", "A", "plain-1/x"}, "A=plain-1/x\n")
}

func TestApplySetLastDuplicate(t *testing.T) {
	checkApply(t, "P=a\nP=b\n", envChange{"set", "P", "c"}, "P=a\nP=c\n")
}

func TestApplyDeleteAllDuplicates(t *testing.T) {
	checkApply(t, "P=a\nX=1\nP=b\n", envChange{"delete", "P", ""}, "X=1\n")
	checkApply(t, "# c\nA=1\n", envChange{"delete", "Z", ""}, "# c\nA=1\n")
}

func TestApplyAppend(t *testing.T) {
	checkApply(t, "A=1", envChange{"set", "B", "2"}, "A=1\nB=2\n")
	checkApply(t, "", envChange{"set", "B", "2"}, "B=2\n")
}

func TestApplyCRLFNoFinalNewline(t *testing.T) {
	checkApply(t, "A=1\r\nB=2", envChange{"set", "A", "3"}, "A=3\r\nB=2")
	checkApply(t, "A=1\r\nB=2", envChange{"set", "C", "4"}, "A=1\r\nB=2\r\nC=4\r\n")
}

func TestApplyLeavesMultilineAlone(t *testing.T) {
	text := "K=\"a\nA=9\nb\"\nA=1\n"
	checkApply(t, text, envChange{"set", "A", "2"}, "K=\"a\nA=9\nb\"\nA=2\n")
}

func TestApplyRefuses(t *testing.T) {
	multi := "K=\"a\nb\"\n"
	for _, tc := range []struct {
		text string
		c    envChange
	}{
		{"", envChange{"set", "1A", "x"}},
		{"", envChange{"set", "A", "a\nb"}},
		{multi, envChange{"set", "K", "x"}},
		{multi, envChange{"delete", "K", ""}},
		{"A=1\rB=2\n", envChange{"set", "A", "x"}},
		{"", envChange{"nope", "A", "x"}},
	} {
		if got, err := applyEnvChanges(tc.text, []envChange{tc.c}); err == nil {
			t.Errorf("%q + %+v = %q, want error", tc.text, tc.c, got)
		}
	}
}
