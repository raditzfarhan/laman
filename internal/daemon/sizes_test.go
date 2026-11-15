package daemon

import (
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func sizeTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", strings.Repeat("a", 100))
	writeFile(t, dir, "vendor/x/b", strings.Repeat("b", 1000))
	writeFile(t, dir, "node_modules/c", strings.Repeat("c", 10))
	return dir
}

func TestMeasureDir(t *testing.T) {
	got, err := measureDir(sizeTree(t))
	if err != nil {
		t.Fatal(err)
	}
	if want := (dirSizes{Total: 1110, Vendor: 1000, NodeModules: 10}); got != want {
		t.Errorf("measureDir = %+v, want %+v", got, want)
	}
}

func TestDirSizeSkipsJunctions(t *testing.T) {
	dir := sizeTree(t)
	link := filepath.Join(dir, "node_modules", "loop")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, dir).CombinedOutput(); err != nil {
		t.Skipf("mklink /J: %v %s", err, out)
	}
	done := make(chan dirSizes, 1)
	go func() { s, _ := measureDir(dir); done <- s }()
	select {
	case got := <-done:
		if want := (dirSizes{Total: 1110, Vendor: 1000, NodeModules: 10}); got != want {
			t.Errorf("measureDir = %+v, want %+v (junction followed?)", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("measureDir did not finish: it follows junctions")
	}
}

func TestSizeCacheGet(t *testing.T) {
	dir := sizeTree(t)
	var runs atomic.Int32
	release := make(chan struct{})
	old := measure
	measure = func(root string) (dirSizes, error) {
		runs.Add(1)
		<-release
		return measureDir(root)
	}
	t.Cleanup(func() { measure = old })

	var c sizeCache
	if s := c.get(dir); s.State != "measuring" {
		t.Fatalf("first get: state %q, want measuring", s.State)
	}
	c.refresh(dir) // already running: no second measurement
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	var s sizeState
	for time.Now().Before(deadline) {
		if s = c.get(dir); s.State == "done" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s.State != "done" || s.Sizes.Total != 1110 {
		t.Fatalf("state %+v, want done with 1110", s)
	}
	if n := runs.Load(); n != 1 {
		t.Errorf("measured %d times, want 1", n)
	}
	c.forget(dir)
	if s := c.get(dir); s.State != "measuring" {
		t.Errorf("after forget: state %q, want measuring", s.State)
	}
}
