package daemon

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"laman/internal/config"
	"laman/internal/install"
	"laman/internal/redisq"
	"laman/internal/sites"

	"golang.org/x/sys/windows"
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
	// Both switched on: startWorker refuses a worker that is off.
	f.d.cfg.QueueWorkers, f.d.cfg.Reverb = map[string]bool{"acme": true}, map[string]bool{"acme": true}

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

// setSwitch switches a worker of acme on or off in the daemon's config only.
func (f *workerFixture) setSwitch(kind string, on bool) {
	f.d.mu.Lock()
	defer f.d.mu.Unlock()
	m := f.d.cfg.QueueWorkers
	if kind == workerReverb {
		m = f.d.cfg.Reverb
	}
	if on {
		m["acme"] = true
	} else {
		delete(m, "acme")
	}
}

func TestStartWorkerNeedsSwitchOn(t *testing.T) {
	f := newWorkerFixture(t, "QUEUE_CONNECTION=sqs\n", func() *exec.Cmd { return shellCmd("ping -n 30 127.0.0.1 >nul") })
	f.setSwitch(workerQueue, false)
	err := f.d.startWorker(workerQueue, "acme.test")
	if err == nil || !strings.Contains(err.Error(), "the queue worker is off for acme.test") {
		t.Errorf("start while off: err = %v", err)
	}
	if st := f.d.workerState(workerQueue, "acme.test"); st.State != workerOff || f.count() != 0 {
		t.Errorf("start while off: %+v, starts %d", st, f.count())
	}

	// On, running; then switched off (as the switch route does): a restart,
	// from any path, must not bring it back.
	f.setSwitch(workerQueue, true)
	if err := f.d.startWorker(workerQueue, "acme.test"); err != nil {
		t.Fatal(err)
	}
	f.setSwitch(workerQueue, false)
	f.d.switchWorkerOff(workerQueue, "acme.test")
	if err := f.d.restartWorker(workerQueue, "acme.test"); err == nil {
		t.Errorf("restartWorker of a switched-off worker: no error")
	}
	f.d.restartSiteWorkers("acme")
	f.d.workersServiceChanged()
	if st := f.d.workerState(workerQueue, "acme.test"); st.State != workerOff || st.PID != 0 || f.count() != 1 {
		t.Errorf("after restarts of a switched-off worker: %+v, starts %d", st, f.count())
	}
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
	f.d.workerExited(w, 1, nil, nil)
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
	f.d.workerExited(w, 1, nil, nil)
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

// processAlive reports whether a process with the id is still running.
func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	return windows.GetExitCodeProcess(h, &code) == nil && code == 259 // STILL_ACTIVE
}

func setSites(d *daemon, list ...sites.Site) {
	d.mu.Lock()
	d.sites = list
	d.mu.Unlock()
}

func TestWorkersFollowSites(t *testing.T) {
	f := newWorkerFixture(t, "QUEUE_CONNECTION=sqs\n", func() *exec.Cmd { return shellCmd("ping -n 30 127.0.0.1 >nul") })
	f.d.cfg.QueueWorkers["acme"] = true
	acme := f.d.sites[0]
	if err := f.d.startWorker(workerQueue, "acme.test"); err != nil {
		t.Fatal(err)
	}
	first := f.d.workerState(workerQueue, "acme.test")
	if first.State != workerRunning || first.PID == 0 {
		t.Fatalf("after start: %+v", first)
	}

	setSites(f.d) // the folder is gone
	f.d.syncWorkers()
	if st := f.d.workerState(workerQueue, "acme.test"); st.State != workerOff || st.PID != 0 {
		t.Errorf("after the site went: %+v", st)
	}
	if processAlive(first.PID) {
		t.Errorf("pid %d still runs after the site went", first.PID)
	}
	f.d.syncWorkers() // nothing changed: nothing happens
	if n := f.count(); n != 1 {
		t.Errorf("starts = %d, want 1", n)
	}

	setSites(f.d, acme) // it is back
	f.d.syncWorkers()
	back := f.d.workerState(workerQueue, "acme.test")
	if back.State != workerRunning || back.PID == 0 || back.PID == first.PID {
		t.Fatalf("after the site came back: %+v", back)
	}

	if err := f.d.restartSiteWorkers("acme"); err != nil {
		t.Fatal(err)
	}
	again := f.d.workerState(workerQueue, "acme.test")
	if again.State != workerRunning || again.PID == 0 || again.PID == back.PID {
		t.Errorf("after restartSiteWorkers: %+v (was pid %d)", again, back.PID)
	}
	if processAlive(back.PID) {
		t.Errorf("old pid %d still runs after the restart", back.PID)
	}
	if n := f.count(); n != 3 {
		t.Errorf("starts = %d, want 3", n)
	}
}

func TestGoneSiteSwitchedOffStaysOff(t *testing.T) {
	f := newWorkerFixture(t, "QUEUE_CONNECTION=sqs\n", func() *exec.Cmd { return shellCmd("ping -n 30 127.0.0.1 >nul") })
	acme := f.d.sites[0]
	if err := f.d.startWorker(workerQueue, "acme.test"); err != nil {
		t.Fatal(err)
	}
	setSites(f.d)
	f.d.syncWorkers()
	f.setSwitch(workerQueue, false) // switched off while the site was gone
	setSites(f.d, acme)             // back, but its switch is off
	f.d.syncWorkers()
	if st := f.d.workerState(workerQueue, "acme.test"); st.State != workerOff {
		t.Errorf("state = %+v, want off", st)
	}
	if n := f.count(); n != 1 {
		t.Errorf("starts = %d, want 1", n)
	}
}

func TestSyncWorkersRestartsOnPHPChange(t *testing.T) {
	f := newWorkerFixture(t, "QUEUE_CONNECTION=sqs\n", func() *exec.Cmd { return shellCmd("ping -n 30 127.0.0.1 >nul") })
	f.d.cfg.QueueWorkers["acme"] = true
	acme := f.d.sites[0]
	acme.PHP = "8.3.10"
	setSites(f.d, acme)
	if err := f.d.startWorker(workerQueue, "acme.test"); err != nil {
		t.Fatal(err)
	}
	first := f.d.workerState(workerQueue, "acme.test")

	f.d.syncWorkers() // same PHP: left alone
	if st := f.d.workerState(workerQueue, "acme.test"); st.PID != first.PID || f.count() != 1 {
		t.Fatalf("unchanged PHP restarted the worker: %+v, starts %d", st, f.count())
	}

	acme.PHP = "8.4.1"
	setSites(f.d, acme)
	f.d.syncWorkers()
	st := f.d.workerState(workerQueue, "acme.test")
	if st.State != workerRunning || st.PID == first.PID || f.count() != 2 {
		t.Errorf("after the PHP change: %+v, starts %d", st, f.count())
	}
}

func TestSyncWorkersLeavesStoppedAlone(t *testing.T) {
	f := newWorkerFixture(t, "QUEUE_CONNECTION=sqs\n", func() *exec.Cmd { return shellCmd("ping -n 30 127.0.0.1 >nul") })
	f.d.cfg.QueueWorkers["acme"] = true
	f.setSwitch(workerReverb, false)
	acme := f.d.sites[0]
	acme.PHP = "8.3.10"
	setSites(f.d, acme)
	f.d.startAutoWorkers()
	f.d.stopWorker(workerQueue, "acme.test")
	acme.PHP = "8.4.1"
	setSites(f.d, acme)
	f.d.syncWorkers()
	if st := f.d.workerState(workerQueue, "acme.test"); st.State != workerStopped {
		t.Errorf("state = %+v, want stopped", st)
	}
	if n := f.count(); n != 1 {
		t.Errorf("starts = %d, want 1", n)
	}
}

func TestStartAutoWorkers(t *testing.T) {
	f := newWorkerFixture(t, "QUEUE_CONNECTION=sqs\n", func() *exec.Cmd { return shellCmd("ping -n 30 127.0.0.1 >nul") })
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, "artisan"), []byte("<?php\n"), 0o644)
	os.WriteFile(filepath.Join(other, ".env"), []byte("QUEUE_CONNECTION=sqs\n"), 0o644)
	setSites(f.d, f.d.sites[0], sites.Site{Name: "beta", Host: "beta.test", Path: other})
	f.d.cfg.QueueWorkers["acme"] = true
	delete(f.d.cfg.Reverb, "acme")

	f.d.syncWorkers() // before the start with laman: nothing starts
	if n := f.count(); n != 0 {
		t.Fatalf("starts before startAutoWorkers = %d, want 0", n)
	}
	f.d.startAutoWorkers()
	if st := f.d.workerState(workerQueue, "acme.test"); st.State != workerRunning {
		t.Errorf("acme queue: %+v", st)
	}
	for _, c := range []struct{ kind, host string }{{workerReverb, "acme.test"}, {workerQueue, "beta.test"}, {workerReverb, "beta.test"}} {
		if st := f.d.workerState(c.kind, c.host); st.State != workerOff {
			t.Errorf("%s %s: %+v, want off", c.kind, c.host, st)
		}
	}
	if n := f.count(); n != 1 {
		t.Errorf("starts = %d, want 1", n)
	}

	// Switched on later (config reload): the next sync starts it.
	f.d.cfg.QueueWorkers["beta"] = true
	f.d.syncWorkers()
	if st := f.d.workerState(workerQueue, "beta.test"); st.State != workerRunning {
		t.Errorf("beta queue after sync: %+v", st)
	}
	if n := f.count(); n != 2 {
		t.Errorf("starts = %d, want 2", n)
	}
}

func TestWorkersServiceChanged(t *testing.T) {
	f := newWorkerFixture(t, "QUEUE_CONNECTION=redis\nREDIS_HOST=127.0.0.1\n", func() *exec.Cmd { return shellCmd("ping -n 30 127.0.0.1 >nul") })
	if err := f.d.startWorker(workerQueue, "acme.test"); err != nil {
		t.Fatal(err)
	}
	if st := f.d.workerState(workerQueue, "acme.test"); st.State != workerWaiting {
		t.Fatalf("before: %+v", st)
	}
	f.d.workersServiceChanged() // Redis still off
	if st := f.d.workerState(workerQueue, "acme.test"); st.State != workerWaiting || f.count() != 0 {
		t.Fatalf("with Redis off: %+v, starts %d", st, f.count())
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0") // Redis answering
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	f.d.mu.Lock()
	f.d.cfg.RedisPort = ln.Addr().(*net.TCPAddr).Port
	f.d.handles = map[string]*handle{"redis": {}}
	f.d.mu.Unlock()
	f.d.workersServiceChanged()
	if st := f.d.workerState(workerQueue, "acme.test"); st.State != workerRunning || f.count() != 1 {
		t.Errorf("after Redis came up: %+v, starts %d", st, f.count())
	}
}

func TestQueueWaitsForServicePort(t *testing.T) {
	f := newWorkerFixture(t, "QUEUE_CONNECTION=redis\nREDIS_HOST=127.0.0.1\n", func() *exec.Cmd { return shellCmd("ping -n 30 127.0.0.1 >nul") })
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()
	h := &handle{done: make(chan struct{})}
	t.Cleanup(func() { close(h.done) })
	f.d.mu.Lock()
	f.d.cfg.RedisPort = port
	f.d.handles = map[string]*handle{"redis": h} // spawned, not answering yet
	f.d.mu.Unlock()

	if err := f.d.startWorker(workerQueue, "acme.test"); err != nil {
		t.Fatal(err)
	}
	st := f.d.workerState(workerQueue, "acme.test")
	if st.State != workerWaiting || st.Waiting != "Waiting for Redis to start" || f.count() != 0 {
		t.Fatalf("port closed: %+v, starts %d", st, f.count())
	}
	f.d.workersServiceChanged() // still closed: still waiting
	if st := f.d.workerState(workerQueue, "acme.test"); st.State != workerWaiting || f.count() != 0 {
		t.Fatalf("port still closed: %+v, starts %d", st, f.count())
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Skipf("port %d was taken meanwhile: %v", port, err)
	}
	defer ln.Close()
	// The port watcher started by the wait notices on its own.
	ok := eventually(t, 5*time.Second, func() bool { return f.d.workerState(workerQueue, "acme.test").State == workerRunning })
	if !ok || f.count() != 1 {
		t.Errorf("after the port opened: %+v, starts %d", f.d.workerState(workerQueue, "acme.test"), f.count())
	}
	f.d.workersServiceChanged() // running: no second start
	if n := f.count(); n != 1 {
		t.Errorf("starts = %d, want 1", n)
	}
}

func TestReverbPorts(t *testing.T) {
	d := &daemon{cfg: config.Default(), sites: []sites.Site{
		{Name: "acme", Host: "acme.test"}, {Name: "blog", Host: "blog.test"}, {Name: "shop", Host: "shop.test"}}}
	d.cfg.Reverb = map[string]bool{"acme": true, "blog": true}
	d.cfg.ReverbPorts = map[string]int{"acme": 8101, "blog": 0, "shop": 8103}
	got := d.reverbPorts()
	if len(got) != 1 || got["acme.test"] != 8101 {
		t.Fatalf("reverbPorts = %v, want only acme.test:8101", got)
	}
}

// routeFixture: a worker fixture with the worker routes on a mux, Caddy
// refreshes counted instead of run.
func routeFixture(t *testing.T, env string) (*workerFixture, *http.ServeMux, *int) {
	t.Helper()
	f := newWorkerFixture(t, env, func() *exec.Cmd { return shellCmd("ping -n 30 127.0.0.1 >nul") })
	// Off: the routes switch them.
	f.d.cfg.QueueWorkers, f.d.cfg.Reverb = map[string]bool{}, map[string]bool{}
	caddy := new(int)
	old := workersCaddyChanged
	t.Cleanup(func() { workersCaddyChanged = old })
	workersCaddyChanged = func(*daemon) { *caddy++ }
	mux := http.NewServeMux()
	f.d.workerRoutes(mux)
	return f, mux, caddy
}

func TestWorkerSwitchRoute(t *testing.T) {
	f, mux, caddy := routeFixture(t, "APP_KEY=x\nQUEUE_CONNECTION=sqs\n")

	rec := call(mux, "POST", "/api/workers/queue/switch", `{"site":"acme.test","on":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("queue on: %d %s", rec.Code, rec.Body)
	}
	var st WorkerStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil || st.State != workerRunning {
		t.Errorf("queue on reply = %+v, %v", st, err)
	}
	if cfg, _ := config.Load(); !cfg.QueueWorkers["acme"] {
		t.Errorf("queue switch not saved: %v", cfg.QueueWorkers)
	}
	var list []WorkerStatus
	json.Unmarshal(call(mux, "GET", "/api/workers", "").Body.Bytes(), &list)
	if len(list) != 1 || list[0].Kind != workerQueue || list[0].Host != "acme.test" {
		t.Errorf("GET /api/workers = %+v", list)
	}
	if *caddy != 0 {
		t.Errorf("queue switch refreshed Caddy %d times", *caddy)
	}

	rec = call(mux, "POST", "/api/workers/queue/switch", `{"site":"acme","on":false}`)
	st = WorkerStatus{}
	json.Unmarshal(rec.Body.Bytes(), &st)
	if rec.Code != http.StatusOK || st.State != workerOff {
		t.Errorf("queue off: %d %+v", rec.Code, st)
	}
	if got := f.d.workerState(workerQueue, "acme.test").State; got != workerOff {
		t.Errorf("state after off = %q", got)
	}
	if cfg, _ := config.Load(); cfg.QueueWorkers["acme"] {
		t.Errorf("queue still on in config")
	}
	if body := call(mux, "GET", "/api/workers", "").Body.String(); strings.TrimSpace(body) != "[]" {
		t.Errorf("GET /api/workers after off = %s", body)
	}

	// Reverb: a port is assigned and saved on first on, and Caddy follows.
	rec = call(mux, "POST", "/api/workers/reverb/switch", `{"site":"acme.test","on":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("reverb on: %d %s", rec.Code, rec.Body)
	}
	cfg, _ := config.Load()
	if p := cfg.ReverbPorts["acme"]; !cfg.Reverb["acme"] || p != config.ReverbFirstPort {
		t.Errorf("reverb config = %v %v", cfg.Reverb, cfg.ReverbPorts)
	}
	if *caddy != 1 {
		t.Errorf("Caddy refreshes = %d, want 1", *caddy)
	}
	call(mux, "POST", "/api/workers/reverb/switch", `{"site":"acme.test","on":false}`)
	cfg, _ = config.Load()
	if cfg.Reverb["acme"] || cfg.ReverbPorts["acme"] != config.ReverbFirstPort || *caddy != 2 {
		t.Errorf("reverb off: %v %v, caddy %d (the port stays assigned)", cfg.Reverb, cfg.ReverbPorts, *caddy)
	}
}

func TestWorkerSwitchRefused(t *testing.T) {
	_, mux, _ := routeFixture(t, "APP_KEY=x\nQUEUE_CONNECTION=sync\n")
	rec := call(mux, "POST", "/api/workers/queue/switch", `{"site":"acme.test","on":true}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Jobs run inline (QUEUE_CONNECTION=sync)") {
		t.Errorf("inline queue on: %d %s", rec.Code, rec.Body)
	}
	if cfg, _ := config.Load(); cfg.QueueWorkers["acme"] {
		t.Errorf("inline queue was saved on")
	}
	if rec := call(mux, "POST", "/api/workers/nope/switch", `{"site":"acme.test","on":true}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown kind: %d", rec.Code)
	}
	if rec := call(mux, "POST", "/api/workers/reverb/switch", `{"site":"gone.test","on":true}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown site: %d", rec.Code)
	}
}

func TestWorkerSwitchNotLaravel(t *testing.T) {
	f, mux, _ := routeFixture(t, "")
	os.Remove(filepath.Join(f.dir, "artisan"))
	rec := call(mux, "POST", "/api/workers/reverb/switch", `{"site":"acme.test","on":true}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "not a Laravel app") {
		t.Errorf("non-Laravel on: %d %s", rec.Code, rec.Body)
	}
	if cfg, _ := config.Load(); cfg.Reverb["acme"] {
		t.Errorf("saved on for a non-Laravel site")
	}
}

func TestWorkerRestartAndLogRoutes(t *testing.T) {
	f, mux, _ := routeFixture(t, "APP_KEY=x\nQUEUE_CONNECTION=sqs\n")
	if rec := call(mux, "POST", "/api/workers/queue/restart", `{"site":"acme.test"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("restart while off: %d %s", rec.Code, rec.Body)
	}
	call(mux, "POST", "/api/workers/queue/switch", `{"site":"acme.test","on":true}`)
	rec := call(mux, "POST", "/api/workers/queue/restart", `{"site":"acme.test"}`)
	var st WorkerStatus
	json.Unmarshal(rec.Body.Bytes(), &st)
	if rec.Code != http.StatusOK || st.State != workerRunning || f.count() != 2 {
		t.Errorf("restart: %d %+v, starts %d", rec.Code, st, f.count())
	}
	f.d.worker(workerQueue, "acme.test").addLine("hello from the worker")
	rec = call(mux, "GET", "/api/workers/queue/log?site=acme.test", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "hello from the worker") ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("log: %d %q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
}

// reverbOn switches Reverb on for acme in the daemon's config, port 8101.
func reverbOn(f *workerFixture) {
	f.d.cfg.Reverb["acme"] = true
	f.d.cfg.ReverbPorts = map[string]int{"acme": 8101}
}

func TestReverbEnvRouteNeedsKey(t *testing.T) {
	const env = "APP_KEY=x\r\nBROADCAST_CONNECTION=log\r\n"
	f, mux, _ := routeFixture(t, env)
	reverbOn(f)
	rec := call(mux, "POST", "/api/reverb/env", `{"site":"acme.test"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "run php artisan reverb:install first") {
		t.Errorf("no key: %d %s", rec.Code, rec.Body)
	}
	if b, _ := os.ReadFile(filepath.Join(f.dir, ".env")); string(b) != env {
		t.Errorf(".env changed: %q", b)
	}
	if exists(envBackupDir("acme")) {
		t.Errorf("backup written")
	}
}

func TestReverbEnvRouteNeedsReverbOn(t *testing.T) {
	_, mux, _ := routeFixture(t, "APP_KEY=x\nREVERB_APP_KEY=abc\n")
	if rec := call(mux, "POST", "/api/reverb/env", `{"site":"acme.test"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("reverb off: %d %s", rec.Code, rec.Body)
	}
}

func TestReverbEnvRouteWrites(t *testing.T) {
	f, mux, _ := routeFixture(t, "APP_KEY=x\nQUEUE_CONNECTION=sqs\nREVERB_APP_KEY=abc\nREVERB_PORT=443\n")
	reverbOn(f)
	f.d.cfg.HTTPSPort = 8443
	f.d.certHosts = map[string]bool{"acme.test": true}
	f.d.cfg.QueueWorkers["acme"] = true
	if err := f.d.startWorker(workerQueue, "acme.test"); err != nil || f.count() != 1 {
		t.Fatalf("queue worker: %v, starts %d", err, f.count())
	}

	rec := call(mux, "POST", "/api/reverb/env", `{"site":"acme.test"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("env: %d %s", rec.Code, rec.Body)
	}
	var reply struct {
		Changed      []string `json:"changed"`
		ConfigCached bool     `json:"configCached"`
		Backup       string   `json:"backup"`
	}
	json.Unmarshal(rec.Body.Bytes(), &reply)
	if len(reply.Changed) != 9 || reply.ConfigCached {
		t.Errorf("reply = %+v", reply)
	}
	if filepath.Dir(reply.Backup) != filepath.Join(config.DataDir(), "env-backups", "acme") || filepath.Ext(reply.Backup) != ".env" {
		t.Errorf("reply backup = %q, want a file under env-backups/acme", reply.Backup)
	}
	if b, _ := os.ReadFile(reply.Backup); !strings.Contains(string(b), "REVERB_PORT=443\n") {
		t.Errorf("backup = %q, want the .env before the write", b)
	}
	// Nothing new in the project besides .env itself.
	if ents, _ := os.ReadDir(f.dir); len(ents) != 2 { // artisan, .env
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Errorf("project folder holds %v, want only artisan and .env", names)
	}
	if n := f.count(); n != 2 {
		t.Errorf("starts after a change = %d, want 2 (the running queue worker restarted)", n)
	}
	b, _ := os.ReadFile(filepath.Join(f.dir, ".env"))
	for _, want := range []string{"REVERB_PORT=8101\n", "REVERB_SCHEME=http\n", "REVERB_HOST=127.0.0.1\n", "REVERB_SERVER_PORT=8101\n", "VITE_REVERB_PORT=8443\n", "VITE_REVERB_SCHEME=https\n", "VITE_REVERB_HOST=acme.test\n", "REVERB_APP_KEY=abc\n"} {
		if !strings.Contains(string(b), want) {
			t.Errorf(".env has no %q:\n%s", want, b)
		}
	}

	// Again: nothing to change; a cached config is reported.
	writeFile(t, f.dir, "bootstrap/cache/config.php", "<?php return [];")
	rec = call(mux, "POST", "/api/reverb/env", `{"site":"acme.test"}`)
	if body := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || body != `{"changed":[],"configCached":true}` {
		t.Errorf("second run: %d %s", rec.Code, body)
	}
	if n := f.count(); n != 2 {
		t.Errorf("starts after a no-change run = %d, want 2 (no restart)", n)
	}
}

func TestSiteEndpoint(t *testing.T) {
	d := &daemon{cfg: config.Default()}
	d.cfg.HTTPPort, d.cfg.HTTPSPort = 8080, 8443
	d.certHosts = map[string]bool{"acme.test": true}
	if s, p := d.siteEndpoint("acme.test"); s != "https" || p != 8443 {
		t.Errorf("with cert: %s %d", s, p)
	}
	if s, p := d.siteEndpoint("blog.test"); s != "http" || p != 8080 {
		t.Errorf("without cert: %s %d", s, p)
	}
	d.cfg.HTTPSPort = 0
	if s, p := d.siteEndpoint("acme.test"); s != "http" || p != 8080 {
		t.Errorf("HTTPS off: %s %d", s, p)
	}
}

func TestQueueRowsNoWorker(t *testing.T) {
	f := newWorkerFixture(t, "APP_KEY=x\nQUEUE_CONNECTION=sqs\n", func() *exec.Cmd { return shellCmd("ping -n 30 127.0.0.1 >nul") })
	rows := []queueRow{
		{Queue: redisq.Queue{Name: "default", Waiting: 3}, Site: "acme.test"},
		{Queue: redisq.Queue{Name: "mail", Reserved: 1}, Site: "acme.test"}, // only being worked on
		{Queue: redisq.Queue{Name: "default", Waiting: 1}},                  // no known site
	}
	f.d.markNoWorker(rows)
	if !rows[0].NoWorker || rows[1].NoWorker || rows[2].NoWorker {
		t.Errorf("no worker running: %+v", rows)
	}
	if err := f.d.startWorker(workerQueue, "acme.test"); err != nil {
		t.Fatal(err)
	}
	rows[0].NoWorker = false
	f.d.markNoWorker(rows)
	if rows[0].NoWorker {
		t.Errorf("worker running, still flagged")
	}
}

func TestWorkerSwitchOnConcurrentPorts(t *testing.T) {
	f, mux, _ := routeFixture(t, "APP_KEY=x\n")
	other := t.TempDir()
	writeFile(t, other, "artisan", "<?php\n")
	setSites(f.d, f.d.sites[0], sites.Site{Name: "beta", Host: "beta.test", Path: other})

	var wg sync.WaitGroup
	for _, host := range []string{"acme.test", "beta.test"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rec := call(mux, "POST", "/api/workers/reverb/switch", `{"site":"`+host+`","on":true}`); rec.Code != http.StatusOK {
				t.Errorf("%s on: %d %s", host, rec.Code, rec.Body)
			}
		}()
	}
	wg.Wait()
	cfg, _ := config.Load()
	a, b := cfg.ReverbPorts["acme"], cfg.ReverbPorts["beta"]
	if a == 0 || b == 0 || a == b || !cfg.Reverb["acme"] || !cfg.Reverb["beta"] {
		t.Errorf("on disk: reverb %v, ports %v", cfg.Reverb, cfg.ReverbPorts)
	}
	f.d.mu.Lock()
	ma, mb := f.d.cfg.ReverbPorts["acme"], f.d.cfg.ReverbPorts["beta"]
	f.d.mu.Unlock()
	if ma != a || mb != b {
		t.Errorf("in memory: acme %d beta %d, on disk %d %d", ma, mb, a, b)
	}
}

func TestWorkerSwitchOffAfterSlowStop(t *testing.T) {
	f, mux, _ := routeFixture(t, "APP_KEY=x\nQUEUE_CONNECTION=sqs\n")
	if rec := call(mux, "POST", "/api/workers/queue/switch", `{"site":"acme.test","on":true}`); rec.Code != http.StatusOK {
		t.Fatalf("on: %d %s", rec.Code, rec.Body)
	}
	workerStopWait = 0 // the stop gives up waiting before the exit
	call(mux, "POST", "/api/workers/queue/switch", `{"site":"acme.test","on":false}`)
	if !eventually(t, 5*time.Second, func() bool {
		st := f.d.workerState(workerQueue, "acme.test")
		return st.State == workerOff && st.PID == 0
	}) {
		t.Errorf("after a slow stop: %+v, want off", f.d.workerState(workerQueue, "acme.test"))
	}
}

func TestSyncWorkersStopsSwitchedOff(t *testing.T) {
	f := newWorkerFixture(t, "QUEUE_CONNECTION=sqs\n", func() *exec.Cmd { return shellCmd("ping -n 30 127.0.0.1 >nul") })
	if err := f.d.startWorker(workerQueue, "acme.test"); err != nil {
		t.Fatal(err)
	}
	first := f.d.workerState(workerQueue, "acme.test")
	f.setSwitch(workerQueue, false) // switched off in config.json by hand
	f.d.syncWorkers()
	if st := f.d.workerState(workerQueue, "acme.test"); st.State != workerOff || st.PID != 0 {
		t.Errorf("after sync: %+v, want off", st)
	}
	if processAlive(first.PID) {
		t.Errorf("pid %d still runs after its switch went off", first.PID)
	}
	f.d.syncWorkers()
	if n := f.count(); n != 1 {
		t.Errorf("starts = %d, want 1", n)
	}
}

func TestWorkerSwitchConcurrentOnOff(t *testing.T) {
	// Waiting for Redis: on and off are cheap, no process starts.
	f, mux, _ := routeFixture(t, "APP_KEY=x\nQUEUE_CONNECTION=redis\nREDIS_HOST=127.0.0.1\n")
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			call(mux, "POST", "/api/workers/queue/switch", fmt.Sprintf(`{"site":"acme.test","on":%v}`, i%2 == 0))
		}()
	}
	wg.Wait()
	cfg, _ := config.Load()
	f.d.mu.Lock()
	mem := f.d.cfg.QueueWorkers["acme"]
	f.d.mu.Unlock()
	if mem != cfg.QueueWorkers["acme"] {
		t.Errorf("in memory %v, on disk %v", mem, cfg.QueueWorkers["acme"])
	}
}

func TestConfigWritersKeepWorkerSwitches(t *testing.T) {
	// Writers elsewhere (theme, folder prefix, editor) load, change and save
	// config.json too; run alongside worker switches, none may drop them.
	f, mux, _ := routeFixture(t, "APP_KEY=x\nQUEUE_CONNECTION=redis\nREDIS_HOST=127.0.0.1\n")
	f.d.folderRoutes(mux)
	f.d.editorRoutes(mux)
	other := t.TempDir()
	writeFile(t, other, "artisan", "<?php\n")
	setSites(f.d, f.d.sites[0], sites.Site{Name: "beta", Host: "beta.test", Path: other})

	var wg sync.WaitGroup
	for i := range 30 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch i % 3 {
			case 0:
				call(mux, "POST", "/api/theme", `{"theme":"dark"}`)
			case 1:
				call(mux, "POST", "/api/editor", `{"id":""}`)
			case 2:
				call(mux, "POST", "/api/folders/prefix", `{"path":"C:\nowhere","prefix":""}`)
			}
		}()
	}
	for _, c := range []struct{ kind, host string }{{"queue", "acme.test"}, {"reverb", "acme.test"}, {"queue", "beta.test"}, {"reverb", "beta.test"}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rec := call(mux, "POST", "/api/workers/"+c.kind+"/switch", `{"site":"`+c.host+`","on":true}`); rec.Code != http.StatusOK {
				t.Errorf("%s %s on: %d %s", c.kind, c.host, rec.Code, rec.Body)
			}
		}()
	}
	wg.Wait()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.QueueWorkers["acme"] || !cfg.QueueWorkers["beta"] || !cfg.Reverb["acme"] || !cfg.Reverb["beta"] ||
		cfg.ReverbPorts["acme"] == 0 || cfg.ReverbPorts["beta"] == 0 {
		t.Errorf("on disk: queue %v, reverb %v, ports %v", cfg.QueueWorkers, cfg.Reverb, cfg.ReverbPorts)
	}
	if cfg.Theme != "dark" {
		t.Errorf("theme %q", cfg.Theme)
	}
}

// processCreated returns when the process with the id was created, and
// whether one with that id exists (running, or ended but still held open).
func processCreated(pid int) (time.Time, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return time.Time{}, false
	}
	defer windows.CloseHandle(h)
	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(h, &created, &exited, &kernel, &user) != nil {
		return time.Time{}, false
	}
	return time.Unix(0, created.Nanoseconds()), true
}

func TestWorkerPidReservedUntilKillDone(t *testing.T) {
	f := newWorkerFixture(t, "QUEUE_CONNECTION=sqs\n", func() *exec.Cmd { return shellCmd("ping -n 2 127.0.0.1 >nul") })
	if err := f.d.startWorker(workerQueue, "acme.test"); err != nil {
		t.Fatal(err)
	}
	w := f.d.worker(workerQueue, "acme.test")
	pid := w.status().PID
	created, ok := processCreated(pid)
	if !ok {
		t.Fatalf("pid %d not found right after the start", pid)
	}
	// A stop reads the pid; the process then exits on its own before the
	// stop gets to kill it.
	got, _, release := w.halt()
	if got != pid {
		t.Fatalf("halt returned pid %d, want %d", got, pid)
	}
	if !eventually(t, 5*time.Second, func() bool { st := w.status(); return st.State == workerStopped && st.PID == 0 }) {
		t.Fatalf("exit not processed: %+v", w.status())
	}
	// The exit is processed, but until the stop is done with the pid it
	// stays this (ended) process, so a kill cannot hit another one.
	time.Sleep(100 * time.Millisecond)
	if c, ok := processCreated(pid); !ok || !c.Equal(created) {
		t.Fatalf("pid %d was given up before the stop was done with it (found %v, created %v, want %v)", pid, ok, c, created)
	}
	if processAlive(pid) {
		t.Fatalf("pid %d still runs", pid)
	}
	release()
	if !eventually(t, 5*time.Second, func() bool { c, ok := processCreated(pid); return !ok || !c.Equal(created) }) {
		t.Errorf("pid %d still held after the stop was done", pid)
	}
}

func TestWorkerRestartRouteStillStopping(t *testing.T) {
	f, mux, _ := routeFixture(t, "APP_KEY=x\nQUEUE_CONNECTION=sqs\n")
	f.setSwitch(workerQueue, true)
	workerStopWait = 50 * time.Millisecond
	// A run whose exit never arrives (no such process, done never closed).
	w := &worker{kind: workerQueue, site: f.d.sites[0], gen: 1, state: workerRunning, pid: 0x7ffffff0, done: make(chan struct{})}
	f.d.workers = map[string]*worker{workerKey(workerQueue, "acme.test"): w}
	rec := call(mux, "POST", "/api/workers/queue/restart", `{"site":"acme.test"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "queue for acme.test is still stopping") {
		t.Errorf("restart: %d %s", rec.Code, rec.Body)
	}
	w.mu.Lock()
	w.pid = 0 // nothing for cleanup to kill
	w.mu.Unlock()
}

func TestStatusListsWorkers(t *testing.T) {
	f := newWorkerFixture(t, "QUEUE_CONNECTION=sqs\n", func() *exec.Cmd { return shellCmd("ping -n 30 127.0.0.1 >nul") })
	f.setSwitch(workerReverb, false)
	f.d.sites = append(f.d.sites,
		sites.Site{Name: "shop", Host: "shop.test", Path: t.TempDir()},
		sites.Site{Name: "adminer", Host: "adminer.test", Path: t.TempDir(), App: "adminer"})
	f.d.cfg.Reverb["adminer"] = true    // an app site: never listed
	f.d.cfg.QueueWorkers["gone"] = true // a site that is no longer there
	if err := f.d.startWorker(workerQueue, "acme.test"); err != nil {
		t.Fatal(err)
	}
	got := f.d.status().Workers
	want := []WorkerBrief{{Host: "acme.test", Kind: workerQueue, State: workerRunning}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("workers = %+v, want %+v", got, want)
	}
	b, _ := json.Marshal(f.d.status())
	if !strings.Contains(string(b), `"workers":[{"host":"acme.test","kind":"queue","state":"running"}]`) {
		t.Errorf("status JSON: %s", b)
	}

	f.setSwitch(workerQueue, false)
	f.d.stopWorker(workerQueue, "acme.test")
	b, _ = json.Marshal(f.d.status())
	if !strings.Contains(string(b), `"workers":[]`) {
		t.Errorf("status JSON with none on: %s", b)
	}
}

// appFix: a daemon with DBZuper installed (its own site and the db alias),
// no other site, and appBeforeCommand replaced by a recorder.
type appFix struct {
	*workerFixture
	app string // the app's folder

	bmu      sync.Mutex
	before   [][]string
	beforeAt []int         // f.count() when each before command ran
	block    chan struct{} // when set, before commands wait for it
	entered  chan struct{} // gets a value as each before command starts
}

const appQueueConf = "<?php\nreturn ['connections' => ['database' => [], 'transfers' => []]];\n"

func appFixture(t *testing.T, cmd func() *exec.Cmd) *appFix {
	t.Helper()
	f := &appFix{workerFixture: newWorkerFixture(t, "", cmd), app: t.TempDir(), entered: make(chan struct{}, 10)}
	os.WriteFile(filepath.Join(f.app, "artisan"), []byte("<?php\n"), 0o644)
	os.MkdirAll(filepath.Join(f.app, "config"), 0o755)
	os.WriteFile(filepath.Join(f.app, "config", "queue.php"), []byte(appQueueConf), 0o644)
	f.d.cfg.QueueWorkers, f.d.cfg.Reverb = map[string]bool{}, map[string]bool{}
	f.d.cfg.Apps = map[string]string{"dbzuper": "1.0"}
	setSites(f.d, f.appSites()...)

	old := appBeforeCommand
	t.Cleanup(func() { appBeforeCommand = old })
	appBeforeCommand = func(phpExe, dir string, args []string) error {
		f.bmu.Lock()
		f.before = append(f.before, append([]string{}, args...))
		f.beforeAt = append(f.beforeAt, f.count())
		block := f.block
		f.bmu.Unlock()
		f.entered <- struct{}{}
		if block != nil {
			<-block
		}
		return nil
	}
	return f
}

func (f *appFix) appSites() []sites.Site {
	return []sites.Site{
		{Name: "dbzuper", Host: "dbzuper.test", App: "dbzuper", Path: f.app},
		{Name: "db", Host: "db.test", App: "dbzuper", Path: f.app},
	}
}

func (f *appFix) befores() [][]string {
	f.bmu.Lock()
	defer f.bmu.Unlock()
	return append([][]string{}, f.before...)
}

func (f *appFix) appState(kind string) string {
	return f.d.workerState(kind, "dbzuper.test").State
}

func (f *appFix) bothRunning(t *testing.T) bool {
	t.Helper()
	return eventually(t, 5*time.Second, func() bool {
		return f.appState("app-transfers") == workerRunning && f.appState("app-default") == workerRunning
	})
}

var recoverCmd = [][]string{{"dbzuper:transfers:recover"}}

func pingCmd() *exec.Cmd { return shellCmd("ping -n 30 127.0.0.1 >nul") }

func TestAppWorkersStartWithLaman(t *testing.T) {
	f := appFixture(t, pingCmd)
	f.d.startAutoWorkers()
	if !f.bothRunning(t) {
		t.Fatalf("transfers %q, default %q, want both running", f.appState("app-transfers"), f.appState("app-default"))
	}
	f.d.mu.Lock()
	for key := range f.d.workers {
		if strings.HasSuffix(key, "|db.test") {
			t.Errorf("a worker on the alias: %s", key)
		}
	}
	f.d.mu.Unlock()
	if got := f.befores(); !reflect.DeepEqual(got, recoverCmd) {
		t.Errorf("before commands = %v, want %v", got, recoverCmd)
	}
	f.bmu.Lock()
	if len(f.beforeAt) == 0 || f.beforeAt[0] != 0 {
		t.Errorf("before ran after %v worker starts, want before any", f.beforeAt)
	}
	f.bmu.Unlock()
	if _, err := os.Stat(filepath.Join(config.LogDir(), "app-transfers-dbzuper.log")); err != nil {
		t.Errorf("log file: %v", err)
	}
}

func TestAppWorkersBeforeRunsOncePerStart(t *testing.T) {
	f := appFixture(t, pingCmd)
	workerBackoff = func(int) time.Duration { return time.Millisecond }
	var mu sync.Mutex
	transfersRuns := 0
	runs := func() int {
		mu.Lock()
		defer mu.Unlock()
		return transfersRuns
	}
	workerCommand = func(w *worker) (*exec.Cmd, string, error) {
		f.mu.Lock()
		f.starts++
		f.mu.Unlock()
		if w.kind == "app-transfers" {
			mu.Lock()
			transfersRuns++
			n := transfersRuns
			mu.Unlock()
			if n == 1 {
				return shellCmd("exit 1"), "fake", nil
			}
		}
		return pingCmd(), "fake", nil
	}
	f.d.startAutoWorkers()
	if !eventually(t, 5*time.Second, func() bool { return runs() >= 2 }) || !f.bothRunning(t) {
		t.Fatalf("transfers not restarted: runs %d, state %q", runs(), f.appState("app-transfers"))
	}
	if got := f.befores(); len(got) != 1 {
		t.Errorf("before commands = %v, want one run", got)
	}
}

func TestAppWorkerCommand(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "config"), 0o755)
	os.WriteFile(filepath.Join(dir, "config", "queue.php"), []byte(appQueueConf), 0o644)
	os.WriteFile(filepath.Join(dir, "php.exe"), nil, 0o644)
	ws := install.AppByName("dbzuper").Workers(dir)
	if len(ws.Run) == 0 || ws.Run[0].Name != "transfers" {
		t.Fatalf("workers = %+v", ws)
	}
	w := &worker{kind: "app-transfers", args: ws.Run[0].Args, site: sites.Site{Path: dir}, phpExe: filepath.Join(dir, "php.exe")}
	cmd, line, err := realWorkerCommand(w)
	if err != nil {
		t.Fatal(err)
	}
	if want := "php artisan queue:work transfers --queue=transfers --sleep=1 --tries=1 --timeout=0"; line != want {
		t.Errorf("line = %q, want %q", line, want)
	}
	if cmd.Dir != dir {
		t.Errorf("Dir = %q, want %q", cmd.Dir, dir)
	}
}

func TestAppWorkersHold(t *testing.T) {
	f := appFixture(t, pingCmd)
	f.d.startAutoWorkers()
	if !f.bothRunning(t) {
		t.Fatal("not running")
	}
	if !f.d.stopAppWorkers("dbzuper", true) {
		t.Fatal("stopAppWorkers: still stopping")
	}
	for _, k := range []string{"app-transfers", "app-default"} {
		if st := f.appState(k); st != workerStopped {
			t.Errorf("%s after stop = %q, want stopped", k, st)
		}
	}
	n := f.count()
	f.d.syncWorkers()
	f.d.startAutoWorkers()
	time.Sleep(200 * time.Millisecond)
	if f.count() != n || f.appState("app-transfers") != workerStopped || f.appState("app-default") != workerStopped {
		t.Errorf("held workers started again: starts %d -> %d", n, f.count())
	}
	if err := f.d.startWorker("app-transfers", "dbzuper.test"); err == nil {
		t.Errorf("startWorker of a held worker: no error")
	}
	if err := f.d.releaseAppWorkers("dbzuper"); err != nil {
		t.Fatal(err)
	}
	if !f.bothRunning(t) {
		t.Fatal("not running after release")
	}
	if got := f.befores(); len(got) != 2 {
		t.Errorf("before commands = %v, want two runs", got)
	}
}

func TestAppWorkersStopWaitsForStart(t *testing.T) {
	f := appFixture(t, pingCmd)
	f.block = make(chan struct{})
	go f.d.startAppWorkers("dbzuper")
	<-f.entered
	// A second start while the first one's before command runs does nothing.
	if err := f.d.startAppWorkers("dbzuper"); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan bool)
	go func() { stopped <- f.d.stopAppWorkers("dbzuper", true) }()
	time.Sleep(100 * time.Millisecond)
	f.bmu.Lock()
	close(f.block)
	f.bmu.Unlock()
	if ok := <-stopped; !ok {
		t.Fatal("stopAppWorkers: still stopping")
	}
	if f.count() != 0 {
		t.Errorf("a held app started %d workers", f.count())
	}
	if got := f.befores(); len(got) != 1 {
		t.Errorf("before commands = %v, want one run", got)
	}
}

func TestAppWorkersReleaseDuringStart(t *testing.T) {
	f := appFixture(t, pingCmd)
	f.block = make(chan struct{})
	go f.d.startAppWorkers("dbzuper")
	<-f.entered
	f.d.mu.Lock()
	f.d.appHold = map[string]bool{"dbzuper": true} // held while its before command runs
	f.d.mu.Unlock()
	f.bmu.Lock()
	close(f.block)
	f.block = nil
	f.bmu.Unlock()
	// The start under way may or may not have been refused by now; either
	// way the release ends with both running.
	if err := f.d.releaseAppWorkers("dbzuper"); err != nil {
		t.Fatal(err)
	}
	if !f.bothRunning(t) {
		t.Fatalf("transfers %q, default %q after release", f.appState("app-transfers"), f.appState("app-default"))
	}
}

func TestAppWorkersFollowApp(t *testing.T) {
	f := appFixture(t, pingCmd)
	f.d.startAutoWorkers()
	if !f.bothRunning(t) {
		t.Fatal("not running")
	}
	f.d.mu.Lock()
	delete(f.d.cfg.Apps, "dbzuper")
	f.d.mu.Unlock()
	setSites(f.d)
	f.d.syncWorkers()
	for _, k := range []string{"app-transfers", "app-default"} {
		if st := f.d.workerState(k, "dbzuper.test"); st.State != workerOff || st.PID != 0 {
			t.Errorf("%s after remove = %+v, want off", k, st)
		}
	}

	f.d.mu.Lock()
	f.d.cfg.Apps["dbzuper"] = "1.0"
	f.d.mu.Unlock()
	setSites(f.d, f.appSites()...)
	f.d.syncWorkers()
	if !f.bothRunning(t) {
		t.Fatalf("transfers %q, default %q after the app came back", f.appState("app-transfers"), f.appState("app-default"))
	}
	if got := f.befores(); len(got) != 2 {
		t.Errorf("before commands = %v, want two runs", got)
	}
}

func TestAppOn(t *testing.T) {
	// Installed, version not recorded.
	d := &daemon{cfg: &config.Config{Apps: map[string]string{"dbzuper": ""}}}
	if !d.appOn("dbzuper") {
		t.Error("an installed app with no version is not on")
	}
	if d.appOn("adminer") {
		t.Error("an app not installed is on")
	}
	d.appHold = map[string]bool{"dbzuper": true}
	if d.appOn("dbzuper") || d.switchedOn("app-transfers", "dbzuper") {
		t.Error("a held app is on")
	}
}

func TestAppWorkersHiddenFromSiteList(t *testing.T) {
	f := appFixture(t, pingCmd)
	f.d.startAutoWorkers()
	if !f.bothRunning(t) {
		t.Fatal("not running")
	}
	for _, w := range f.d.switchedOnWorkers() {
		if isAppKind(w.Kind) {
			t.Errorf("switchedOnWorkers lists %+v", w)
		}
	}
	for _, w := range f.d.status().Workers {
		if isAppKind(w.Kind) {
			t.Errorf("status lists %+v", w)
		}
	}
}

func TestAppWorkersOldRelease(t *testing.T) {
	f := appFixture(t, pingCmd)
	os.WriteFile(filepath.Join(f.app, "config", "queue.php"), []byte("<?php\nreturn ['connections' => ['database' => []]];\n"), 0o644)
	f.d.startAutoWorkers()
	time.Sleep(200 * time.Millisecond)
	if f.count() != 0 || len(f.befores()) != 0 {
		t.Errorf("starts %d, before commands %v, want none", f.count(), f.befores())
	}
	f.d.mu.Lock()
	n := len(f.d.workers)
	f.d.mu.Unlock()
	if n != 0 {
		t.Errorf("%d workers, want none", n)
	}
}

func TestAppWorkersRoute(t *testing.T) {
	f := appFixture(t, pingCmd)
	mux := http.NewServeMux()
	f.d.appRoutes(mux)
	f.d.startAutoWorkers()
	if !f.bothRunning(t) {
		t.Fatal("not running")
	}
	if rec := call(mux, "POST", "/api/apps/workers", `{"name":"dbzuper","action":"stop"}`); rec.Code != http.StatusOK {
		t.Fatalf("stop: %d %s", rec.Code, rec.Body)
	}
	for _, k := range []string{"app-transfers", "app-default"} {
		if st := f.appState(k); st == workerRunning || st == workerStarting {
			t.Errorf("%s after stop = %q", k, st)
		}
	}
	if err := f.d.startWorker("app-transfers", "dbzuper.test"); err == nil {
		t.Errorf("workers not held after stop")
	}
	if rec := call(mux, "POST", "/api/apps/workers", `{"name":"dbzuper","action":"start"}`); rec.Code != http.StatusOK {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	if !f.bothRunning(t) {
		t.Errorf("not running after start")
	}
	for _, body := range []string{`{"name":"nope","action":"stop"}`, `{"name":"dbzuper","action":"pause"}`} {
		if rec := call(mux, "POST", "/api/apps/workers", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", body, rec.Code)
		}
	}

	// A run whose exit never arrives.
	workerStopWait = 50 * time.Millisecond
	f.d.stopAppWorkers("dbzuper", true)
	w := &worker{kind: "app-transfers", site: f.d.sites[0], gen: 99, state: workerRunning, pid: 0x7ffffff0, done: make(chan struct{})}
	f.d.mu.Lock()
	f.d.workers[workerKey("app-transfers", "dbzuper.test")] = w
	f.d.mu.Unlock()
	rec := call(mux, "POST", "/api/apps/workers", `{"name":"dbzuper","action":"stop"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "still stopping") {
		t.Errorf("stuck stop: %d %s", rec.Code, rec.Body)
	}
	w.mu.Lock()
	w.pid = 0
	w.mu.Unlock()
}
