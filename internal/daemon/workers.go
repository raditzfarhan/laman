package daemon

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"laman/internal/config"
	"laman/internal/sites"
)

// Workers: a Laravel site's queue worker (`php artisan queue:listen`) and
// Reverb server, run with the site's own PHP. Unlike dev servers they are
// restarted when they exit, with a growing delay, until they crash too
// often in a short time.

const (
	workerQueue  = "queue"
	workerReverb = "reverb"

	workerOff      = "off"     // never started
	workerWaiting  = "waiting" // a service it needs (Redis, MariaDB, PostgreSQL) is off
	workerStarting = "starting"
	workerRunning  = "running"
	workerCrashed  = "crashed" // gave up, or could not start
	workerStopped  = "stopped"

	workerMaxCrashes = 5
)

var (
	// workerStopWait: how long a stop waits for the process to exit.
	workerStopWait = 10 * time.Second
	// workerReadyWait: how long Reverb has to open its port.
	workerReadyWait = time.Minute
	// workerOutputWait: after the process exits, how long Wait waits for
	// output still held open by children it left behind.
	workerOutputWait = 5 * time.Second

	// workerCommand builds the process for a worker; tests replace it.
	workerCommand = realWorkerCommand
	// workerBackoff is the wait before the n-th consecutive restart.
	workerBackoff = restartDelay
	// workerWindow: workerMaxCrashes crashes within it and laman gives up;
	// a run longer than it forgets earlier crashes.
	workerWindow = 2 * time.Minute
)

// restartDelay: 2s, 4s, 8s, 16s, then 30s.
func restartDelay(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	if n > 5 {
		return 30 * time.Second
	}
	d := time.Duration(1<<n) * time.Second
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

// workerRun is one started process. Its handle keeps the pid from being
// reused until the exit is processed (children killed, pid cleared) and
// every kill that read the pid meanwhile is done.
type workerRun struct {
	pid   int
	h     uintptr
	kills sync.WaitGroup // kills in progress; added to only under the worker's mu while it is w.run
}

type worker struct {
	kind string
	site sites.Site

	mu      sync.Mutex
	state   string
	port    int    // Reverb's port
	phpExe  string // the site's php.exe, set before each start
	pid     int
	run     *workerRun // the started process, until its exit is processed
	cmdline string
	note    string
	waiting string // why it waits ("Redis is off")
	errMsg  string
	since   time.Time
	stop    bool          // stop was asked for: an exit is not a crash
	gone    bool          // its site disappeared; it is off until the site returns
	offed   bool          // the stop is a switch-off: it ends off, not stopped
	failed  bool          // laman gave up on this run (Reverb never opened its port): its exit is not a crash
	gen     int           // bumped by each startWorker; older timers and exits are ignored
	timer   *time.Timer   // a pending restart
	done    chan struct{} // closed when the running process has exited
	crashes []time.Time
	tail    outputTail
}

// WorkerStatus is a worker as the dashboard sees it.
type WorkerStatus struct {
	Host    string `json:"host"`
	Kind    string `json:"kind"`
	State   string `json:"state"`
	Port    int    `json:"port,omitempty"`
	PID     int    `json:"pid,omitempty"`
	Command string `json:"command,omitempty"`
	Uptime  string `json:"uptime,omitempty"`
	Note    string `json:"note,omitempty"`
	Waiting string `json:"waiting,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (w *worker) status() WorkerStatus {
	w.mu.Lock()
	defer w.mu.Unlock()
	st := WorkerStatus{Host: w.site.Host, Kind: w.kind, State: w.state, Port: w.port, PID: w.pid,
		Command: w.cmdline, Note: w.note, Error: w.errMsg}
	if w.state == workerRunning {
		st.Uptime = time.Since(w.since).Round(time.Second).String()
	}
	if w.state == workerWaiting {
		st.Waiting = w.waiting
	}
	return st
}

func (w *worker) addLine(line string) {
	w.mu.Lock()
	w.addLineLocked(line)
	w.mu.Unlock()
}

func (w *worker) addLineLocked(line string) { w.tail.add(line) }

// current reports whether a start of generation gen may still change w:
// no stop was asked for and no newer start replaced it. Callers hold w.mu.
func (w *worker) current(gen int) bool { return !w.stop && w.gen == gen }

func workerKey(kind, host string) string { return kind + "|" + host }

func workerLogPath(kind, name string) string {
	return filepath.Join(config.LogDir(), kind+"-"+name+".log")
}

func (d *daemon) worker(kind, host string) *worker {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.workers[workerKey(kind, host)]
}

func (d *daemon) workerState(kind, host string) WorkerStatus {
	if s, ok := d.findSite(host); ok {
		host = s.Host
	}
	w := d.worker(kind, host)
	if w == nil {
		return WorkerStatus{Host: host, Kind: kind, State: workerOff}
	}
	return w.status()
}

// workerLog: the worker's recent output, newest last.
func (d *daemon) workerLog(kind, host string) string {
	if s, ok := d.findSite(host); ok {
		host = s.Host
	}
	w := d.worker(kind, host)
	if w == nil {
		return "(not started yet)"
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Join(w.tail, "\n")
}

// startWorker starts a site's queue worker or Reverb server. Starting one
// that runs (or waits to restart) is a no-op; one that waits for a service,
// crashed or was stopped is started afresh. A worker switched off in the
// config is not started.
func (d *daemon) startWorker(kind, host string) error {
	if kind != workerQueue && kind != workerReverb {
		return fmt.Errorf("unknown worker %q", kind)
	}
	site, ok := d.findSite(host)
	if !ok {
		return fmt.Errorf("no site %s", host)
	}
	if !exists(filepath.Join(site.Path, "artisan")) {
		return fmt.Errorf("%s is not a Laravel app", site.Host)
	}
	d.mu.Lock()
	// Checked under d.mu, which the switch route also holds when it changes
	// the switch: a switch-off then either comes first (and this start is
	// refused) or comes after (and stops what this starts).
	if !d.switchedOn(kind, site.Name) {
		d.mu.Unlock()
		return fmt.Errorf("the %s worker is off for %s", kind, site.Host)
	}
	if d.workers == nil {
		d.workers = map[string]*worker{}
	}
	key := workerKey(kind, site.Host)
	w := d.workers[key]
	if w == nil {
		w = &worker{kind: kind, site: site, state: workerOff}
		d.workers[key] = w
	}
	w.mu.Lock()
	d.mu.Unlock()
	if w.state == workerStarting || w.state == workerRunning {
		w.mu.Unlock()
		return nil
	}
	w.site = site // the folder or PHP may have changed since the last start
	w.failed = false
	w.gen++
	gen := w.gen
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	w.stop, w.gone, w.offed, w.crashes, w.errMsg, w.waiting, w.note = false, false, false, nil, "", "", ""
	w.state = workerStarting
	w.mu.Unlock()
	return d.launchWorker(w, gen, true)
}

// launchWorker starts one run of w. fresh is a start asked for (the log is
// started anew); otherwise it is a restart after a crash.
func (d *daemon) launchWorker(w *worker, gen int, fresh bool) error {
	w.mu.Lock()
	site := w.site
	w.mu.Unlock()
	// fail ends this start with an error that does not retry.
	fail := func(err error) error {
		w.mu.Lock()
		if w.current(gen) {
			w.state, w.errMsg, w.pid = workerCrashed, err.Error(), 0
			w.addLineLocked("[laman] " + err.Error())
		}
		w.mu.Unlock()
		return err
	}

	// The PHP the site resolved to at its last rescan, so that syncWorkers
	// can tell when it changes; a site not yet rescanned asks the config.
	v := site.PHP
	if v == "" {
		d.mu.Lock()
		v, _ = d.cfg.PHPFor(site.Name)
		d.mu.Unlock()
	}
	phpExe := filepath.Join(config.BinDir("php"), v, "php.exe")

	port := 0
	switch w.kind {
	case workerQueue:
		vals, _ := readEnvFile(site.Path)
		plan := planQueue(site.Path, vals, nil) // queue names don't matter here
		if plan.Inline {
			w.mu.Lock()
			if w.current(gen) {
				w.state, w.note = workerOff, plan.Note
			}
			w.mu.Unlock()
			return errors.New(plan.Note)
		}
		if plan.Needs != "" {
			d.mu.Lock()
			spawned := d.handles[plan.Needs] != nil
			port := serverPort(d.cfg, plan.Needs)
			d.mu.Unlock()
			// Spawned is not enough: a first MariaDB start can take a while
			// to accept connections, and starting before then would burn
			// through the crash limit.
			why := ""
			if !spawned {
				why = serviceOffMsg(plan.Needs)
			} else if port > 0 && !portOpen(port) {
				why = "Waiting for " + kindNames[plan.Needs] + " to start"
				d.watchServicePort(plan.Needs)
			}
			if why != "" {
				w.mu.Lock()
				if w.current(gen) {
					w.state, w.waiting, w.note = workerWaiting, why, plan.Note
				}
				w.mu.Unlock()
				return nil
			}
		}
		w.mu.Lock()
		w.note = plan.Note
		w.mu.Unlock()
	case workerReverb:
		var err error
		if port, err = d.reverbPort(site.Name); err != nil {
			return fail(err)
		}
		w.mu.Lock()
		w.port = port
		w.mu.Unlock()
		if portOpen(port) {
			return fail(fmt.Errorf("port %d is in use", port))
		}
	}

	w.mu.Lock()
	w.phpExe = phpExe
	w.mu.Unlock()
	cmd, line, err := workerCommand(w)
	if err != nil {
		return fail(err)
	}
	if err := os.MkdirAll(config.LogDir(), 0o755); err != nil {
		return fail(err)
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if fresh {
		flags |= os.O_TRUNC
	}
	out, err := os.OpenFile(workerLogPath(w.kind, site.Name), flags, 0o644)
	if err != nil {
		return fail(err)
	}
	// An OS pipe rather than io.Pipe: cmd.Wait then returns when the process
	// exits, even if children it left behind (queue:listen's queue:work)
	// still hold the pipe; reading goes on until they are gone.
	pr, pw, err := os.Pipe()
	if err != nil {
		out.Close()
		return fail(err)
	}
	cmd.Stdout, cmd.Stderr = pw, pw
	cmd.WaitDelay = workerOutputWait // only matters if a command brings its own writer
	writeLogHeader(out, site.Path, line)

	// Start under w.mu so a stop either sees the pid or stops this start.
	w.mu.Lock()
	if !w.current(gen) {
		w.mu.Unlock()
		out.Close()
		pw.Close()
		pr.Close()
		return nil
	}
	startedAt := time.Now() // children it starts are created after this
	if err := cmd.Start(); err != nil {
		w.state, w.errMsg = workerCrashed, err.Error()
		w.addLineLocked("[laman] " + err.Error())
		w.mu.Unlock()
		out.Close()
		pw.Close()
		pr.Close()
		return err
	}
	pw.Close() // the process has its copy; reading ends when every holder closes it
	pid := cmd.Process.Pid
	// Held from here, while Go's own handle is still open, so the id stays
	// this process's until the exit below is fully processed.
	run := &workerRun{pid: pid, h: holdProcess(pid)}
	done := make(chan struct{})
	w.pid, w.run, w.cmdline, w.since, w.done, w.errMsg = pid, run, line, time.Now(), done, ""
	if w.kind == workerReverb {
		w.state = workerStarting // running once its port answers
	} else {
		w.state = workerRunning
	}
	w.mu.Unlock()

	go readWorkerOutput(w, pr, out)
	if w.kind == workerReverb {
		go waitWorkerPort(w, gen, pid, port)
	}
	go func() {
		err := cmd.Wait()
		// A crashed queue:listen can leave its queue:work child running;
		// end whatever it left behind before a restart starts another.
		killChildren(pid, startedAt)
		d.workerExited(w, gen, run, err)
		close(done)
		// No new kill can read the pid now (w.run is no longer run); wait
		// for those under way, then let Windows reuse the id.
		run.kills.Wait()
		releaseProcess(run.h)
	}()
	return nil
}

// workerExited records an exit: a stop, or a crash that is retried after a
// delay unless it crashed too often lately.
func (d *daemon) workerExited(w *worker, gen int, run *workerRun, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.run == run {
		w.run, w.pid = nil, 0
	}
	if w.gen != gen {
		return // a newer start owns the worker (and its pid)
	}
	w.pid = 0
	if w.stop {
		w.state = w.stoppedState()
		return
	}
	if w.failed {
		return // laman ended it and already said why
	}
	msg := "exited"
	if err != nil {
		msg += ": " + err.Error()
	}
	w.addLineLocked("[laman] " + msg)
	now := time.Now()
	if now.Sub(w.since) > workerWindow {
		w.crashes = nil
	}
	recent := w.crashes[:0]
	for _, t := range w.crashes {
		if now.Sub(t) <= workerWindow {
			recent = append(recent, t)
		}
	}
	w.crashes = append(recent, now)
	if len(w.crashes) >= workerMaxCrashes {
		w.state = workerCrashed
		w.errMsg = tailLines(w.tail, 12)
		return
	}
	delay := workerBackoff(len(w.crashes))
	w.state = workerStarting
	w.errMsg = fmt.Sprintf("%s; restarting in %s", msg, delay)
	host := w.site.Host
	w.timer = time.AfterFunc(delay, func() {
		w.mu.Lock()
		ok := w.current(gen)
		if ok {
			w.timer = nil
		}
		w.mu.Unlock()
		if ok {
			if err := d.launchWorker(w, gen, false); err != nil {
				logf("restarting %s worker for %s: %v", w.kind, host, err)
			}
		}
	})
}

// waitWorkerPort marks Reverb running once its port answers. One that does
// not open it in time is ended and marked crashed (it is not restarted).
func waitWorkerPort(w *worker, gen, pid, port int) {
	// mine: the run being watched is still the worker's, and still starting.
	mine := func() bool { return w.current(gen) && w.pid == pid && w.state == workerStarting }
	deadline := time.Now().Add(workerReadyWait)
	for time.Now().Before(deadline) {
		time.Sleep(min(300*time.Millisecond, workerReadyWait/4))
		w.mu.Lock()
		ok := mine()
		w.mu.Unlock()
		if !ok {
			return
		}
		if portOpen(port) {
			w.mu.Lock()
			if mine() {
				w.state = workerRunning
			}
			w.mu.Unlock()
			return
		}
	}
	w.mu.Lock()
	if !mine() {
		w.mu.Unlock()
		return
	}
	msg := fmt.Sprintf("Reverb did not open port %d within %s", port, waitText(workerReadyWait))
	w.state, w.errMsg, w.failed = workerCrashed, msg, true
	w.addLineLocked("[laman] " + msg)
	host := w.site.Host
	release := w.claimRun()
	w.mu.Unlock()
	if err := killTree(pid); err != nil {
		logf("ending reverb for %s (pid %d): %v", host, pid, err)
	}
	release()
}

// waitText: "1 minute" for the default wait, else Go's duration text.
func waitText(d time.Duration) string {
	if d == time.Minute {
		return "1 minute"
	}
	return d.String()
}

func readWorkerOutput(w *worker, r, out *os.File) {
	defer out.Close()
	defer r.Close()
	scanOutput(r, out, w.addLine)
}

func serviceOffMsg(kind string) string {
	switch kind {
	case "redis":
		return "Redis is off"
	case "mariadb":
		return "MariaDB is off"
	case "postgres":
		return "PostgreSQL is off"
	}
	return kind + " is off"
}

// reverbPort returns the site's Reverb port, assigning and saving one on
// first use.
func (d *daemon) reverbPort(name string) (int, error) {
	d.mu.Lock()
	p, ok := d.cfg.ReverbPorts[name]
	d.mu.Unlock()
	if ok && p != 0 {
		return p, nil
	}
	d.cfgMu.Lock() // before mu, as everywhere
	defer d.cfgMu.Unlock()
	cfg, err := config.Load()
	if err != nil {
		return 0, err
	}
	_, had := cfg.ReverbPorts[name]
	p = cfg.ReverbPort(name)
	if p == 0 {
		return 0, fmt.Errorf("no free Reverb port between %d and %d", config.ReverbFirstPort, config.ReverbLastPort)
	}
	if !had {
		if err := cfg.Save(); err != nil {
			return 0, err
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cfg.ReverbPorts == nil {
		d.cfg.ReverbPorts = map[string]int{}
	}
	d.cfg.ReverbPorts[name] = p
	return p, nil
}

// realWorkerCommand: `php artisan queue:listen ...` or `php artisan
// reverb:start ...` in the project folder, with the site's PHP.
func realWorkerCommand(w *worker) (*exec.Cmd, string, error) {
	w.mu.Lock()
	kind, dir, phpExe, port := w.kind, w.site.Path, w.phpExe, w.port
	w.mu.Unlock()
	if !exists(phpExe) {
		return nil, "", fmt.Errorf("PHP is not installed at %s", phpExe)
	}
	var args []string
	switch kind {
	case workerQueue:
		vals, _ := readEnvFile(dir)
		var queues []string
		if usesHorizon(dir) {
			queues = horizonQueues(phpExe, dir)
		}
		plan := planQueue(dir, vals, queues)
		if plan.Inline {
			return nil, "", errors.New(plan.Note)
		}
		args = plan.Args
	case workerReverb:
		args = reverbArgs(port)
	default:
		return nil, "", fmt.Errorf("unknown worker %q", kind)
	}
	cmd := exec.Command(phpExe, append([]string{"artisan"}, args...)...)
	cmd.Dir = dir
	noWindow(cmd)
	return cmd, "php artisan " + strings.Join(args, " "), nil
}

// stopWorker stops a worker and everything it started, cancels a pending
// restart, and waits (briefly) for the exit. The state is then stopped.
func (d *daemon) stopWorker(kind, host string) { d.stopWorkerWait(kind, host) }

// stopWorkerWait is stopWorker, reporting false when the process had not
// exited within workerStopWait.
func (d *daemon) stopWorkerWait(kind, host string) bool {
	if s, ok := d.findSite(host); ok {
		host = s.Host
	}
	w := d.worker(kind, host)
	if w == nil {
		return true
	}
	pid, done, release := w.halt()
	if pid == 0 {
		release()
		return true
	}
	if err := killTree(pid); err != nil {
		logf("stopping %s worker %s (pid %d): %v", kind, host, pid, err)
	}
	release()
	select {
	case <-done:
		return true
	case <-time.After(workerStopWait):
		return false
	}
}

// halt marks w stopped and cancels any pending restart. It returns the
// running process, if any, for the caller to kill, and release, which the
// caller calls once done with the pid (until then the pid is not reused).
func (w *worker) halt() (int, chan struct{}, func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stop, w.errMsg = true, ""
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	if w.pid == 0 && w.state != workerOff {
		w.state = w.stoppedState()
	}
	return w.pid, w.done, w.claimRun()
}

// claimRun keeps the running process's pid reserved until the returned
// func is called, even if the process exits meanwhile. Callers hold w.mu.
func (w *worker) claimRun() func() {
	r := w.run
	if r == nil {
		return func() {}
	}
	r.kills.Add(1)
	return r.kills.Done
}

// stoppedState: what a stopped worker shows, off after a switch-off.
// Callers hold w.mu.
func (w *worker) stoppedState() string {
	if w.offed {
		return workerOff
	}
	return workerStopped
}

// errStillStopping: a restart gave up waiting for the old process to exit.
var errStillStopping = errors.New("still stopping")

// restartWorker stops and starts a worker, e.g. after code changes.
func (d *daemon) restartWorker(kind, host string) error {
	if !d.stopWorkerWait(kind, host) {
		return fmt.Errorf("%s for %s is %w", kind, host, errStillStopping)
	}
	return d.startWorker(kind, host)
}

// stopAllWorkers stops every worker, on daemon shutdown.
func (d *daemon) stopAllWorkers() {
	d.mu.Lock()
	ws := make([]*worker, 0, len(d.workers))
	for _, w := range d.workers {
		ws = append(ws, w)
	}
	d.mu.Unlock()
	for _, w := range ws {
		pid, _, release := w.halt()
		if pid != 0 {
			killTree(pid)
		}
		release()
	}
}

// active: the worker runs, or is meant to (starting, waiting for a
// service, or given up on after crashes). Callers hold w.mu.
func (w *worker) active() bool {
	switch w.state {
	case workerRunning, workerStarting, workerWaiting, workerCrashed:
		return !w.gone
	}
	return false
}

// switchedOn reports whether the site's worker of kind is switched on in the
// config. Callers hold d.mu.
func (d *daemon) switchedOn(kind, name string) bool {
	if kind == workerReverb {
		return d.cfg.Reverb[name]
	}
	return d.cfg.QueueWorkers[name]
}

// startAutoWorkers starts every switched-on worker, once at daemon start
// (after the add-on services). From then on syncWorkers also starts
// switched-on workers of sites that appear.
func (d *daemon) startAutoWorkers() {
	type job struct{ kind, host string }
	var jobs []job
	d.mu.Lock()
	d.workersReady = true
	for _, s := range d.sites {
		for _, kind := range []string{workerQueue, workerReverb} {
			if d.switchedOn(kind, s.Name) {
				jobs = append(jobs, job{kind, s.Host})
			}
		}
	}
	d.mu.Unlock()
	for _, j := range jobs {
		if err := d.startWorker(j.kind, j.host); err != nil {
			logf("start %s worker for %s: %v", j.kind, j.host, err)
		}
	}
}

// syncWorkers brings the workers in line with the sites after a rescan:
// workers of a site that is gone, and active ones switched off in the
// config, are stopped (state off); switched-on ones
// of a site that (re)appeared are started; active ones whose site now
// resolves to another PHP version are restarted. It does nothing when
// nothing changed. The processes are stopped and started outside d.mu.
func (d *daemon) syncWorkers() {
	type job struct {
		kind, host string
		w          *worker
	}
	var gone, start, restart, off []job
	d.mu.Lock()
	byHost := make(map[string]sites.Site, len(d.sites))
	for _, s := range d.sites {
		byHost[s.Host] = s
	}
	for _, w := range d.workers {
		w.mu.Lock()
		s, ok := byHost[w.site.Host]
		switch {
		case !ok && !w.gone:
			gone = append(gone, job{w.kind, w.site.Host, w})
		case ok && w.gone:
			if d.switchedOn(w.kind, s.Name) {
				start = append(start, job{w.kind, s.Host, w})
			}
		case ok && w.active() && !d.switchedOn(w.kind, s.Name):
			off = append(off, job{w.kind, s.Host, w}) // switched off in config.json
		case ok && w.active() && w.site.PHP != s.PHP:
			restart = append(restart, job{w.kind, s.Host, w})
		}
		w.mu.Unlock()
	}
	// Switched-on workers never started: a site that appeared, or a switch
	// turned on in config.json. Not before the start with laman, which
	// starts them in its own time.
	var fresh []job
	if d.workersReady {
		for _, s := range d.sites {
			for _, kind := range []string{workerQueue, workerReverb} {
				if d.switchedOn(kind, s.Name) && d.workers[workerKey(kind, s.Host)] == nil {
					fresh = append(fresh, job{kind, s.Host, nil})
				}
			}
		}
	}
	d.mu.Unlock()

	for _, j := range gone {
		logf("%s worker for %s: its site is gone, stopping", j.kind, j.host)
		if !d.stopWorkerWait(j.kind, j.host) {
			logf("%s worker for %s: still stopping after %s", j.kind, j.host, workerStopWait)
		}
		j.w.mu.Lock()
		j.w.gone = true
		if j.w.pid == 0 {
			j.w.state = workerOff
		}
		j.w.mu.Unlock()
	}
	for _, j := range off {
		logf("%s worker for %s: switched off, stopping", j.kind, j.host)
		d.switchWorkerOff(j.kind, j.host)
	}
	for _, j := range restart {
		logf("%s worker for %s: PHP changed, restarting", j.kind, j.host)
		if err := d.restartWorker(j.kind, j.host); err != nil {
			logf("restart %s worker for %s: %v", j.kind, j.host, err)
		}
	}
	for _, j := range fresh {
		if s, ok := byHost[j.host]; !ok || !exists(filepath.Join(s.Path, "artisan")) {
			continue // not a Laravel app (any more): nothing to start, nothing to log
		}
		start = append(start, j)
	}
	for _, j := range start {
		if err := d.startWorker(j.kind, j.host); err != nil {
			logf("start %s worker for %s: %v", j.kind, j.host, err)
		}
	}
}

// workersServiceChanged starts the workers waiting for a service; each
// works out again whether what it needs now runs.
func (d *daemon) workersServiceChanged() {
	type job struct{ kind, host string }
	var jobs []job
	d.mu.Lock()
	for _, w := range d.workers {
		w.mu.Lock()
		if w.state == workerWaiting && !w.gone {
			jobs = append(jobs, job{w.kind, w.site.Host})
		}
		w.mu.Unlock()
	}
	d.mu.Unlock()
	for _, j := range jobs {
		if err := d.startWorker(j.kind, j.host); err != nil {
			logf("start %s worker for %s: %v", j.kind, j.host, err)
		}
	}
}

// restartSiteWorkers restarts a site's active workers, e.g. after its PHP
// version or .env changed. Stopped and never-started ones are left alone.
func (d *daemon) restartSiteWorkers(name string) error {
	site, ok := d.findSite(name)
	if !ok {
		return fmt.Errorf("no site %s", name)
	}
	var errs []error
	for _, kind := range []string{workerQueue, workerReverb} {
		w := d.worker(kind, site.Host)
		if w == nil {
			continue
		}
		w.mu.Lock()
		act := w.active()
		w.mu.Unlock()
		if act {
			errs = append(errs, d.restartWorker(kind, site.Host))
		}
	}
	return errors.Join(errs...)
}

// watchServicePort waits for a running add-on server to answer on its port,
// then starts the workers waiting for a service. It watches for as long as
// that run of the server lasts (a slow first start may take minutes); a
// server stopped meanwhile leaves its workers waiting until it is started
// again, which watches anew. At most one watch per server runs at a time.
func (d *daemon) watchServicePort(kind string) {
	d.mu.Lock()
	h := d.handles[kind]
	port := serverPort(d.cfg, kind)
	if h == nil || port <= 0 || d.portWatch[kind] == h {
		d.mu.Unlock()
		return // not running, not a service a worker needs (Mailpit), or this run is watched
	}
	if d.portWatch == nil {
		d.portWatch = map[string]*handle{}
	}
	d.portWatch[kind] = h
	d.mu.Unlock()
	go func() {
		open := portOpen(port)
	poll:
		for !open {
			select {
			case <-h.done:
				break poll // stopped: its workers wait for the next start
			case <-time.After(300 * time.Millisecond):
				open = portOpen(port)
			}
		}
		// Done watching before starting anyone, so a worker that finds the
		// port closed again from here on starts a new watch.
		d.mu.Lock()
		if d.portWatch[kind] == h {
			delete(d.portWatch, kind)
		}
		d.mu.Unlock()
		if open {
			d.workersServiceChanged()
		}
	}()
}

// reverbPorts: the Reverb port of each site that has Reverb switched on, by
// host, for Caddy. Whether its server is running does not matter.
func (d *daemon) reverbPorts() map[string]int {
	d.mu.Lock()
	list := d.sites
	d.mu.Unlock()
	return d.reverbPortsFor(list)
}

// reverbPortsFor is reverbPorts for a site list not yet stored in d.sites.
func (d *daemon) reverbPortsFor(list []sites.Site) map[string]int {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string]int{}
	for _, s := range list {
		if p := d.cfg.ReverbPorts[s.Name]; d.cfg.Reverb[s.Name] && p > 0 {
			out[s.Host] = p
		}
	}
	return out
}

// workersCaddyChanged regenerates Caddy's config after a Reverb switch, so
// the site's /app routes follow; tests replace it.
var workersCaddyChanged = (*daemon).devChanged

// siteEndpoint is how browsers reach a site, as its URL is built
// (caddyconf.URL): https on laman's HTTPS port when the site has a
// certificate, else http on the HTTP port.
func (d *daemon) siteEndpoint(host string) (scheme string, port int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cfg.HTTPSPort > 0 && d.certHosts[host] {
		return "https", d.cfg.HTTPSPort
	}
	return "http", d.cfg.HTTPPort
}

// reverbWant is what the site's .env should hold for its Reverb server on
// reverbPort behind laman's proxy.
func (d *daemon) reverbWant(s sites.Site, reverbPort int) []envKV {
	scheme, port := d.siteEndpoint(s.Host)
	return reverbEnv(s.Host, scheme, port, reverbPort)
}

// reverbEnvReply: the .env keys laman wrote, where the .env as it was went
// (only when laman changed it), and whether Laravel's config is cached (the
// new values then need `php artisan config:clear`).
type reverbEnvReply struct {
	Changed      []string `json:"changed"`
	ConfigCached bool     `json:"configCached"`
	Backup       string   `json:"backup,omitempty"`
}

// setupReverbEnv writes laman's Reverb values into the site's .env and
// restarts the site's running workers when anything changed.
func (d *daemon) setupReverbEnv(s sites.Site) (reverbEnvReply, error) {
	d.mu.Lock()
	on := d.cfg.Reverb[s.Name]
	d.mu.Unlock()
	if !on {
		return reverbEnvReply{}, fmt.Errorf("Reverb is off for %s; switch it on first", s.Host)
	}
	vals, ok := readEnvFile(s.Path)
	if !ok {
		return reverbEnvReply{}, fmt.Errorf("%s has no .env", s.Host)
	}
	if vals["REVERB_APP_KEY"] == "" {
		return reverbEnvReply{}, errors.New("run php artisan reverb:install first")
	}
	rp, err := d.reverbPort(s.Name)
	if err != nil {
		return reverbEnvReply{}, err
	}
	want := d.reverbWant(s, rp)
	changed := envMismatch(vals, want)
	backup := ""
	if len(changed) > 0 {
		sets := make([]envChange, len(want))
		for i, kv := range want {
			sets[i] = envChange{"set", kv.Key, kv.Value}
		}
		saved, err := d.writeEnv(s, "", func(o string) (string, error) { return applyEnvChanges(o, sets) })
		if err != nil {
			return reverbEnvReply{}, err
		}
		if saved.Changed {
			backup = filepath.Join(envBackupDir(s.Name), saved.Backup+".env")
		}
		if err := d.restartSiteWorkers(s.Name); err != nil {
			logf("restarting workers of %s after its .env changed: %v", s.Host, err)
		}
	} else {
		changed = []string{}
	}
	return reverbEnvReply{Changed: changed, Backup: backup, ConfigCached: exists(filepath.Join(s.Path, "bootstrap", "cache", "config.php"))}, nil
}

// switchWorkerOff stops a worker that was switched off; it then shows as
// off, not as stopped by hand.
func (d *daemon) switchWorkerOff(kind, host string) {
	w := d.worker(kind, host)
	if w == nil {
		return
	}
	// Marked first, so an exit after a slow stop also ends off.
	w.mu.Lock()
	w.offed, w.waiting = true, ""
	w.mu.Unlock()
	if !d.stopWorkerWait(kind, host) {
		logf("%s worker for %s: still stopping after %s", kind, host, workerStopWait)
	}
}

// workerRoutes: the dashboard's and the CLI's worker API. Bodies name the
// site by host or name, like the dev server routes.
func (d *daemon) workerRoutes(mux *http.ServeMux) {
	kindOf := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		k := r.PathValue("kind")
		if k != workerQueue && k != workerReverb {
			fail(w, http.StatusBadRequest, "unknown worker %q (queue or reverb)", k)
			return "", false
		}
		return k, true
	}
	// The switched-on workers of current sites.
	mux.HandleFunc("GET /api/workers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, d.switchedOnWorkers())
	})
	mux.HandleFunc("POST /api/workers/{kind}/switch", func(w http.ResponseWriter, r *http.Request) {
		kind, ok := kindOf(w, r)
		if !ok {
			return
		}
		var req struct {
			Site string
			On   bool
		}
		if err := readJSON(r, &req); err != nil || req.Site == "" {
			fail(w, http.StatusBadRequest, "site required")
			return
		}
		s, ok := d.findSite(req.Site)
		if !ok || s.App != "" {
			fail(w, http.StatusNotFound, "no site %s", req.Site)
			return
		}
		if req.On {
			if !exists(filepath.Join(s.Path, "artisan")) {
				fail(w, http.StatusBadRequest, "%s is not a Laravel app", s.Host)
				return
			}
			if kind == workerQueue {
				vals, _ := readEnvFile(s.Path)
				if plan := planQueue(s.Path, vals, nil); plan.Inline {
					fail(w, http.StatusBadRequest, "%s", plan.Note)
					return
				}
			}
		}
		port := 0
		// The daemon's copy follows under the same cfgMu hold, so a
		// concurrent on and off cannot leave memory and disk disagreeing.
		// The next refresh reloads it anyway.
		err := d.saveConfigThen(func(c *config.Config) error {
			m := &c.QueueWorkers
			if kind == workerReverb {
				m = &c.Reverb
			}
			if !req.On {
				delete(*m, s.Name)
				return nil
			}
			if *m == nil {
				*m = map[string]bool{}
			}
			(*m)[s.Name] = true
			if kind == workerReverb {
				if port = c.ReverbPort(s.Name); port == 0 {
					return fmt.Errorf("no free Reverb port between %d and %d", config.ReverbFirstPort, config.ReverbLastPort)
				}
			}
			return nil
		}, func() {
			d.mu.Lock()
			defer d.mu.Unlock()
			m := &d.cfg.QueueWorkers
			if kind == workerReverb {
				m = &d.cfg.Reverb
			}
			if req.On {
				if *m == nil {
					*m = map[string]bool{}
				}
				(*m)[s.Name] = true
			} else {
				delete(*m, s.Name)
			}
			if port > 0 {
				if d.cfg.ReverbPorts == nil {
					d.cfg.ReverbPorts = map[string]int{}
				}
				d.cfg.ReverbPorts[s.Name] = port
			}
		})
		if err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}

		if req.On {
			// A failed start shows in the state (crashed, with the error).
			if err := d.startWorker(kind, s.Host); err != nil {
				logf("start %s worker for %s: %v", kind, s.Host, err)
			}
		} else {
			d.switchWorkerOff(kind, s.Host)
		}
		if kind == workerReverb {
			workersCaddyChanged(d)
		}
		writeJSON(w, d.workerState(kind, s.Host))
	})
	mux.HandleFunc("POST /api/workers/{kind}/restart", func(w http.ResponseWriter, r *http.Request) {
		kind, ok := kindOf(w, r)
		if !ok {
			return
		}
		var req struct{ Site string }
		if err := readJSON(r, &req); err != nil || req.Site == "" {
			fail(w, http.StatusBadRequest, "site required")
			return
		}
		s, ok := d.findSite(req.Site)
		if !ok || s.App != "" {
			fail(w, http.StatusNotFound, "no site %s", req.Site)
			return
		}
		d.mu.Lock()
		on := d.switchedOn(kind, s.Name)
		d.mu.Unlock()
		if !on {
			fail(w, http.StatusBadRequest, "the %s worker is off for %s", kind, s.Host)
			return
		}
		if err := d.restartWorker(kind, s.Host); err != nil {
			if errors.Is(err, errStillStopping) {
				fail(w, http.StatusConflict, "%v", err)
				return
			}
			// A failed start shows in the state (crashed, with the error).
			logf("restart %s worker for %s: %v", kind, s.Host, err)
		}
		writeJSON(w, d.workerState(kind, s.Host))
	})
	// The worker's recent output, newest last.
	mux.HandleFunc("GET /api/workers/{kind}/log", func(w http.ResponseWriter, r *http.Request) {
		kind, ok := kindOf(w, r)
		if !ok {
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, d.workerLog(kind, r.URL.Query().Get("site")))
	})
	mux.HandleFunc("POST /api/reverb/env", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Site string }
		if err := readJSON(r, &req); err != nil || req.Site == "" {
			fail(w, http.StatusBadRequest, "site required")
			return
		}
		s, ok := d.findSite(req.Site)
		if !ok || s.App != "" {
			fail(w, http.StatusNotFound, "no site %s", req.Site)
			return
		}
		reply, err := d.setupReverbEnv(s)
		if err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		writeJSON(w, reply)
	})
}

// WorkerBrief is a switched-on worker as Home lists it.
type WorkerBrief struct {
	Host  string `json:"host"`
	Kind  string `json:"kind"`
	State string `json:"state"`
}

// switchedOnWorkers lists the switched-on workers of the current sites
// (laman's apps left out), in site order, the queue before Reverb. Call it
// without d.mu held: workerState takes it.
func (d *daemon) switchedOnWorkers() []WorkerStatus {
	type job struct{ kind, host string }
	var on []job
	d.mu.Lock()
	for _, s := range d.sites {
		if s.App != "" {
			continue
		}
		for _, kind := range []string{workerQueue, workerReverb} {
			if d.switchedOn(kind, s.Name) {
				on = append(on, job{kind, s.Host})
			}
		}
	}
	d.mu.Unlock()
	list := []WorkerStatus{}
	for _, j := range on {
		list = append(list, d.workerState(j.kind, j.host))
	}
	return list
}
