package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"laman/internal/config"
)

// Service supervises one long-running process and restarts it if it exits.
type Service struct {
	Name    string
	LogName string
	Build   func() *exec.Cmd
	// Stop asks the process to exit. done closes when it has. Stop must
	// return once the process is gone; it may kill it after a grace period.
	Stop func(cmd *exec.Cmd, done <-chan struct{})

	mu       sync.Mutex
	state    string
	pid      int
	restarts int
	lastErr  string
	since    time.Time
}

type ServiceStatus struct {
	Name      string `json:"name"`
	State     string `json:"state"`
	PID       int    `json:"pid,omitempty"`
	Restarts  int    `json:"restarts"`
	LastError string `json:"lastError,omitempty"`
	Uptime    string `json:"uptime,omitempty"`
}

func (s *Service) Status() ServiceStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := ServiceStatus{Name: s.Name, State: s.state, PID: s.pid, Restarts: s.restarts, LastError: s.lastErr}
	if st.State == "" {
		st.State = "starting"
	}
	if s.state == "running" {
		st.Uptime = time.Since(s.since).Round(time.Second).String()
	}
	return st
}

func (s *Service) set(f func()) { s.mu.Lock(); f(); s.mu.Unlock() }

func (s *Service) Run(ctx context.Context) {
	logName := s.LogName
	if logName == "" {
		logName = s.Name
	}
	logf, err := os.OpenFile(filepath.Join(config.LogDir(), logName+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		s.set(func() { s.state, s.lastErr = "failed", err.Error() })
		return
	}
	defer logf.Close()

	backoff := time.Second
	for {
		cmd := s.Build()
		cmd.Stdout, cmd.Stderr = logf, logf
		noWindow(cmd)
		fmt.Fprintf(logf, "\n[laman %s] starting %s\n", time.Now().Format(time.RFC3339), cmd.String())
		started := time.Now()
		if err := cmd.Start(); err != nil {
			s.set(func() { s.state, s.lastErr = "failed", err.Error() })
		} else {
			s.set(func() { s.state, s.pid, s.since = "running", cmd.Process.Pid, started })
			done := make(chan struct{})
			var waitErr error
			go func() { waitErr = cmd.Wait(); close(done) }()
			select {
			case <-ctx.Done():
				s.set(func() { s.state = "stopping" })
				s.Stop(cmd, done)
				<-done
				s.set(func() { s.state, s.pid = "stopped", 0 })
				return
			case <-done:
				msg := "exited"
				if waitErr != nil {
					msg = waitErr.Error()
				}
				fmt.Fprintf(logf, "[laman] %s %s\n", s.Name, msg)
				s.set(func() { s.state, s.pid, s.lastErr = "restarting", 0, msg; s.restarts++ })
			}
			if time.Since(started) > 30*time.Second {
				backoff = time.Second
			}
		}
		select {
		case <-ctx.Done():
			s.set(func() { s.state = "stopped" })
			return
		case <-time.After(backoff):
		}
		if backoff < 10*time.Second {
			backoff *= 2
		}
	}
}

// killAfter waits for done, killing the process if it outlives grace.
func killAfter(cmd *exec.Cmd, done <-chan struct{}, grace time.Duration) {
	select {
	case <-done:
	case <-time.After(grace):
		cmd.Process.Kill()
	}
}

func killNow(cmd *exec.Cmd, done <-chan struct{}) { cmd.Process.Kill() }

// logf writes to the daemon's own log (laman.log). The standard logger is
// not used because Caddy redirects it into its JSON log once it loads.
var logf = log.New(os.Stderr, "", log.LstdFlags).Printf
