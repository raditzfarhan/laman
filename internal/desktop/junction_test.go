//go:build windows

package desktop

import (
	"os"
	"path/filepath"
	"testing"
)

func TestJunction(t *testing.T) {
	dir := t.TempDir()
	a, b, link := filepath.Join(dir, "a"), filepath.Join(dir, "b"), filepath.Join(dir, "current")
	for _, d := range []string{a, b} {
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "which"), []byte(filepath.Base(d)), 0o644)
	}
	read := func() string { b, _ := os.ReadFile(filepath.Join(link, "which")); return string(b) }

	if err := Junction(link, a); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "a" {
		t.Fatalf("through link: %q, want a", got)
	}
	// Re-pointing must remove only the link, never the old target's files.
	if err := Junction(link, b); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "b" {
		t.Fatalf("after re-point: %q, want b", got)
	}
	if _, err := os.Stat(filepath.Join(a, "which")); err != nil {
		t.Fatalf("old target was damaged: %v", err)
	}
	// A real directory in the way is left alone.
	if err := Junction(a, b); err == nil {
		t.Fatal("Junction replaced a real directory")
	}
	if _, err := os.Stat(filepath.Join(a, "which")); err != nil {
		t.Fatalf("real directory was damaged: %v", err)
	}
}

func TestUserName(t *testing.T) {
	n := UserName()
	if n == "" {
		t.Fatal("no user name")
	}
	t.Logf("Windows user name: %q", n)
}
