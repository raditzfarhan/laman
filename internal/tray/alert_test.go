package tray

import (
	"errors"
	"strings"
	"testing"
)

func TestAlertText(t *testing.T) {
	out := "laman: laman failed to start:\nlaman: port 80 for HTTP (httpPort) is already in use by wslrelay.exe (pid 1).\nlaman: port 80 for HTTP (httpPort) is already in use by wslrelay.exe (pid 1).\n"
	got := alertText(out, errors.New("exit status 1"))
	if strings.Contains(got, "laman:") || strings.Contains(got, "failed to start") {
		t.Errorf("prefixes or header kept: %q", got)
	}
	if strings.Count(got, "port 80") != 1 {
		t.Errorf("repeat not dropped: %q", got)
	}
	if !strings.Contains(got, "laman.log") {
		t.Errorf("no log path: %q", got)
	}
	if got := alertText("", errors.New("exit status 1")); got != "exit status 1" {
		t.Errorf("empty output = %q", got)
	}
}
