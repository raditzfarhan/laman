package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.2.0", "0.1.0", true},
		{"0.10.0", "0.9.9", true},
		{"1.0.0", "0.99.99", true},
		{"0.1.0", "0.1.0", false},
		{"0.1.0", "0.2.0", false},
		{"v0.2.0", "0.1.0", true},
		{"0.2.0", "dev", true},
		{"junk", "0.1.0", false},
		{"0.2", "0.1.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// fakeGitHub serves a release whose laman.exe has the given checksum line.
func fakeGitHub(t *testing.T, exe []byte, sumFor func([]byte) string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			fmt.Fprintf(w, `{"tag_name":"v9.8.7","html_url":"https://example.invalid/r","assets":[
				{"name":"laman.exe","browser_download_url":"%[1]s/dl/laman.exe"},
				{"name":"laman-setup-9.8.7.exe","browser_download_url":"%[1]s/dl/setup.exe"},
				{"name":"checksums.txt","browser_download_url":"%[1]s/dl/checksums.txt"}]}`, srv.URL)
		case r.URL.Path == "/dl/laman.exe":
			w.Write(exe)
		case r.URL.Path == "/dl/checksums.txt":
			fmt.Fprintf(w, "%s  laman-setup-9.8.7.exe\n%s  laman.exe\n", strings.Repeat("0", 64), sumFor(exe))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old, oldAPI := httpClient, API
	httpClient, API = srv.Client(), srv.URL
	t.Cleanup(func() { httpClient, API = old, oldAPI })
	return srv
}

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func TestLatestAndDownload(t *testing.T) {
	exe := []byte("MZ pretend laman")
	fakeGitHub(t, exe, sha)
	r, err := Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != "9.8.7" || r.URL != "https://example.invalid/r" {
		t.Fatalf("release = %+v", r)
	}
	dest := filepath.Join(t.TempDir(), "laman.exe")
	if err := Download(context.Background(), r, dest); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); string(got) != string(exe) {
		t.Fatalf("downloaded %q", got)
	}
}

func TestDownloadRefusesBadChecksum(t *testing.T) {
	fakeGitHub(t, []byte("MZ tampered"), func([]byte) string { return strings.Repeat("a", 64) })
	r, err := Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "laman.exe")
	if err := Download(context.Background(), r, dest); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("err = %v, want a checksum error", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("a file was written despite the bad checksum")
	}
}

func TestNoReleases(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	old, oldAPI := httpClient, API
	httpClient, API = srv.Client(), srv.URL
	defer func() { httpClient, API = old, oldAPI }()
	if _, err := Latest(context.Background()); err != ErrNoReleases {
		t.Fatalf("err = %v, want ErrNoReleases", err)
	}
}
