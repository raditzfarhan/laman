package main

import (
	"net"
	"testing"

	"laman/internal/config"
)

// The hooks must not reach a real laman: LAMAN_HOME is a temp folder whose
// config points at a port nothing listens on.
func TestAppWorkerHooksNoDaemon(t *testing.T) {
	t.Setenv("LAMAN_HOME", t.TempDir())
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	cfg := config.Default()
	cfg.ControlPort = port
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	stop, start := appWorkerHooks("dbzuper")
	if err := stop(); err != nil {
		t.Errorf("stop without a daemon: %v", err)
	}
	start()
}
