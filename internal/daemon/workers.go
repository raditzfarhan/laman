package daemon

import (
	"bufio"
	"errors"
	"fmt"
	"io"
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
	workerStopWait   = 10 * time.Second
)

var (
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

type worker struct {
	kind string
	site sites.Site

	mu      sync.Mutex
	state   string
	port    int    // Reverb's port
	phpExe  string // the site's php.exe, set before each start
	pid     int
	cmdline string
	note    string
	waiting string // why it waits ("Redis is off")
	errMsg  string
	since   time.Time
	stop    bool          // stop was asked for: an exit is not a crash
	gen     int           // bumped by each startWorker; older timers and exits are ignored
	timer   *time.Timer   // a pending restart
	done    chan struct{} // closed when the running process has exited
	crashes []time.Time
	tail    []string
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

func (w *worker) addLineLocked(line string) {
	w.tail = append(w.tail, line)
	if len(w.tail) > devTailLines {
		w.tail = w.tail[len(w.tail)-devTailLines:]
	}
}

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
// crashed or was stopped is started afresh.
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
	w.gen++
	gen := w.gen
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	w.stop, w.crashes, w.errMsg, w.waiting, w.note = false, nil, "", "", ""
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

	d.mu.Lock()
	v, _ := d.cfg.PHPFor(site.Name)
	d.mu.Unlock()
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
			up := d.handles[plan.Needs] != nil
			d.mu.Unlock()
			if !up {
				w.mu.Lock()
				if w.current(gen) {
					w.state, w.waiting, w.note = workerWaiting, serviceOffMsg(plan.Needs), plan.Note
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
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	fmt.Fprintf(out, "[laman %s] %s> %s\n", time.Now().Format(time.RFC3339), site.Path, line)

	// Start under w.mu so a stop either sees the pid or stops this start.
	w.mu.Lock()
	if !w.current(gen) {
		w.mu.Unlock()
		out.Close()
		pw.Close()
		return nil
	}
	if err := cmd.Start(); err != nil {
		w.state, w.errMsg = workerCrashed, err.Error()
		w.addLineLocked("[laman] " + err.Error())
		w.mu.Unlock()
		out.Close()
		pw.Close()
		return err
	}
	done := make(chan struct{})
	w.pid, w.cmdline, w.since, w.done, w.errMsg = cmd.Process.Pid, line, time.Now(), done, ""
	if w.kind == workerReverb {
		w.state = workerStarting // running once its port answers
	} else {
		w.state = workerRunning
	}
	w.mu.Unlock()

	go readWorkerOutput(w, pr, out)
	if w.kind == workerReverb {
		go waitWorkerPort(w, gen, port)
	}
	go func() {
		err := cmd.Wait()
		pw.Close()
		d.workerExited(w, gen, err)
		close(done)
	}()
	return nil
}

// workerExited records an exit: a stop, or a crash that is retried after a
// delay unless it crashed too often lately.
func (d *daemon) workerExited(w *worker, gen int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pid = 0
	if w.gen != gen {
		return // a newer start owns the worker
	}
	if w.stop {
		w.state = workerStopped
		return
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

// waitWorkerPort marks Reverb running once its port answers.
func waitWorkerPort(w *worker, gen, port int) {
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
		w.mu.Lock()
		waiting := w.current(gen) && w.state == workerStarting && w.pid != 0
		w.mu.Unlock()
		if !waiting {
			return
		}
		if portOpen(port) {
			w.mu.Lock()
			if w.current(gen) && w.state == workerStarting && w.pid != 0 {
				w.state = workerRunning
			}
			w.mu.Unlock()
			return
		}
	}
}

func readWorkerOutput(w *worker, r io.Reader, out *os.File) {
	defer out.Close()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(ansi.ReplaceAllString(sc.Text(), ""), "\r ")
		out.WriteString(line + "\n")
		w.addLine(line)
	}
	io.Copy(io.Discard, r)
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
	defer d.mu.Unlock()
	if p, ok := d.cfg.ReverbPorts[name]; ok && p != 0 {
		return p, nil
	}
	cfg, err := config.Load()
	if err != nil {
		return 0, err
	}
	_, had := cfg.ReverbPorts[name]
	p := cfg.ReverbPort(name)
	if p == 0 {
		return 0, fmt.Errorf("no free Reverb port between %d and %d", config.ReverbFirstPort, config.ReverbLastPort)
	}
	if !had {
		if err := cfg.Save(); err != nil {
			return 0, err
		}
	}
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
		if composerRequires(dir, "laravel/horizon") {
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
func (d *daemon) stopWorker(kind, host string) {
	if s, ok := d.findSite(host); ok {
		host = s.Host
	}
	w := d.worker(kind, host)
	if w == nil {
		return
	}
	pid, done := w.halt()
	if pid != 0 {
		if err := killTree(pid); err != nil {
			logf("stopping %s worker %s (pid %d): %v", kind, host, pid, err)
		}
		select {
		case <-done:
		case <-time.After(workerStopWait):
		}
	}
}

// halt marks w stopped and cancels any pending restart. It returns the
// running process, if any, for the caller to kill.
func (w *worker) halt() (int, chan struct{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stop = true
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	if w.pid == 0 && w.state != workerOff {
		w.state = workerStopped
	}
	return w.pid, w.done
}

// restartWorker stops and starts a worker, e.g. after code changes.
func (d *daemon) restartWorker(kind, host string) error {
	d.stopWorker(kind, host)
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
		if pid, _ := w.halt(); pid != 0 {
			killTree(pid)
		}
	}
}
