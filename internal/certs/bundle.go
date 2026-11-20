package certs

import (
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const (
	bundleBegin = "# BEGIN laman local CA"
	bundleEnd   = "# END laman local CA"
)

// AddToBundle makes the PEM bundle at path (PHP's cacert.pem) trust this CA
// by keeping one marked block with the CA certificate (never the key) at the
// end of it, replacing any block from an earlier CA. A missing bundle is left
// alone. The file is rewritten atomically, and only when its content changes.
func (ca *CA) AddToBundle(bundle string) (changed bool, err error) {
	raw, err := os.ReadFile(bundle)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	old := string(raw)
	crlf := strings.Contains(old, "\r\n")
	s := strings.ReplaceAll(old, "\r\n", "\n")

	if i := strings.Index(s, bundleBegin); i >= 0 {
		end := len(s)
		if j := strings.Index(s[i:], bundleEnd); j >= 0 {
			end = i + j + len(bundleEnd)
			if end < len(s) && s[end] == '\n' {
				end++
			}
		}
		start := i
		if start > 0 && s[start-1] == '\n' {
			start--
		}
		s = s[:start] + s[end:]
	}

	certPEM := strings.TrimSpace(string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Cert.Raw})))
	s += "\n" + bundleBegin + "\n# laman's local development CA (only signs .<tld> and localhost)\n" + certPEM + "\n" + bundleEnd + "\n"
	if crlf {
		s = strings.ReplaceAll(s, "\n", "\r\n")
	}
	if s == old {
		return false, nil
	}

	mode := os.FileMode(0o644)
	if fi, err := os.Stat(bundle); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(bundle), ".cacert-*.tmp")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(s); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	os.Chmod(tmp.Name(), mode)
	if err := os.Rename(tmp.Name(), bundle); err != nil {
		return false, err
	}
	return true, nil
}
