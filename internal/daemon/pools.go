package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"laman/internal/config"
)

// pool is a set of php-cgi workers for one PHP version.
type pool struct {
	version  string
	slot     int
	ports    []int
	services []*Service
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// startPool launches workers for version on the first free block of ports.
// Slot 0 is cfg.PHPBasePort..+workers-1; slot n is n blocks higher.
func (d *daemon) startPool(version string) (*pool, error) {
	used := map[int]bool{}
	for _, p := range d.pools {
		used[p.slot] = true
	}
	workers := d.cfg.PHPWorkers
	for slot := 0; slot < 50; slot++ {
		if used[slot] {
			continue
		}
		ports := make([]int, workers)
		for i := range ports {
			ports[i] = d.cfg.PHPBasePort + slot*workers + i
		}
		if !portsFree(ports) {
			continue
		}
		return d.launch(version, slot, ports), nil
	}
	return nil, fmt.Errorf("no free ports for a PHP %s pool above %d", version, d.cfg.PHPBasePort)
}

func portsFree(ports []int) bool {
	for _, p := range ports {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			return false
		}
		ln.Close()
	}
	return true
}

func (d *daemon) launch(version string, slot int, ports []int) *pool {
	dir := filepath.Join(config.BinDir("php"), version)
	phpCGI := filepath.Join(dir, "php-cgi.exe")
	ini := filepath.Join(dir, "php.ini")
	smtpPort := d.cfg.SMTPPort
	ctx, cancel := context.WithCancel(d.ctx)
	p := &pool{version: version, slot: slot, ports: ports, cancel: cancel}
	for i, port := range ports {
		port := port
		s := &Service{
			Name:    fmt.Sprintf("php%s-%d", shortVersion(version), i+1),
			LogName: "php",
			Build: func() *exec.Cmd {
				cmd := exec.Command(phpCGI, "-b", fmt.Sprintf("127.0.0.1:%d", port), "-c", ini,
					// On Windows OPcache names its shared memory after the PHP
					// build, so a second copy of the same version (another
					// laman home, `php artisan serve`) would attach to it and
					// crash. A per-install id keeps caches apart.
					"-d", "opcache.cache_id="+cacheID(dir),
					// Pick up file edits on the very next request.
					"-d", "opcache.revalidate_freq=0",
					// PHP's mail() goes to Mailpit, never to a real server.
					"-d", "SMTP=127.0.0.1", "-d", "smtp_port="+strconv.Itoa(smtpPort),
					"-d", "sendmail_from=laman@localhost")
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "PHP_FCGI_MAX_REQUESTS=0")
				return cmd
			},
			Stop: killNow,
		}
		p.services = append(p.services, s)
		p.wg.Add(1)
		go func() { defer p.wg.Done(); s.Run(ctx) }()
	}
	logf("started PHP %s pool on ports %v", version, ports)
	return p
}

func (p *pool) stop() {
	p.cancel()
	p.wg.Wait()
	logf("stopped PHP %s pool", p.version)
}

func cacheID(dir string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(dir))))
	return "laman" + hex.EncodeToString(sum[:4])
}

// shortVersion turns "8.4.27-1" into "8.4" for compact service names.
func shortVersion(v string) string {
	dots := 0
	for i, r := range v {
		if r == '.' {
			dots++
			if dots == 2 {
				return v[:i]
			}
		}
	}
	return v
}

// reconcilePools starts a pool for every version in need and returns the
// pools that are no longer needed; the caller stops them once Caddy no
// longer routes to them.
func (d *daemon) reconcilePools(need map[string]bool) (stale []*pool, err error) {
	for v := range need {
		if _, ok := d.pools[v]; ok {
			continue
		}
		p, err := d.startPool(v)
		if err != nil {
			return nil, err
		}
		d.mu.Lock()
		d.pools[v] = p
		d.mu.Unlock()
	}
	for v, p := range d.pools {
		if !need[v] {
			stale = append(stale, p)
			d.mu.Lock()
			delete(d.pools, v)
			d.mu.Unlock()
		}
	}
	return stale, nil
}

// poolServices lists every worker, ordered by PHP version.
func (d *daemon) poolServices() []*Service {
	versions := make([]string, 0, len(d.pools))
	for v := range d.pools {
		versions = append(versions, v)
	}
	sort.Slice(versions, func(i, j int) bool { return config.CompareVersions(versions[i], versions[j]) < 0 })
	var out []*Service
	for _, v := range versions {
		out = append(out, d.pools[v].services...)
	}
	return out
}

// restartPHP restarts the workers of one PHP version so they reread
// php.ini (an extension was turned on or off). Other versions and the
// database servers keep running.
func (d *daemon) restartPHP(version string) error {
	d.refreshMu.Lock()
	d.mu.Lock()
	p := d.pools[version]
	delete(d.pools, version)
	d.mu.Unlock()
	d.refreshMu.Unlock()
	if p == nil {
		return nil // no site uses it right now: the next pool reads php.ini fresh
	}
	logf("restarting PHP %s workers", version)
	p.stop()
	return d.refresh() // starts a fresh pool and points Caddy at it
}
