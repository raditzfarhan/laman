package daemon

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"laman/internal/config"
	"laman/internal/install"
)

// Shares on the user's own domain go through one Cloudflare named tunnel
// (made by `laman share setup`). It delivers every request to a single
// local port, where Caddy routes by host name to the shared site, so a
// share starts or stops with a config change while the tunnel keeps
// running. The tunnel runs while at least one such share exists.

type namedTunnel struct {
	port   int
	cancel context.CancelFunc
	ready  chan struct{} // closed once Cloudflare has a connection
	failed chan string   // the reason, if it exits before that
}

// ShareLabel is a valid address name: one DNS label.
var ShareLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func (d *daemon) domainPort() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.domain == nil {
		return 0
	}
	return d.domain.port
}

func (d *daemon) startDomainShare(cfg *config.Config, exe, host, label string) (*share, error) {
	if !ShareLabel.MatchString(label) {
		return nil, fmt.Errorf("%q cannot be an address name: use lowercase letters, digits and dashes", label)
	}
	hostname := cfg.ShareHost(label)
	if hostname == "" || strings.Count(hostname, ".") > strings.Count(cfg.ShareDomain, ".")+1 {
		return nil, fmt.Errorf("%s is more than one level below %s, which Cloudflare's free certificate does not cover; change the pattern with `laman share setup %s --pattern {site}-dev`",
			hostname, cfg.ShareDomain, cfg.ShareDomain)
	}
	d.mu.Lock()
	for _, s := range d.shares {
		if s.hostname == hostname {
			d.mu.Unlock()
			return nil, fmt.Errorf("https://%s is already in use for %s", hostname, s.Host)
		}
	}
	d.mu.Unlock()

	if _, err := os.Stat(config.ShareCert()); err != nil {
		return nil, fmt.Errorf("laman is not signed in to Cloudflare; run `laman share setup %s` (or `laman share --quick` for a random link)", cfg.ShareDomain)
	}
	// Point the name at the tunnel. Repeating it for a name already routed
	// there is fine; a name used for something else is refused by Cloudflare.
	if out, err := cloudflared(exe, "route", "dns", cfg.ShareTunnel, hostname); err != nil {
		return nil, fmt.Errorf("could not point %s at laman's tunnel: %s", hostname, lastLines(out, 3))
	}

	t, err := d.ensureDomainTunnel(exe, cfg.ShareTunnel)
	if err != nil {
		return nil, err
	}
	sh := &share{Host: host, URL: "https://" + hostname, Started: time.Now(), cancel: func() {}, hostname: hostname}
	d.mu.Lock()
	d.shares[host] = sh
	d.mu.Unlock()
	if err := d.refresh(); err != nil {
		d.dropShare(sh)
		return nil, err
	}
	select {
	case <-t.ready:
	case why := <-t.failed:
		d.dropShare(sh)
		return nil, fmt.Errorf("laman's tunnel stopped: %s", why)
	case <-time.After(45 * time.Second):
		d.dropShare(sh)
		return nil, fmt.Errorf("Cloudflare did not connect laman's tunnel within 45 seconds; see %s",
			filepath.Join(config.LogDir(), "share-domain.log"))
	}
	logf("sharing %s at %s", host, sh.URL)
	return sh, nil
}

// cloudflared runs a cloudflared management command with laman's own
// sign-in and an empty config, so ~/.cloudflared is neither read nor written.
// args follow "tunnel", e.g. "route", "dns", id, hostname.
func cloudflared(exe string, args ...string) (string, error) {
	cmd, err := install.CloudflaredCommand(exe, args...)
	if err != nil {
		return "", err
	}
	noWindow(cmd)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func emptyCloudflaredConfig() (string, error) { return install.CloudflaredEmptyConfig() }

// ensureDomainTunnel starts the named tunnel if it is not running.
func (d *daemon) ensureDomainTunnel(exe, id string) (*namedTunnel, error) {
	d.mu.Lock()
	if d.domain != nil {
		t := d.domain
		d.mu.Unlock()
		return t, nil
	}
	d.mu.Unlock()

	port, err := freePort()
	if err != nil {
		return nil, err
	}
	conf := filepath.Join(config.ShareDir(), "laman-tunnel.yml")
	yml := fmt.Sprintf("# Written by laman for `laman share` on your domain.\ntunnel: %s\ncredentials-file: %s\ningress:\n  - service: http://127.0.0.1:%d\n",
		id, filepath.ToSlash(config.ShareCredentials()), port)
	if err := os.WriteFile(conf, []byte(yml), 0o600); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(d.ctx)
	t := &namedTunnel{port: port, cancel: cancel, ready: make(chan struct{}), failed: make(chan string, 1)}
	cmd := exec.CommandContext(ctx, exe, "tunnel", "--config", conf, "--no-autoupdate", "run", id)
	cmd.Env = append(os.Environ(), "HOME="+config.ShareDir())
	noWindow(cmd)
	logFile, _ := os.Create(filepath.Join(config.LogDir(), "share-domain.log"))
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	cmd.Stdout = logFile
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start cloudflared: %w", err)
	}
	d.mu.Lock()
	d.domain = t
	d.mu.Unlock()

	var lastErr string
	scanned := make(chan struct{})
	go func() {
		defer close(scanned)
		sc := bufio.NewScanner(stderr)
		readyOnce := false
		for sc.Scan() {
			line := sc.Text()
			if logFile != nil {
				io.WriteString(logFile, line+"\n")
			}
			if strings.Contains(line, " ERR ") {
				lastErr = line
			}
			if !readyOnce && strings.Contains(line, "Registered tunnel connection") {
				readyOnce = true
				close(t.ready)
			}
		}
	}()
	go func() {
		cmd.Wait()
		<-scanned
		if logFile != nil {
			logFile.Close()
		}
		select {
		case t.failed <- strings.TrimSpace(lastErr + " (cloudflared exited)"):
		default:
		}
		d.mu.Lock()
		if d.domain == t {
			d.domain = nil
		}
		var gone []*share
		for _, s := range d.shares {
			if s.hostname != "" {
				gone = append(gone, s)
			}
		}
		d.mu.Unlock()
		for _, s := range gone {
			d.dropShare(s)
		}
		logf("own-domain tunnel stopped")
	}()
	return t, nil
}

// stopDomainTunnelIfIdle stops the named tunnel once nothing uses it.
func (d *daemon) stopDomainTunnelIfIdle() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.domain == nil {
		return
	}
	for _, s := range d.shares {
		if s.hostname != "" {
			return
		}
	}
	d.domain.cancel()
	d.domain = nil
}
