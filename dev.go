package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"laman/internal/client"
	"laman/internal/config"
	"laman/internal/daemon"
	"laman/internal/sites"
)

const devUsage = `usage:
  laman dev                     list Node apps and their dev servers
  laman dev start [site]        start a site's dev server (default: this folder's)
  laman dev stop [site]         stop it
  laman dev log [site]          its latest output
  laman dev auto on|off [site]  start it whenever laman starts`

// cmdDev runs Node apps' dev servers behind their .test names.
func cmdDev(args []string) error {
	sub := ""
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	if !client.Running() {
		return errors.New("laman is not running; run `laman start` first")
	}
	cfg, err := loadOrInit()
	if err != nil {
		return err
	}
	st, err := daemonStatus()
	if err != nil {
		return err
	}
	switch sub {
	case "", "list", "ls":
		return listDev(st)
	case "start", "stop", "log", "logs":
		if len(args) > 1 {
			return errors.New(devUsage)
		}
		s, err := nodeSite(cfg, args)
		if err != nil {
			return err
		}
		switch sub {
		case "start":
			return startDev(s)
		case "stop":
			if _, err := client.Do("POST", "/api/dev/stop", map[string]string{"site": s.Host}); err != nil {
				return err
			}
			fmt.Printf("Stopped %s's dev server.\n", s.Host)
			return nil
		default:
			b, err := client.Do("GET", "/api/dev/log?site="+s.Host, nil)
			if err != nil {
				return err
			}
			fmt.Println(string(b))
			return nil
		}
	case "auto":
		if len(args) < 1 || len(args) > 2 || (args[0] != "on" && args[0] != "off") {
			return errors.New(devUsage)
		}
		s, err := nodeSite(cfg, args[1:])
		if err != nil {
			return err
		}
		if _, err := client.Do("POST", "/api/dev/auto", map[string]any{"site": s.Host, "on": args[0] == "on"}); err != nil {
			return err
		}
		if args[0] == "on" {
			fmt.Printf("%s's dev server now starts with laman.\n", s.Host)
		} else {
			fmt.Printf("%s's dev server no longer starts with laman.\n", s.Host)
		}
		return nil
	}
	return errors.New(devUsage)
}

func daemonStatus() (daemon.Status, error) {
	var st daemon.Status
	b, err := client.Do("GET", "/status", nil)
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(b, &st)
}

func nodeSite(cfg *config.Config, args []string) (sites.Site, error) {
	name := ""
	if len(args) == 1 {
		name = args[0]
	}
	s, err := siteHere(cfg, name)
	if err != nil {
		return s, err
	}
	if s.Kind != sites.KindNode {
		return s, fmt.Errorf("%s is not a Node app with a dev script (laman sees it as %s)", s.Host, s.Kind)
	}
	return s, nil
}

// startDev starts the server and waits until it answers, printing its
// output if it stops instead.
func startDev(s sites.Site) error {
	if _, err := client.Do("POST", "/api/dev/start", map[string]string{"site": s.Host}); err != nil {
		return err
	}
	fmt.Printf("Starting %s (%s)…\n", s.Host, s.DevCmd)
	for deadline := time.Now().Add(3 * time.Minute); time.Now().Before(deadline); time.Sleep(time.Second) {
		st, err := daemonStatus()
		if err != nil {
			return err
		}
		for _, d := range st.Dev {
			if d.Host != s.Host {
				continue
			}
			switch d.State {
			case "running":
				fmt.Printf("Running on port %d: https://%s/\n", d.Port, s.Host)
				return nil
			case "crashed":
				return fmt.Errorf("the dev server stopped:\n%s", d.Error)
			case "stopped":
				return errors.New("the dev server was stopped")
			}
		}
	}
	fmt.Printf("Still starting; it opens at https://%s/ once ready. `laman dev log %s` shows its output.\n", s.Host, s.Name)
	return nil
}

func listDev(st daemon.Status) error {
	state := map[string]daemon.DevStatus{}
	for _, d := range st.Dev {
		state[d.Host] = d
	}
	var list []sites.Site
	for _, s := range st.Sites {
		if s.Kind == sites.KindNode {
			list = append(list, s)
		}
	}
	if len(list) == 0 {
		fmt.Println("No Node apps found. A project folder with a package.json dev script shows up here.")
		return nil
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	for _, s := range list {
		d := state[s.Host]
		run := "stopped"
		switch d.State {
		case "running":
			run = fmt.Sprintf("running :%d", d.Port)
		case "starting", "crashed":
			run = d.State
		}
		auto := ""
		if st.DevAuto[s.Name] {
			auto = " (starts with laman)"
		}
		fmt.Printf("%-36s %-12s %-16s %s%s\n", s.Host, s.Framework, run, s.DevCmd, auto)
	}
	return nil
}

const proxyUsage = `usage:
  laman proxy                    list proxies
  laman proxy <name> <port>      serve <name>.test from a port on this computer
  laman proxy remove <name>      remove one`

// cmdProxy points a .test name at a port: a Docker container, or a server
// laman doesn't start itself.
func cmdProxy(args []string) error {
	cfg, err := loadOrInit()
	if err != nil {
		return err
	}
	switch {
	case len(args) == 0:
		if len(cfg.Proxies) == 0 {
			fmt.Println("No proxies. `laman proxy <name> <port>` adds one.")
			return nil
		}
		names := make([]string, 0, len(cfg.Proxies))
		for n := range cfg.Proxies {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Printf("%-30s -> %s\n", n+"."+cfg.TLD, cfg.Proxies[n])
		}
		return nil
	case len(args) == 2 && (args[0] == "remove" || args[0] == "rm"):
		return saveProxy(cfg, args[1], "")
	case len(args) == 2 && !strings.HasPrefix(args[0], "-"):
		return saveProxy(cfg, args[0], args[1])
	}
	return errors.New(proxyUsage)
}

func saveProxy(cfg *config.Config, name, target string) error {
	name, err := cfg.SetProxy(name, target)
	if err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	if client.Running() {
		if _, err := client.Do("POST", "/reload", nil); err != nil {
			return err
		}
	}
	if target == "" {
		fmt.Printf("Removed %s.%s.\n", name, cfg.TLD)
		return nil
	}
	fmt.Printf("https://%s.%s/ -> %s\n", name, cfg.TLD, cfg.Proxies[name])
	fmt.Println("New names need a hosts entry: run `laman hosts` if the address doesn't open.")
	return nil
}
