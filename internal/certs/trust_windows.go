//go:build windows

package certs

import (
	"fmt"
	"os/exec"
	"strings"
)

// Install adds the CA to the current user's Trusted Root store. Windows shows
// a confirmation dialog; no admin rights are needed.
func (ca *CA) Install() error {
	out, err := exec.Command("certutil", "-user", "-addstore", "Root", ca.CertPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("certutil: %v: %s", err, lastLine(out))
	}
	return nil
}

// Uninstall removes the CA from the current user's Trusted Root store.
func (ca *CA) Uninstall() error {
	out, err := exec.Command("certutil", "-user", "-delstore", "Root", ca.Cert.SerialNumber.Text(16)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("certutil: %v: %s", err, lastLine(out))
	}
	return nil
}

func lastLine(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
