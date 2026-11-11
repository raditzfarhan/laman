package certs

import (
	"crypto/x509"
	"net"
	"os"
	"testing"
)

func TestCAIssuesConstrainedLeaves(t *testing.T) {
	dir := t.TempDir()
	ca, err := LoadOrCreateCA(dir, []string{"test", "localhost"})
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca.Cert)

	verify := func(host, dnsName string, ips []net.IP) error {
		crt, _, _, err := ca.Leaf(t.TempDir(), host, ips)
		if err != nil {
			t.Fatal(err)
		}
		c, err := readCert(crt)
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.Verify(x509.VerifyOptions{Roots: roots, DNSName: dnsName})
		return err
	}
	if err := verify("blog.test", "blog.test", nil); err != nil {
		t.Errorf("blog.test should verify: %v", err)
	}
	if err := verify("localhost", "127.0.0.1", []net.IP{net.ParseIP("127.0.0.1")}); err != nil {
		t.Errorf("localhost by IP should verify: %v", err)
	}
	if err := verify("google.com", "google.com", nil); err == nil {
		t.Error("google.com must be rejected by the CA's name constraints")
	}
	if !ca.Permits("a.b.test") || ca.Permits("test.com") {
		t.Error("Permits is wrong")
	}
}

func TestLeafIsReusedAndCAReloads(t *testing.T) {
	dir := t.TempDir()
	ca, err := LoadOrCreateCA(dir, []string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	leafDir := t.TempDir()
	if _, _, changed, _ := ca.Leaf(leafDir, "x.test", nil); !changed {
		t.Error("first issue should report changed")
	}
	again, err := LoadOrCreateCA(dir, []string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	if !again.Cert.Equal(ca.Cert) {
		t.Error("CA should load from disk, not be regenerated")
	}
	if _, _, changed, _ := again.Leaf(leafDir, "x.test", nil); changed {
		t.Error("valid leaf should be reused")
	}

	other, _ := LoadOrCreateCA(t.TempDir(), []string{"test"})
	if _, _, changed, _ := other.Leaf(leafDir, "x.test", nil); !changed {
		t.Error("leaf from a different CA should be reissued")
	}
	if fi, _ := os.Stat(dir + "/ca.key"); fi == nil {
		t.Error("ca.key missing")
	}
}
