package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"laman/internal/client"
	"laman/internal/config"
	"laman/internal/daemon"
	"laman/internal/sites"
)

const workerUsage = `usage:
  laman queue  on|off|restart|status|log [site]
  laman reverb on|off|restart|status|log|env [site]`

// parseWorkerArgs checks `laman queue|reverb <action> [site]` and returns
// the action and the optional site name.
func parseWorkerArgs(kind string, args []string) (action, site string, err error) {
	if len(args) == 0 {
		return "status", "", nil
	}
	action = args[0]
	switch action {
	case "on", "off", "restart", "status", "log":
	case "env":
		if kind != "reverb" {
			return "", "", errors.New(workerUsage)
		}
	default:
		return "", "", errors.New(workerUsage)
	}
	if len(args) > 2 {
		return "", "", errors.New(workerUsage)
	}
	if len(args) == 2 {
		site = args[1]
	}
	return action, site, nil
}

func cmdQueue(args []string) error  { return cmdWorker("queue", args) }
func cmdReverb(args []string) error { return cmdWorker("reverb", args) }

// cmdWorker drives a site's queue worker or Reverb server.
func cmdWorker(kind string, args []string) error {
	action, name, err := parseWorkerArgs(kind, args)
	if err != nil {
		return err
	}
	if !client.Running() {
		return errors.New("laman is not running; run `laman start` first")
	}
	cfg, err := loadOrInit()
	if err != nil {
		return err
	}
	var rest []string
	if name != "" {
		rest = []string{name}
	}
	s, err := laravelSite(cfg, rest)
	if err != nil {
		return err
	}
	label := "queue worker"
	if kind == "reverb" {
		label = "Reverb server"
	}
	base := "/api/workers/" + kind
	switch action {
	case "on", "off":
		b, err := client.Do("POST", base+"/switch", map[string]any{"site": s.Host, "on": action == "on"})
		if err != nil {
			return err
		}
		var w daemon.WorkerStatus
		if err := json.Unmarshal(b, &w); err != nil {
			return err
		}
		if action == "off" {
			fmt.Printf("Stopped %s's %s.\n", s.Host, label)
			return nil
		}
		switch w.State {
		case "starting", "running", "waiting":
			fmt.Printf("Started %s's %s.\n", s.Host, label)
		case "crashed":
			msg := fmt.Sprintf("Could not start %s's %s.", s.Host, label)
			if w.Error != "" {
				msg += "\n" + w.Error
			}
			return errors.New(msg)
		}
		printWorker(w)
		if kind == "reverb" {
			reverbAddress(s.Host)
		}
	case "restart":
		b, err := client.Do("POST", base+"/restart", map[string]any{"site": s.Host})
		if err != nil {
			return err
		}
		var w daemon.WorkerStatus
		if err := json.Unmarshal(b, &w); err != nil {
			return err
		}
		fmt.Printf("Restarted %s's %s.\n", s.Host, label)
		printWorker(w)
	case "status":
		return workerStatus(kind, label, s)
	case "log":
		b, err := client.Do("GET", base+"/log?site="+url.QueryEscape(s.Host), nil)
		if err != nil {
			return err
		}
		fmt.Println(strings.TrimRight(string(b), "\r\n"))
	case "env":
		b, err := client.Do("POST", "/api/reverb/env", map[string]any{"site": s.Host})
		if err != nil {
			return err
		}
		var r struct {
			Changed      []string `json:"changed"`
			ConfigCached bool     `json:"configCached"`
		}
		if err := json.Unmarshal(b, &r); err != nil {
			return err
		}
		if len(r.Changed) == 0 {
			fmt.Printf("%s's .env is already set up for Reverb.\n", s.Host)
		} else {
			fmt.Printf("Updated %s's .env: %s\n", s.Host, strings.Join(r.Changed, ", "))
			fmt.Println("Restart Vite (npm run dev) or rebuild so the browser picks up the new values.")
		}
		if r.ConfigCached {
			fmt.Println("Config is cached — run php artisan config:clear.")
		}
	}
	return nil
}

// printWorker prints what a worker status says, one short line each.
func printWorker(w daemon.WorkerStatus) {
	if w.State == "" {
		return
	}
	line := "State: " + w.State
	if w.Port > 0 {
		line += fmt.Sprintf(", port %d", w.Port)
	}
	if w.Uptime != "" {
		line += ", up " + w.Uptime
	}
	fmt.Println(line)
	if w.Command != "" {
		fmt.Println("Command: " + w.Command)
	}
	if w.Waiting != "" {
		fmt.Println("Waiting: " + w.Waiting)
	}
	if w.Note != "" {
		fmt.Println(w.Note)
	}
	if w.Error != "" {
		fmt.Println("Error: " + w.Error)
	}
}

type reverbFact struct {
	On           bool                `json:"on"`
	URL          string              `json:"url"`
	EnvOK        bool                `json:"envOK"`
	EnvMismatch  []string            `json:"envMismatch"`
	NeedsInstall bool                `json:"needsInstall"`
	Installed    bool                `json:"installed"`
	Port         int                 `json:"port"`
	Status       daemon.WorkerStatus `json:"status"`
}

type queueFact struct {
	On     bool                `json:"on"`
	Note   string              `json:"note"`
	Status daemon.WorkerStatus `json:"status"`
}

func siteWorkers(host string) (queue queueFact, reverb reverbFact, err error) {
	b, err := client.Do("GET", "/api/sites/detail?site="+url.QueryEscape(host), nil)
	if err != nil {
		return
	}
	var d struct {
		Workers struct {
			Queue  queueFact  `json:"queue"`
			Reverb reverbFact `json:"reverb"`
		} `json:"workers"`
	}
	if err = json.Unmarshal(b, &d); err != nil {
		return
	}
	return d.Workers.Queue, d.Workers.Reverb, nil
}

func workerStatus(kind, label string, s sites.Site) error {
	q, r, err := siteWorkers(s.Host)
	if err != nil {
		return err
	}
	if kind == "queue" {
		if !q.On {
			fmt.Printf("%s's %s is off.\n", s.Host, label)
			if q.Note != "" {
				fmt.Println(q.Note)
			}
			return nil
		}
		fmt.Printf("%s's %s is on.\n", s.Host, label)
		printWorker(q.Status)
		return nil
	}
	if !r.On {
		fmt.Printf("%s's %s is off.\n", s.Host, label)
		if r.NeedsInstall {
			fmt.Println("Run php artisan reverb:install first.")
		}
		return nil
	}
	fmt.Printf("%s's %s is on.\n", s.Host, label)
	printWorker(r.Status)
	if r.URL != "" {
		fmt.Println("Address: " + r.URL)
	}
	envHint(r)
	return nil
}

func envHint(r reverbFact) {
	if len(r.EnvMismatch) > 0 {
		fmt.Printf(".env differs (%s); run `laman reverb env`.\n", strings.Join(r.EnvMismatch, ", "))
	}
}

// reverbAddress prints the address the browser connects to, and suggests
// `laman reverb env` when the site's .env doesn't match it.
func reverbAddress(host string) {
	_, r, err := siteWorkers(host)
	if err != nil {
		fmt.Printf("(could not read the address: %v)\n", err)
		return
	}
	if r.URL != "" {
		fmt.Println("Address: " + r.URL)
	}
	envHint(r)
}

// laravelSite is nodeSite for any site whose folder has an artisan file.
func laravelSite(cfg *config.Config, args []string) (sites.Site, error) {
	name := ""
	if len(args) == 1 {
		name = args[0]
	}
	s, err := siteHere(cfg, name)
	if err != nil {
		return s, err
	}
	if _, err := os.Stat(filepath.Join(s.Path, "artisan")); err != nil {
		return s, fmt.Errorf("%s is not a Laravel app (no artisan file in %s)", s.Host, s.Path)
	}
	return s, nil
}
