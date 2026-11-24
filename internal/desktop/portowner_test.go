//go:build windows

package desktop

import (
	"net"
	"os"
	"strings"
	"testing"
)

func TestPortOwnerFindsListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	pid, path := PortOwner(port)
	if int(pid) != os.Getpid() {
		t.Fatalf("pid = %d, want %d (path %q)", pid, os.Getpid(), path)
	}
	exe, _ := os.Executable()
	if !strings.EqualFold(path, exe) {
		t.Fatalf("path = %q, want %q", path, exe)
	}
}

func TestDescribeOwner(t *testing.T) {
	cases := map[string]struct {
		pid  uint32
		path string
	}{
		"inside WSL":       {28448, `C:\Program Files\WSL\wslrelay.exe`},
		"Docker container": {100, `C:\Program Files\Docker\Docker\resources\com.docker.backend.exe`},
		"Quit Laragon":     {200, `C:\laragon\bin\nginx\nginx.exe`},
		"IIS":              {4, ""},
		"another program":  {300, ""},
	}
	for want, c := range cases {
		if got := describeOwner(c.pid, c.path); !strings.Contains(got, want) {
			t.Errorf("describeOwner(%d, %q) = %q, want it to mention %q", c.pid, c.path, got, want)
		}
	}
}
