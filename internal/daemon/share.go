package daemon

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"laman/internal/caddyconf"
	"laman/internal/config"
	"laman/internal/install"
	"laman/internal/sites"
)

// A share makes one site reachable from the internet through a Cloudflare
// quick tunnel (a random https://….trycloudflare.com address, no account).
// Caddy serves the site on a private port of its own, and cloudflared
// forwards the tunnel there. Apps (DBZuper, Adminer, …) are never shared.
type share struct {
	Host    string    `json:"host"`
	URL     string    `json:"url"` // "" while the tunnel is starting
	Started time.Time `json:"started"`
	port    int
	cancel  context.CancelFunc
	// hostname is set for a share on the user's own domain, served through
	// the shared named tunnel instead of a quick tunnel of its own.
	hostname string
}

// ShareInfo is a share as `laman status` and the dashboard see it.
type ShareInfo struct {
	Host    string `json:"host"`
	URL     string `json:"url,omitempty"`
	Started string `json:"started"`
	Own     bool   `json:"own,omitempty"` // on the user's domain: the address stays
}

var tunnelURL = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

// shareLock serialises starting shares, so two clicks make one tunnel.
var shareLock sync.Mutex

func (d *daemon) shareInfo() []ShareInfo {
	var out []ShareInfo
	for _, s := range d.shares {
		out = append(out, ShareInfo{Host: s.Host, URL: s.URL, Started: s.Started.Format(time.RFC3339), Own: s.hostname != ""})
	}
	return out
}

// sharePorts lists the shares for the Caddy config. Callers hold no lock.
func (d *daemon) sharePorts() []caddyconf.Share {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []caddyconf.Share
	for _, s := range d.shares {
		out = append(out, caddyconf.Share{Host: s.Host, Port: s.port, Hostname: s.hostname})
	}
	return out
}

// startShare shares a site (by host or folder name) and returns its public
// address once the tunnel is up: on the user's domain when one is set up
// (as names the address, else the site's), else a random quick-tunnel
// link; quick forces the latter.
func (d *daemon) startShare(name, as string, quick bool) (*share, error) {
	shareLock.Lock()
	defer shareLock.Unlock()

	d.mu.Lock()
	var site *sites.Site
	for i := range d.sites {
		if s := &d.sites[i]; s.Host == name || s.Name == name {
			site = s
			break
		}
	}
	if site == nil {
		d.mu.Unlock()
		return nil, fmt.Errorf("no site called %q", name)
	}
	host, app := site.Host, site.App
	if s := d.shares[host]; s != nil {
		d.mu.Unlock()
		return s, nil
	}
	d.mu.Unlock()
	if app != "" {
		return nil, fmt.Errorf("%s is laman's %s app, which has full access to your local data: apps are never shared", host, app)
	}

	exe := install.CloudflaredExe()
	if exe == "" {
		if _, err := install.Cloudflared(); err != nil {
			return nil, err
		}
		if exe = install.CloudflaredExe(); exe == "" {
			return nil, errors.New("cloudflared did not install")
		}
	}
	cfg, err := config.Load() // fresh: `laman share setup` may have run since start
	if err != nil {
		return nil, err
	}
	if !quick && cfg.ShareDomain != "" && cfg.ShareTunnel != "" {
		label := as
		if label == "" {
			label = strings.TrimSuffix(host, "."+cfg.TLD)
		}
		return d.startDomainShare(cfg, exe, host, label)
	}
	if as != "" {
		return nil, errors.New("--as needs your own domain: run `laman share setup <domain>` first")
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(d.ctx)
	sh := &share{Host: host, Started: time.Now(), port: port, cancel: cancel}
	d.mu.Lock()
	d.shares[host] = sh
	d.mu.Unlock()
	// Caddy listens on the share's port before the tunnel points at it.
	if err := d.refresh(); err != nil {
		d.dropShare(sh)
		return nil, err
	}

	// cloudflared reads ~/.cloudflared/config.yml when it exists, and its
	// routing rules then replace --url (quick tunnels answer 404). Point it
	// at an empty config of laman's own; the user's stays untouched.
	conf, err := emptyCloudflaredConfig()
	if err != nil {
		d.dropShare(sh)
		return nil, err
	}
	cmd := exec.CommandContext(ctx, exe, "tunnel", "--config", conf, "--no-autoupdate", "--url", fmt.Sprintf("http://127.0.0.1:%d", port))
	noWindow(cmd)
	// Logged per share, so a failing tunnel can be looked into.
	logPath := filepath.Join(config.LogDir(), "share-"+safeFile(host)+".log")
	logFile, _ := os.Create(logPath)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		d.dropShare(sh)
		return nil, err
	}
	cmd.Stdout = logFile
	if err := cmd.Start(); err != nil {
		d.dropShare(sh)
		return nil, fmt.Errorf("start cloudflared: %w", err)
	}

	found := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := sc.Text()
			if logFile != nil {
				io.WriteString(logFile, line+"\n")
			}
			if u := tunnelURL.FindString(line); u != "" {
				select {
				case found <- u:
				default:
				}
			}
		}
	}()
	go func() {
		cmd.Wait()
		if logFile != nil {
			logFile.Close()
		}
		d.dropShare(sh)
		logf("share of %s ended", host)
	}()

	select {
	case u := <-found:
		d.mu.Lock()
		sh.URL = u
		d.mu.Unlock()
		logf("sharing %s at %s", host, u)
		return sh, nil
	case <-time.After(60 * time.Second):
	case <-ctx.Done():
	}
	cancel()
	tail, _ := os.ReadFile(logPath)
	return nil, fmt.Errorf("Cloudflare did not open a tunnel for %s within a minute.%s", host, lastLines(string(tail), 4))
}

// stopShare ends a share; false when the site was not shared.
func (d *daemon) stopShare(name string) bool {
	d.mu.Lock()
	var sh *share
	for host, s := range d.shares {
		if host == name || strings.TrimSuffix(host, "."+d.cfg.TLD) == name {
			sh = s
		}
	}
	d.mu.Unlock()
	if sh == nil {
		return false
	}
	sh.cancel()
	d.dropShare(sh)
	return true
}

// dropShare forgets a share and closes its Caddy port; safe to call twice.
func (d *daemon) dropShare(sh *share) {
	sh.cancel()
	d.mu.Lock()
	if d.shares[sh.Host] != sh {
		d.mu.Unlock()
		return
	}
	delete(d.shares, sh.Host)
	d.mu.Unlock()
	if d.ctx.Err() != nil {
		return // laman is stopping
	}
	if sh.hostname != "" {
		d.stopDomainTunnelIfIdle()
	}
	if err := d.refresh(); err != nil {
		logf("refresh after share: %v", err)
	}
}

func (d *daemon) shareRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/share", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Site, As string
			Quick    bool
		}
		if err := readJSON(r, &req); err != nil || req.Site == "" {
			fail(w, http.StatusBadRequest, "site required")
			return
		}
		sh, err := d.startShare(req.Site, req.As, req.Quick)
		if err != nil {
			fail(w, http.StatusConflict, "%v", err)
			return
		}
		d.mu.Lock()
		info := ShareInfo{Host: sh.Host, URL: sh.URL, Started: sh.Started.Format(time.RFC3339), Own: sh.hostname != ""}
		d.mu.Unlock()
		writeJSON(w, info)
	})
	mux.HandleFunc("POST /api/share/stop", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Site string }
		if err := readJSON(r, &req); err != nil || req.Site == "" {
			fail(w, http.StatusBadRequest, "site required")
			return
		}
		if !d.stopShare(req.Site) {
			fail(w, http.StatusNotFound, "%s is not shared", req.Site)
			return
		}
		writeJSON(w, struct{ OK bool }{true})
	})
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

var unsafeFile = regexp.MustCompile(`[^a-zA-Z0-9.-]`)

func safeFile(s string) string { return unsafeFile.ReplaceAllString(s, "_") }

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	if len(lines) == 0 || lines[0] == "" {
		return ""
	}
	return "\n" + strings.Join(lines, "\n")
}
