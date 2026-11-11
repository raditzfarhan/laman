package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"laman/internal/config"
	"laman/internal/install"
	"laman/internal/sites"
)

// Dev servers: laman runs a Node site's own dev server (`pnpm run dev`) on
// a free port and its .test name forwards there. They run only when asked
// (the dashboard, `laman dev start`, or "start with laman"), and are not
// restarted when they exit: a crash usually needs a fix in the project.

const (
	devStarting = "starting"
	devRunning  = "running"
	devStopped  = "stopped"
	devCrashed  = "crashed"

	devFirstPort = 3100
	devLastPort  = 3999
	devTailLines = 300
)

type devServer struct {
	site sites.Site

	mu      sync.Mutex
	state   string
	port    int
	pid     int
	cmdline string
	since   time.Time
	stop    bool     // Stop was asked for: an exit is not a crash
	tail    []string // last lines of output, colour codes removed
}

// DevStatus is a dev server as the dashboard and `laman dev` see it.
type DevStatus struct {
	Host    string `json:"host"`
	State   string `json:"state"`
	Port    int    `json:"port,omitempty"`
	PID     int    `json:"pid,omitempty"`
	Command string `json:"command,omitempty"`
	Uptime  string `json:"uptime,omitempty"`
	Error   string `json:"error,omitempty"` // last output lines after a crash
}

func (s *devServer) status() DevStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := DevStatus{Host: s.site.Host, State: s.state, Port: s.port, PID: s.pid, Command: s.cmdline}
	if s.state == devRunning {
		st.Uptime = time.Since(s.since).Round(time.Second).String()
	}
	if s.state == devCrashed {
		st.Error = tailLines(s.tail, 12)
	}
	return st
}

func (s *devServer) addLine(line string) {
	s.mu.Lock()
	s.tail = append(s.tail, line)
	if len(s.tail) > devTailLines {
		s.tail = s.tail[len(s.tail)-devTailLines:]
	}
	s.mu.Unlock()
}

func tailLines(lines []string, n int) string {
	var keep []string
	for i := len(lines) - 1; i >= 0 && len(keep) < n; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			keep = append([]string{lines[i]}, keep...)
		}
	}
	return strings.Join(keep, "\n")
}

// devPorts: the ports of running dev servers, by host, for Caddy.
func (d *daemon) devPorts() map[string]int {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string]int{}
	for host, s := range d.devs {
		s.mu.Lock()
		if s.state == devRunning {
			out[host] = s.port
		}
		s.mu.Unlock()
	}
	return out
}

func (d *daemon) devState(host string) DevStatus {
	d.mu.Lock()
	s := d.devs[host]
	d.mu.Unlock()
	if s == nil {
		return DevStatus{Host: host, State: devStopped}
	}
	return s.status()
}

func (d *daemon) findSite(host string) (sites.Site, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, s := range d.sites {
		if s.Host == host || s.Name == host {
			return s, true
		}
	}
	return sites.Site{}, false
}

// startDev starts a Node site's dev server. Starting one that runs is a no-op.
func (d *daemon) startDev(host string) error {
	site, ok := d.findSite(host)
	if !ok {
		return fmt.Errorf("no site %s", host)
	}
	if site.Kind != sites.KindNode || site.DevCmd == "" {
		return fmt.Errorf("%s is not a Node app with a dev server", site.Host)
	}
	pm := sites.PackageManager(site.Path)
	if !exists(filepath.Join(site.Path, "node_modules")) {
		return fmt.Errorf("%s has no node_modules yet: run `%s install` in %s first", site.Host, pm, site.Path)
	}
	if strings.HasPrefix(site.DevCmd, "bun ") && !install.HaveBun() {
		return fmt.Errorf("%s uses bun, which is not installed: run `laman install bun`", site.Host)
	}
	d.mu.Lock()
	if d.devs == nil {
		d.devs = map[string]*devServer{}
	}
	if s := d.devs[site.Host]; s != nil {
		s.mu.Lock()
		busy := s.state == devStarting || s.state == devRunning
		s.mu.Unlock()
		if busy {
			d.mu.Unlock()
			return nil
		}
	}
	used := map[int]bool{}
	for _, s := range d.devs {
		used[s.port] = true
	}
	port := freeDevPort(used)
	s := &devServer{site: site, state: devStarting, port: port, since: time.Now()}
	d.devs[site.Host] = s
	d.mu.Unlock()
	if port == 0 {
		s.mu.Lock()
		s.state, s.tail = devCrashed, []string{fmt.Sprintf("laman: no free port between %d and %d", devFirstPort, devLastPort)}
		s.mu.Unlock()
		return errors.New(s.tail[0])
	}

	line := site.DevCmd + portArgs(site, port)
	cmd := shellCmd(line)
	cmd.Dir = site.Path
	cmd.Env = devEnv(site.Path, port)
	out, err := os.OpenFile(devLogPath(site.Name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	fmt.Fprintf(out, "[laman %s] %s> %s\n", time.Now().Format(time.RFC3339), site.Path, line)
	if cfg, err := config.Load(); err == nil {
		if v, from := cfg.NodeFor(site.Path); from != "" {
			fmt.Fprintf(out, "[laman] Node %s (from %s)\n", v, from)
		} else if msg := config.NodeMismatch(site.Path, v); msg != "" {
			fmt.Fprintf(out, "[laman] %s\n", msg)
			s.addLine("[laman] " + msg)
		}
	}
	if err := cmd.Start(); err != nil {
		out.Close()
		pw.Close()
		s.mu.Lock()
		s.state, s.tail = devCrashed, []string{err.Error()}
		s.mu.Unlock()
		return err
	}
	s.mu.Lock()
	s.pid, s.cmdline = cmd.Process.Pid, line
	s.mu.Unlock()
	go d.readDevOutput(s, pr, out)
	go d.waitDevReady(s)
	go func() {
		err := cmd.Wait()
		pw.Close()
		s.mu.Lock()
		if s.stop {
			s.state = devStopped
		} else {
			s.state = devCrashed
			msg := "the dev server exited"
			if err != nil {
				msg += ": " + err.Error()
			}
			s.tail = append(s.tail, "[laman] "+msg)
		}
		s.pid = 0
		s.mu.Unlock()
		d.devChanged() // stop forwarding; the notice page says what happened
	}()
	d.devChanged() // notice page: "starting"
	return nil
}

// stopDev stops a dev server and everything it started.
func (d *daemon) stopDev(host string) error {
	d.mu.Lock()
	s := d.devs[host]
	d.mu.Unlock()
	if s == nil {
		return nil
	}
	s.mu.Lock()
	pid := s.pid
	s.stop = true
	if pid == 0 {
		s.state = devStopped
	}
	s.mu.Unlock()
	if pid != 0 {
		if err := killTree(pid); err != nil {
			logf("stopping dev server %s (pid %d): %v", host, pid, err)
		}
	}
	d.devChanged()
	return nil
}

func (d *daemon) stopAllDev() {
	d.mu.Lock()
	hosts := make([]string, 0, len(d.devs))
	for h := range d.devs {
		hosts = append(hosts, h)
	}
	d.mu.Unlock()
	for _, h := range hosts {
		d.mu.Lock()
		s := d.devs[h]
		d.mu.Unlock()
		s.mu.Lock()
		pid := s.pid
		s.stop = true
		s.mu.Unlock()
		if pid != 0 {
			killTree(pid)
		}
	}
}

// devChanged rewrites Caddy's config and the notice pages after a dev
// server starts, becomes ready or stops. Changes close together share one
// refresh.
func (d *daemon) devChanged() {
	d.mu.Lock()
	if d.devRefresh {
		d.mu.Unlock()
		return
	}
	d.devRefresh = true
	d.mu.Unlock()
	go func() {
		time.Sleep(150 * time.Millisecond)
		d.mu.Lock()
		d.devRefresh = false
		d.mu.Unlock()
		if err := d.refresh(); err != nil {
			logf("refresh after dev server change: %v", err)
		}
	}()
}

// localURL finds the address a dev server prints when it is up
// ("Local: http://localhost:5173/"), so laman follows a server that picked
// its own port.
var (
	localURL = regexp.MustCompile(`https?://(?:localhost|127\.0\.0\.1|\[::1\]|0\.0\.0\.0):(\d{2,5})`)
	ansi     = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]|\x1b\\][^\x07]*\x07")
)

func (d *daemon) readDevOutput(s *devServer, r io.Reader, out *os.File) {
	defer out.Close()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	seenURL := false
	for sc.Scan() {
		line := strings.TrimRight(ansi.ReplaceAllString(sc.Text(), ""), "\r ")
		out.WriteString(line + "\n")
		s.addLine(line)
		if !seenURL {
			if m := localURL.FindStringSubmatch(line); m != nil {
				if p, err := strconv.Atoi(m[1]); err == nil && p != d.cfg.ControlPort {
					seenURL = true
					s.mu.Lock()
					if p != s.port {
						s.port = p
					}
					s.mu.Unlock()
				}
			}
		}
	}
	io.Copy(io.Discard, r)
}

// waitDevReady marks the server running once its port answers, which is
// when its .test name starts forwarding there. It gives up after 5 minutes
// (first runs of big apps can be slow); the server keeps running.
func (d *daemon) waitDevReady(s *devServer) {
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(400 * time.Millisecond)
		s.mu.Lock()
		state, port := s.state, s.port
		s.mu.Unlock()
		if state != devStarting {
			return
		}
		if portOpen(port) {
			s.mu.Lock()
			if s.state == devStarting {
				s.state, s.since = devRunning, time.Now()
			}
			s.mu.Unlock()
			d.devChanged()
			return
		}
	}
}

// portOpen checks both loopback addresses: Node often listens on ::1 only.
func portOpen(port int) bool {
	for _, host := range []string{"127.0.0.1", "::1"} {
		c, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 300*time.Millisecond)
		if err == nil {
			c.Close()
			return true
		}
	}
	return false
}

func freeDevPort(used map[int]bool) int {
	for p := devFirstPort; p <= devLastPort; p++ {
		if used[p] || portOpen(p) {
			continue
		}
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			continue
		}
		ln.Close()
		return p
	}
	return 0
}

// portArgs tells the dev server which port to use, for the tools that take
// a flag (Vite ignores $PORT). Scripts that chain several commands get no
// flag; they read $PORT or print their own address, which laman follows.
func portArgs(site sites.Site, port int) string {
	flag := fmt.Sprintf(" --port %d", port)
	if strings.HasPrefix(site.DevCmd, `node_modules\.bin\quasar`) {
		return flag
	}
	parts := strings.Fields(site.DevCmd) // "<pm> run <script>"
	if len(parts) != 3 || parts[1] != "run" {
		return ""
	}
	script := scriptText(site.Path, parts[2])
	if strings.ContainsAny(script, "&|;") {
		return ""
	}
	words := strings.Fields(script)
	if len(words) == 0 {
		return ""
	}
	switch words[0] {
	case "vite", "vp":
		flag += " --strictPort"
	case "nuxt", "nuxi", "next", "astro", "ng", "vue-cli-service", "quasar", "svelte-kit":
	default:
		return ""
	}
	if parts[0] == "npm" {
		return " --" + flag
	}
	return flag
}

func scriptText(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return ""
	}
	var pkg struct{ Scripts map[string]string }
	json.Unmarshal(b, &pkg)
	return pkg.Scripts[name]
}

// devEnv: the daemon's environment with laman's Node first on PATH (the
// project's .nvmrc version when installed, else the default), the port in
// $PORT, and no browser windows or colour codes.
func devEnv(dir string, port int) []string {
	var path string
	var env []string
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(k) {
		case "PATH":
			path = v
		case "PORT", "BROWSER", "FORCE_COLOR", "NO_COLOR":
		default:
			env = append(env, kv)
		}
	}
	if cfg, err := config.Load(); err == nil {
		v, _ := cfg.NodeFor(dir)
		nodeDir := install.NodeDir(v)
		if nodeDir == "" {
			nodeDir = install.NodeDir(cfg.DefaultNode())
		}
		if nodeDir != "" {
			path = nodeDir + ";" + install.NpmPrefix() + ";" + path
		}
	}
	if bun := install.BunDir(); bun != "" {
		path = bun + ";" + path
	}
	return append(env, "PATH="+path, "PORT="+strconv.Itoa(port), "BROWSER=none", "FORCE_COLOR=0", "NO_COLOR=1")
}

func devLogPath(name string) string { return filepath.Join(config.LogDir(), "dev-"+name+".log") }

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// startAutoDev starts the dev servers marked "start with laman".
func (d *daemon) startAutoDev() {
	d.mu.Lock()
	var hosts []string
	for _, s := range d.sites {
		if s.Kind == sites.KindNode && d.cfg.DevAuto[s.Name] {
			hosts = append(hosts, s.Host)
		}
	}
	d.mu.Unlock()
	for _, h := range hosts {
		if err := d.startDev(h); err != nil {
			logf("start with laman: %v", err)
		}
	}
}

func (d *daemon) devRoutes(mux *http.ServeMux) {
	site := func(r *http.Request) (string, bool) {
		var req struct{ Site string }
		if err := readJSON(r, &req); err != nil || req.Site == "" {
			return "", false
		}
		return req.Site, true
	}
	mux.HandleFunc("POST /api/dev/start", func(w http.ResponseWriter, r *http.Request) {
		host, ok := site(r)
		if !ok {
			fail(w, http.StatusBadRequest, "site required")
			return
		}
		if err := d.startDev(host); err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		writeJSON(w, d.devState(host))
	})
	mux.HandleFunc("POST /api/dev/stop", func(w http.ResponseWriter, r *http.Request) {
		host, ok := site(r)
		if !ok {
			fail(w, http.StatusBadRequest, "site required")
			return
		}
		d.stopDev(host)
		writeJSON(w, d.devState(host))
	})
	mux.HandleFunc("POST /api/dev/auto", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Site string
			On   bool
		}
		if err := readJSON(r, &req); err != nil {
			fail(w, http.StatusBadRequest, "bad request")
			return
		}
		s, ok := d.findSite(req.Site)
		if !ok || s.Kind != sites.KindNode {
			fail(w, http.StatusBadRequest, "%s is not a Node app", req.Site)
			return
		}
		d.updateConfig(w, func(c *config.Config) error {
			if req.On {
				if c.DevAuto == nil {
					c.DevAuto = map[string]bool{}
				}
				c.DevAuto[s.Name] = true
			} else {
				delete(c.DevAuto, s.Name)
			}
			return nil
		})
	})
	// The output of one dev server, newest last.
	mux.HandleFunc("GET /api/dev/log", func(w http.ResponseWriter, r *http.Request) {
		host := r.URL.Query().Get("site")
		d.mu.Lock()
		s := d.devs[host]
		d.mu.Unlock()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if s == nil {
			io.WriteString(w, "(not started yet)")
			return
		}
		s.mu.Lock()
		text := strings.Join(s.tail, "\n")
		s.mu.Unlock()
		io.WriteString(w, text)
	})
	// `laman proxy` from the dashboard: target "" removes.
	mux.HandleFunc("POST /api/proxy", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Name, Target string }
		if err := readJSON(r, &req); err != nil {
			fail(w, http.StatusBadRequest, "bad request")
			return
		}
		d.updateConfig(w, func(c *config.Config) error {
			_, err := c.SetProxy(req.Name, req.Target)
			return err
		})
	})
}
