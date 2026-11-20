package fsx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRenameMoves(t *testing.T) {
	d := t.TempDir()
	a, b := filepath.Join(d, "a"), filepath.Join(d, "b")
	os.WriteFile(a, []byte("1"), 0o644)
	if err := Rename(a, b); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(b); err != nil {
		t.Fatal(err)
	}
	if err := Rename(a, b); err == nil {
		t.Fatal("missing source should fail")
	}
}
