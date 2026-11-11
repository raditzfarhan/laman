package daemon

import (
	"bufio"
	"context"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"

	"laman/internal/config"
)

// The dashboard's "Share on your own domain": it runs `laman share setup`
// like the command line does, and passes on the Cloudflare sign-in link
// as soon as cloudflared prints it, in case the browser did not open.

type shareSetup struct {
	mu       sync.Mutex
	running  bool
	step     string
	loginURL string
	err      string
	cancel   context.CancelFunc
}

type shareSetupState struct {
	Domain    string `json:"domain,omitempty"`
	Pattern   string `json:"pattern"`
	Example   string `json:"example,omitempty"` // e.g. blog.example.com
	Connected bool   `json:"connected"`         // domain, tunnel and sign-in all in place
	Running   bool   `json:"running"`
	Step      string `json:"step,omitempty"`
	LoginURL  string `json:"loginUrl,omitempty"`
	Error     string `json:"error,omitempty"`
}

// What the dashboard may pass on to `laman share setup` (it checks the
// rest); neither may look like a flag.
var (
	setupDomain  = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,252}$`)
	setupPattern = regexp.MustCompile(`^[a-z0-9{][a-z0-9{}-]{0,62}$`)
)

var loginLink = regexp.MustCompile(`https://dash\.cloudflare\.com/\S+`)

func (d *daemon) shareSetupState() shareSetupState {
	cfg, _ := config.Load()
	st := shareSetupState{Pattern: "{site}"}
	if cfg != nil {
		st.Domain = cfg.ShareDomain
		if cfg.SharePattern != "" {
			st.Pattern = cfg.SharePattern
		}
		st.Example = cfg.ShareHost("blog")
		_, certErr := os.Stat(config.ShareCert())
		_, credErr := os.Stat(config.ShareCredentials())
		st.Connected = cfg.ShareDomain != "" && cfg.ShareTunnel != "" && certErr == nil && credErr == nil
	}
	s := &d.setup
	s.mu.Lock()
	st.Running, st.Step, st.LoginURL, st.Error = s.running, s.step, s.loginURL, s.err
	s.mu.Unlock()
	return st
}

func (d *daemon) startShareSetup(domain, pattern string) error {
	s := &d.setup
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return errBusy
	}
	ctx, cancel := context.WithCancel(d.ctx)
	s.running, s.step, s.loginURL, s.err, s.cancel = true, "Starting…", "", "", cancel
	s.mu.Unlock()

	exe, err := os.Executable()
	if err != nil {
		cancel()
		return err
	}
	args := []string{"share", "setup", domain}
	if pattern != "" {
		args = append(args, "--pattern", pattern)
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	noWindow(cmd)
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		cancel()
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		return err
	}
	go func() {
		var last string
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			last = line
			s.mu.Lock()
			switch {
			case loginLink.MatchString(line):
				s.loginURL = loginLink.FindString(line)
				s.step = "Waiting for you to approve in the browser…"
			case strings.Contains(line, "Installing cloudflared"):
				s.step = "Downloading cloudflared…"
			case strings.Contains(line, "Created Cloudflare tunnel"):
				s.step = "Created laman's tunnel…"
			case strings.Contains(line, "You have successfully logged in"), strings.Contains(line, "saved"):
				s.step = "Signed in. Creating laman's tunnel…"
			}
			s.mu.Unlock()
		}
		err := cmd.Wait()
		s.mu.Lock()
		s.running, s.cancel = false, nil
		switch {
		case ctx.Err() != nil:
			s.step, s.err = "", "Cancelled."
		case err != nil:
			s.step, s.err = "", strings.TrimPrefix(last, "laman: ")
		default:
			s.step, s.loginURL = "Connected.", ""
		}
		s.mu.Unlock()
	}()
	return nil
}

func (d *daemon) shareSetupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/share/setup", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, d.shareSetupState())
	})
	mux.HandleFunc("POST /api/share/setup", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Domain, Pattern string }
		if err := readJSON(r, &req); err != nil || req.Domain == "" {
			fail(w, http.StatusBadRequest, "domain required")
			return
		}
		domain := strings.ToLower(strings.TrimSpace(req.Domain))
		if !setupDomain.MatchString(domain) || (req.Pattern != "" && !setupPattern.MatchString(req.Pattern)) {
			fail(w, http.StatusBadRequest, "invalid domain or pattern")
			return
		}
		if err := d.startShareSetup(domain, req.Pattern); err != nil {
			fail(w, http.StatusConflict, "%v", err)
			return
		}
		writeJSON(w, d.shareSetupState())
	})
	mux.HandleFunc("POST /api/share/setup/cancel", func(w http.ResponseWriter, r *http.Request) {
		d.setup.mu.Lock()
		if d.setup.cancel != nil {
			d.setup.cancel()
		}
		d.setup.mu.Unlock()
		writeJSON(w, struct{ OK bool }{true})
	})
	mux.HandleFunc("POST /api/share/setup/off", func(w http.ResponseWriter, r *http.Request) {
		exe, err := os.Executable()
		if err == nil {
			cmd := exec.Command(exe, "share", "setup", "off")
			noWindow(cmd)
			err = cmd.Run()
		}
		if err != nil {
			fail(w, http.StatusInternalServerError, "%v", err)
			return
		}
		d.setup.mu.Lock()
		d.setup.step, d.setup.err = "", ""
		d.setup.mu.Unlock()
		writeJSON(w, d.shareSetupState())
	})
}
