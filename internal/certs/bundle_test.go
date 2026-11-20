package certs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestCA(t *testing.T) *CA {
	t.Helper()
	ca, err := LoadOrCreateCA(t.TempDir(), []string{"test", "localhost"})
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

func caPEM(t *testing.T, ca *CA) string {
	t.Helper()
	b, err := os.ReadFile(ca.CertPath)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func TestAddToBundleAppends(t *testing.T) {
	ca := newTestCA(t)
	p := filepath.Join(t.TempDir(), "cacert.pem")
	os.WriteFile(p, []byte("MOZILLA\n"), 0o644)
	changed, err := ca.AddToBundle(p)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	got, _ := os.ReadFile(p)
	s := string(got)
	if !strings.HasPrefix(s, "MOZILLA\n") || strings.Count(s, "# BEGIN laman local CA") != 1 || !strings.Contains(s, caPEM(t, ca)) {
		t.Fatalf("unexpected bundle:\n%s", s)
	}
}

func TestAddToBundleIdempotent(t *testing.T) {
	ca := newTestCA(t)
	p := filepath.Join(t.TempDir(), "cacert.pem")
	os.WriteFile(p, []byte("MOZILLA\n"), 0o644)
	ca.AddToBundle(p)
	first, _ := os.ReadFile(p)
	changed, err := ca.AddToBundle(p)
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	second, _ := os.ReadFile(p)
	if string(first) != string(second) {
		t.Fatal("file changed")
	}
}

func TestAddToBundleReplacesOldCA(t *testing.T) {
	ca1, ca2 := newTestCA(t), newTestCA(t)
	p := filepath.Join(t.TempDir(), "cacert.pem")
	os.WriteFile(p, []byte("MOZILLA\n"), 0o644)
	ca1.AddToBundle(p)
	if _, err := ca2.AddToBundle(p); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	s := string(got)
	if strings.Count(s, "# BEGIN laman local CA") != 1 || !strings.Contains(s, caPEM(t, ca2)) || strings.Contains(s, caPEM(t, ca1)) {
		t.Fatalf("unexpected bundle:\n%s", s)
	}
	if !strings.HasPrefix(s, "MOZILLA\n# BEGIN") && !strings.HasPrefix(s, "MOZILLA\n\n# BEGIN") {
		t.Fatalf("prefix damaged:\n%s", s)
	}
}

func TestAddToBundleKeepsCRLF(t *testing.T) {
	ca := newTestCA(t)
	p := filepath.Join(t.TempDir(), "cacert.pem")
	os.WriteFile(p, []byte("MOZILLA\r\n"), 0o644)
	ca.AddToBundle(p)
	got, _ := os.ReadFile(p)
	if strings.Contains(strings.ReplaceAll(string(got), "\r\n", ""), "\n") {
		t.Fatalf("mixed line endings: %q", got)
	}
}

func TestAddToBundleMissingFile(t *testing.T) {
	ca := newTestCA(t)
	p := filepath.Join(t.TempDir(), "cacert.pem")
	changed, err := ca.AddToBundle(p)
	if changed || err != nil {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("file was created")
	}
}
