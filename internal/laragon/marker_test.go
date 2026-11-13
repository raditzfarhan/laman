package laragon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInterruptedCopy(t *testing.T) {
	t.Setenv("LAMAN_HOME", t.TempDir())
	var p Plan
	if db := NoteInterrupted(&p, []string{"acme"}); db != "" || len(p.Notes) != 0 {
		t.Fatalf("no record: %q %v", db, p.Notes)
	}
	if err := MarkCopying("acme"); err != nil {
		t.Fatal(err)
	}
	// laman doesn't have it: Copy dropped it, nothing to say.
	if db := NoteInterrupted(&p, []string{"shop"}); db != "" || len(p.Notes) != 0 {
		t.Fatalf("not in laman: %q %v", db, p.Notes)
	}
	if db := NoteInterrupted(&p, []string{"shop", "ACME"}); db != "ACME" || len(p.Notes) != 1 ||
		!strings.Contains(p.Notes[0], "ACME may be incomplete from an interrupted import") {
		t.Fatalf("in laman: %q %v", db, p.Notes)
	}
	ClearCopying()
	if _, err := os.Stat(filepath.Join(os.Getenv("LAMAN_HOME"), "logs", copyingName)); !os.IsNotExist(err) {
		t.Errorf("record still there: %v", err)
	}
}

func TestPlanLaragonMariaDB(t *testing.T) {
	l, cfg := planFixture(t)
	write(t, filepath.Join(l.Root, "usr", "laragon.ini"), "[mysql]\r\nVersion=mariadb-11.4.2-winx64\r\n")
	l, err := Find(l.Root)
	if err != nil {
		t.Fatal(err)
	}
	if !l.MariaDB {
		t.Fatal("MariaDB not detected")
	}
	os.MkdirAll(filepath.Join(l.MySQLData, "acme"), 0o755)
	p := BuildPlan(l, cfg, nil, nil)
	if len(p.DBs) != 0 {
		t.Errorf("DBs = %+v, want none", p.DBs)
	}
	if len(p.Notes) != 1 || !strings.HasPrefix(p.Notes[0], "Laragon's MariaDB isn't supported yet; only MySQL") {
		t.Errorf("Notes = %q", p.Notes)
	}
	if len(p.Folders) != 1 {
		t.Errorf("Folders = %+v, want the www folder", p.Folders)
	}
}
