package install

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func writeZip(t *testing.T, names ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.zip")
	f, _ := os.Create(p)
	zw := zip.NewWriter(f)
	for _, n := range names {
		w, _ := zw.Create(n)
		w.Write([]byte(n))
	}
	zw.Close()
	f.Close()
	return p
}

func TestUnpackStripsSharedTopDir(t *testing.T) {
	z := writeZip(t, "nginx-1.30.6/nginx.exe", "nginx-1.30.6/conf/mime.types")
	dest := filepath.Join(t.TempDir(), "out")
	if err := unpack(z, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "conf", "mime.types")); err != nil {
		t.Fatal(err)
	}
}

func TestUnpackKeepsFlatLayout(t *testing.T) {
	z := writeZip(t, "php.exe", "ext/php_curl.dll")
	dest := filepath.Join(t.TempDir(), "out")
	if err := unpack(z, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "ext", "php_curl.dll")); err != nil {
		t.Fatal(err)
	}
}

func TestUnpackRejectsZipSlip(t *testing.T) {
	z := writeZip(t, "ok.txt", "../evil.txt")
	if err := unpack(z, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("expected an error for ../ entry")
	}
}
