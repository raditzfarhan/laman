//go:build windows

package desktop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProcessesUnder(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	got := ProcessesUnder(strings.ToUpper(filepath.Dir(exe)))
	found := false
	for _, p := range got {
		if strings.EqualFold(p, exe) {
			found = true
		}
	}
	if !found {
		t.Errorf("ProcessesUnder(test dir) = %v, want it to include %s", got, exe)
	}
	if got := ProcessesUnder(t.TempDir()); len(got) != 0 {
		t.Errorf("ProcessesUnder(empty dir) = %v, want none", got)
	}
}
