// Package certs runs laman's local certificate authority. The CA is name
// constrained to the dev TLD and localhost, so a leaked CA key cannot be used
// to impersonate real websites.
package certs

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	leafValidity = 397 * 24 * time.Hour
	renewBefore  = 30 * 24 * time.Hour
)

type CA struct {
	Cert     *x509.Certificate
	Key      crypto.Signer
	CertPath string
}

// LoadOrCreateCA loads the CA from dir, creating it (permitted for the given
// DNS domains plus loopback IPs) if it does not exist yet.
func LoadOrCreateCA(dir string, domains []string) (*CA, error) {
	crtPath, keyPath := filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key")
	if _, err := os.Stat(crtPath); err == nil {
		return loadCA(crtPath, keyPath)
	}
	if len(domains) == 0 {
		return nil, errors.New("refusing to create a CA without name constraints")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	owner := ownerName()
	tmpl := &x509.Certificate{
		SerialNumber:                serial(),
		Subject:                     pkix.Name{Organization: []string{"laman local CA"}, OrganizationalUnit: []string{owner}, CommonName: "laman local CA (" + owner + ")"},
		NotBefore:                   time.Now().Add(-time.Hour),
		NotAfter:                    time.Now().AddDate(10, 0, 0),
		IsCA:                        true,
		BasicConstraintsValid:       true,
		MaxPathLenZero:              true,
		KeyUsage:                    x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         domains,
		PermittedIPRanges: []*net.IPNet{
			{IP: net.IPv4(127, 0, 0, 0).To4(), Mask: net.CIDRMask(8, 32)},
			{IP: net.IPv6loopback, Mask: net.CIDRMask(128, 128)},
		},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	if err := writeKey(keyPath, key); err != nil {
		return nil, err
	}
	if err := writePEM(crtPath, "CERTIFICATE", der, 0o644); err != nil {
		return nil, err
	}
	return loadCA(crtPath, keyPath)
}

// LoadCA loads an existing CA; it fails with fs.ErrNotExist if there is none.
func LoadCA(dir string) (*CA, error) {
	return loadCA(filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key"))
}

func loadCA(crtPath, keyPath string) (*CA, error) {
	cert, err := readCert(crtPath)
	if err != nil {
		return nil, fmt.Errorf("CA certificate: %w", err)
	}
	b, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("CA key: %w", err)
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, errors.New("CA key: not PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("CA key: %w", err)
	}
	signer, ok := k.(crypto.Signer)
	if !ok {
		return nil, errors.New("CA key: unsupported type")
	}
	return &CA{Cert: cert, Key: signer, CertPath: crtPath}, nil
}

// Permits reports whether the CA's name constraints allow host.
func (ca *CA) Permits(host string) bool {
	for _, d := range ca.Cert.PermittedDNSDomains {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// Leaf returns a certificate for host stored in dir, issuing a new one when
// it is missing, not signed by this CA, lacks a name, or expires soon.
func (ca *CA) Leaf(dir, host string, ips []net.IP) (crtPath, keyPath string, changed bool, err error) {
	crtPath, keyPath = filepath.Join(dir, host+".crt"), filepath.Join(dir, host+".key")
	if c, err := readCert(crtPath); err == nil && ca.stillGood(c, host, ips) {
		if _, err := os.Stat(keyPath); err == nil {
			return crtPath, keyPath, false, nil
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", false, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", false, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{Organization: []string{"laman"}, CommonName: host},
		DNSNames:     []string{host},
		IPAddresses:  ips,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(leafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		return "", "", false, err
	}
	if err := writeKey(keyPath, key); err != nil {
		return "", "", false, err
	}
	if err := writePEM(crtPath, "CERTIFICATE", der, 0o644); err != nil {
		return "", "", false, err
	}
	return crtPath, keyPath, true, nil
}

func (ca *CA) stillGood(c *x509.Certificate, host string, ips []net.IP) bool {
	if c.CheckSignatureFrom(ca.Cert) != nil || time.Until(c.NotAfter) < renewBefore {
		return false
	}
	if !slices.Contains(c.DNSNames, host) || len(c.IPAddresses) != len(ips) {
		return false
	}
	for i := range ips {
		if !c.IPAddresses[i].Equal(ips[i]) {
			return false
		}
	}
	return true
}

// Trusted reports whether the operating system trusts the CA.
func (ca *CA) Trusted() bool {
	_, err := ca.Cert.Verify(x509.VerifyOptions{KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
	return err == nil
}

func readCert(path string) (*x509.Certificate, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("not a PEM certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

func writeKey(path string, key *ecdsa.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	return writePEM(path, "PRIVATE KEY", der, 0o600)
}

func writePEM(path, typ string, der []byte, mode os.FileMode) error {
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), mode)
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	return n
}

func ownerName() string {
	u, _ := user.Current()
	h, _ := os.Hostname()
	name := "user"
	if u != nil {
		name = u.Username
		if i := strings.LastIndex(name, `\`); i >= 0 {
			name = name[i+1:]
		}
	}
	return name + "@" + h
}
