package daemon

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"laman/internal/config"
	"laman/internal/sites"
)

// workerFixture: a daemon with one Laravel site "acme" in a throwaway
// LAMAN_HOME, and workerCommand replaced by cmd (counting its calls).
type workerFixture struct {
	d   *daemon
	dir string

	mu     sync.Mutex
	starts int
}

func newWorkerFixture(t *testing.T, env string, cmd func() *exec.Cmd) *workerFixture {
	t.Helper()
	t.Setenv("LAMAN_HOME", t.TempDir())
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "artisan"), []byte("<?php\n"), 0o644)
	if env != "" {
		os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o644)
	}
	f := &workerFixture{dir: dir}
	f.d = &daemon{cfg: config.Default(), sites: []sites.Site{{Name: "acme", Host: "acme.test", Path: dir}}}

	oldCmd, oldBackoff, oldWindow := workerCommand, workerBackoff, workerWindow
	oldStop, oldReady := workerStopWait, workerReadyWait
	t.Cleanup(func() {
		f.d.stopAllWorkers()
		workerCommand, workerBackoff, workerWindow = oldCmd, oldBackoff, oldWindow
		workerStopWait, workerReadyWait = oldStop, oldReady
	})
	workerCommand = func(w *worker) (*exec.Cmd, string, error) {
		f.mu.Lock()
		f.starts++
		f.mu.Unlock()
		return cmd(), "fake", nil
	}
	return f
}

func (f *workerFixture) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts
}

// eventually polls cond until it holds or the deadline passes.
func eventually(t *testing.T, within time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

func TestRestartDelay(t *testing.T) {
	want := []time.Duration{2, 4, 8, 16, 30, 30, 30}
	for i, w := range want {
		if got := restartDelay(i + 1); got != w*time.Second {
			t.Errorf("restartDelay(%d) = %s, want %s", i+1, got, w*time.Second)
		}
	}
}

func TestWorkerGivesUpAfterQuickCrashes(t *testing.T) {
	f := newWorkerFixture(t, "QUEUE_CONNECTION=sqs\n", func() *exec.Cmd { return shellCmd("echo boom & exit 1") })
	workerBackoff = func(int) time.Duration { return 10 * time.Millisecond }

	if err := f.d.startWorker(workerQueue, "acme.test"); err != nil {
		t.Fatal(err)
	}
	ok := eventually(t, 5*time.Second, func() bool {
		return f.d.workerState(workerQueue, "acme.test").State == workerCrashed
	})
	st := f.d.workerState(workerQueue, "acme.test")
	if !ok {
		t.Fatalf("state = %q after 5s, want crashed (starts %d)", st.State, f.count())
	}
	time.Sleep(100 * time.Millisecond) // a sixth start would show up by now
	if n := f.count(); n != 5 {
		t.Errorf("starts = %d, want 5", n)
	}
	if !strings.Contains(st.Error, "boom") {
		t.Errorf("Error = %q, want the last output lines", st.Error)
	}
	if !strings.Contains(f.d.workerLog(workerQueue, "acme.test"), "boom") {
		t.Errorf("log = %q", f.d.workerLog(workerQueue, "acme.test"))
	}
	if b, err := os.ReadFile(filepath.Join(config.LogDir(), "queue-acme.log")); err != nil || !strings.Contains(string(b), "boom") {
		t.Errorf("log file: %v %q", err, b)
	}
	f.d.stopWorker(workerQueue, "acme.test")
	if st := f.d.workerState(workerQueue, "acme.test"); st.State != workerStopped || st.Error != "" {
		t.Errorf("after stop: %+v, want stopped without the old error", st)
	}
}

func TestStopDuringBackoffDoesNotRestart(t *testing.T) {
	f := newWorkerFixture(t, "QUEUE_CONNECTION=sqs\n", func() *exec.Cmd { return shellCmd("exit 1") })
	workerBackoff = func(int) time.Duration { return 300 * time.Millisecond }

	if err := f.d.startWorker(workerQueue, "acme.test"); err != nil {
		t.Fatal(err)
	}
	w := f.d.worker(workerQueue, "acme.test")
	if !eventually(t, 3*time.Second, func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.timer != nil
	}) {
		t.Fatal("no restart was scheduled after the first crash")
	}
	f.d.stopWorker(workerQueue, "acme.test")
	time.Sleep(600 * time.Millisecond)
	if n := f.count(); n != 1 {
		t.Errorf("starts = %d, want 1", n)
	}
	if st := f.d.workerState(workerQueue, "acme.test"); st.State != workerStopped {
		t.Errorf("state = %q, want stopped", st.State)
	}
}

func TestStopRunningWorker(t *testing.T) {
	f := newWorkerFixture(t, "QUEUE_CONNECTION=sqs\n", func() *exec.Cmd { return shellCmd("ping -n 30 127.0.0.1 >nul") })
	if err := f.d.startWorker(workerQueue, "acme.test"); err != nil {
		t.Fatal(err)
	}
	st := f.d.workerState(workerQueue, "acme.test")
	if st.State != workerRunning || st.PID == 0 {
		t.Fatalf("after start: %+v", st)
	}
	f.d.stopWorker(workerQueue, "acme.test")
	st = f.d.workerState(workerQueue, "acme.test")
	if st.State != workerStopped || st.PID != 0 {
		t.Errorf("after stop: %+v", st)
	}
	if n := f.count(); n != 1 {
		t.Errorf("starts = %d, want 1", n)
	}
}

func TestReverbBusyPortFails(t *testing.T) {
	f := newWorkerFixture(t, "", func() *exec.Cmd { return shellCmd("exit 0") })
	ln, err := net.Listen("tcp", "127.0.0.1:8100")
	if err != nil {
		t.Skipf("port 8100 is taken on this machine: %v", err)
	}
	defer ln.Close()

	err = f.d.startWorker(workerReverb, "acme.test")
	if err == nil || !strings.Contains(err.Error(), "port 8100 is in use") {
		t.Fatalf("err = %v, want port 8100 is in use", err)
	}
	if p := f.d.cfg.ReverbPorts["acme"]; p != 8100 {
		t.Errorf("cfg.ReverbPorts[acme] = %d, want 8100", p)
	}
	saved, _ := config.Load()
	if p := saved.ReverbPorts["acme"]; p != 8100 {
		t.Errorf("saved ReverbPorts[acme] = %d, want 8100", p)
	}
	st := f.d.workerState(workerReverb, "acme.test")
	if st.State != workerCrashed || !strings.Contains(st.Error, "port 8100 is in use") || st.Port != 8100 {
		t.Errorf("status = %+v", st)
	}
	if n := f.count(); n != 0 {
		t.Errorf("starts = %d, want 0", n)
	}
}

func TestQueueWaitsForService(t *testing.T) {
	f := newWorkerFixture(t, "QUEUE_CONNECTION=redis\nREDIS_HOST=127.0.0.1\n", func() *exec.Cmd { return shellCmd("exit 0") })
	if err := f.d.startWorker(workerQueue, "acme.test"); err != nil {
		t.Fatal(err)
	}
	st := f.d.workerState(workerQueue, "acme.test")
	if st.State != workerWaiting || st.Waiting != "Redis is off" {
		t.Errorf("status = %+v", st)
	}
	if n := f.count(); n != 0 {
		t.Errorf("starts = %d, want 0", n)
	}
}

func TestQueueInlineDoesNotStart(t *testing.T) {
	f := newWorkerFixture(t, "QUEUE_CONNECTION=sync\n", func() *exec.Cmd { return shellCmd("exit 0") })
	err := f.d.startWorker(workerQueue, "acme.test")
	if err == nil || !strings.Contains(err.Error(), "Jobs run inline") {
		t.Errorf("err = %v", err)
	}
	if n := f.count(); n != 0 {
		t.Errorf("starts = %d, want 0", n)
	}
}

func TestWorkerStateUnknown(t *testing.T) {
	f := newWorkerFixture(t, "", func() *exec.Cmd { return shellCmd("exit 0") })
	if st := f.d.workerState(workerReverb, "acme.test"); st.State != workerOff {
		t.Errorf("state = %q, want off", st.State)
	}
}

func TestWorkerCrashListResetsAfterLongRun(t *testing.T) {
	f := newWorkerFixture(t, "", func() *exec.Cmd { return shellCmd("exit 0") })
	workerWindow = time.Minute
	workerBackoff = func(int) time.Duration { return time.Hour } // never fires
	w := &worker{kind: workerQueue, site: f.d.sites[0], gen: 1, state: workerRunning}
	f.d.workers = map[string]*worker{workerKey(workerQueue, "acme.test"): w}
	recent := func() []time.Time {
		now := time.Now()
		return []time.Time{now.Add(-4 * time.Second), now.Add(-3 * time.Second), now.Add(-2 * time.Second), now.Add(-time.Second)}
	}

	// Four quick crashes, then one after a run longer than the window: the
	// list starts over and a restart is scheduled.
	w.crashes, w.since = recent(), time.Now().Add(-2*time.Minute)
	f.d.workerExited(w, 1, nil)
	w.mu.Lock()
	n, state, timer := len(w.crashes), w.state, w.timer
	w.mu.Unlock()
	if n != 1 || state != workerStarting || timer == nil {
		t.Errorf("after a long run: %d crashes, state %q, timer %v; want 1, starting, a restart", n, state, timer != nil)
	}
	w.halt()

	// The same four, then a quick fifth: laman gives up.
	w.mu.Lock()
	w.stop, w.state, w.crashes, w.since = false, workerRunning, recent(), time.Now().Add(-time.Second)
	w.mu.Unlock()
	f.d.workerExited(w, 1, nil)
	if st := w.status(); st.State != workerCrashed {
		t.Errorf("after a quick fifth crash: state %q, want crashed", st.State)
	}
}

func TestWorkerCrashNoticedDespiteOrphanChild(t *testing.T) {
	// The parent exits at once; its child keeps the output pipe open for ~2s
	// and would then print. laman must notice the exit and end the child.
	f := newWorkerFixture(t, "QUEUE_CONNECTION=sqs\n", func() *exec.Cmd {
		return shellCmd(`start /b cmd /d /c "ping -n 3 127.0.0.1 >nul & echo orphan-alive" & exit 1`)
	})
	workerBackoff = func(int) time.Duration { return time.Hour }
	if err := f.d.startWorker(workerQueue, "acme.test"); err != nil {
		t.Fatal(err)
	}
	w := f.d.worker(workerQueue, "acme.test")
	if !eventually(t, 1500*time.Millisecond, func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.timer != nil
	}) {
		t.Fatalf("the exit was not noticed while a child held the output open: %+v %q", f.d.workerState(workerQueue, "acme.test"), f.d.workerLog(workerQueue, "acme.test"))
	}
	time.Sleep(3 * time.Second)
	if log := f.d.workerLog(workerQueue, "acme.test"); strings.Contains(log, "orphan-alive") {
		t.Errorf("the orphaned child kept running: %q", log)
	}
}

func TestRestartWorkerStillStopping(t *testing.T) {
	f := newWorkerFixture(t, "", func() *exec.Cmd { return shellCmd("exit 0") })
	workerStopWait = 50 * time.Millisecond
	// A run whose exit never arrives (no such process, done never closed).
	w := &worker{kind: workerQueue, site: f.d.sites[0], gen: 1, state: workerRunning, pid: 0x7ffffff0, done: make(chan struct{})}
	f.d.workers = map[string]*worker{workerKey(workerQueue, "acme.test"): w}
	err := f.d.restartWorker(workerQueue, "acme.test")
	if err == nil || !strings.Contains(err.Error(), "queue for acme.test is still stopping") {
		t.Errorf("err = %v", err)
	}
	if n := f.count(); n != 0 {
		t.Errorf("starts = %d, want 0", n)
	}
	w.mu.Lock()
	w.pid = 0 // nothing for cleanup to kill
	w.mu.Unlock()
}

func TestReverbPortTimeout(t *testing.T) {
	f := newWorkerFixture(t, "", func() *exec.Cmd { return shellCmd("ping -n 30 127.0.0.1 >nul") })
	if portOpen(8100) {
		t.Skip("port 8100 is taken on this machine")
	}
	workerReadyWait = 300 * time.Millisecond
	workerBackoff = func(int) time.Duration { return 10 * time.Millisecond }
	if err := f.d.startWorker(workerReverb, "acme.test"); err != nil {
		t.Fatal(err)
	}
	ok := eventually(t, 5*time.Second, func() bool {
		st := f.d.workerState(workerReverb, "acme.test")
		return st.State == workerCrashed && st.PID == 0
	})
	st := f.d.workerState(workerReverb, "acme.test")
	if !ok || st.Error != "Reverb did not open port 8100 within 300ms" {
		t.Fatalf("status = %+v", st)
	}
	time.Sleep(200 * time.Millisecond)
	if n := f.count(); n != 1 {
		t.Errorf("starts = %d, want 1 (no restart after laman ended it)", n)
	}
}
