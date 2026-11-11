package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"laman/internal/client"
	"laman/internal/config"
	"laman/internal/daemon"
	"laman/internal/install"
)

const shareUsage = `usage:
  laman share [site] [--as name] [--quick]   share a site (default: this folder's)
  laman share stop [site]                    stop sharing it
  laman share list                           what is shared
  laman share setup <domain> [--pattern {site}-dev]   share on your own domain (Cloudflare)
  laman share setup off                      back to random trycloudflare.com links`

// cmdShare makes a site reachable from the internet through Cloudflare:
// on the user's own domain once `laman share setup` ran, else on a random
// trycloudflare.com link.
func cmdShare(args []string) error {
	sub := ""
	if len(args) > 0 && (args[0] == "stop" || args[0] == "list" || args[0] == "setup") {
		sub, args = args[0], args[1:]
	}
	if sub == "setup" {
		return cmdShareSetup(args)
	}

	var as string
	var quick bool
	var rest []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--quick":
			quick = true
		case a == "--as" && i+1 < len(args):
			as, i = args[i+1], i+1
		case strings.HasPrefix(a, "--as="):
			as = strings.TrimPrefix(a, "--as=")
		case strings.HasPrefix(a, "-"):
			return errors.New(shareUsage)
		default:
			rest = append(rest, a)
		}
	}
	if len(rest) > 1 {
		return errors.New(shareUsage)
	}

	if !client.Running() {
		return errors.New("laman is not running; run `laman start` first")
	}
	cfg, err := loadOrInit()
	if err != nil {
		return err
	}

	if sub == "list" {
		body, err := client.Do("GET", "/status", nil)
		if err != nil {
			return err
		}
		var st daemon.Status
		if err := json.Unmarshal(body, &st); err != nil {
			return err
		}
		if len(st.Shares) == 0 {
			fmt.Println("Nothing is shared. `laman share` in a project folder shares it.")
			return nil
		}
		for _, s := range st.Shares {
			fmt.Printf("%-24s %s\n", s.Host, s.URL)
		}
		return nil
	}

	name := ""
	if len(rest) == 1 {
		name = rest[0]
	}
	s, err := siteHere(cfg, name)
	if err != nil {
		return err
	}
	if sub == "stop" {
		if _, err := client.Do("POST", "/api/share/stop", map[string]string{"site": s.Host}); err != nil {
			return err
		}
		fmt.Printf("%s is no longer shared\n", s.Host)
		return nil
	}

	own := cfg.ShareDomain != "" && !quick
	if own {
		fmt.Printf("Sharing %s on %s…\n", s.Host, cfg.ShareDomain)
	} else {
		fmt.Printf("Opening a Cloudflare tunnel to %s…\n", s.Host)
	}
	body, err := client.DoWithin(3*time.Minute, "POST", "/api/share", map[string]any{"site": s.Host, "as": as, "quick": quick})
	if err != nil {
		return err
	}
	var info daemon.ShareInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return err
	}
	fmt.Printf("\n%s is shared at\n\n    %s\n\n", s.Host, info.URL)
	fmt.Println("Anyone with the link can open it, until `laman share stop` or laman stops.")
	if own {
		fmt.Println("The address stays the same next time. A brand-new address can take a minute to resolve.")
	} else {
		fmt.Println("The link changes each time you share. It can take a few seconds to start working.")
	}
	if _, err := os.Stat(filepath.Join(s.Path, "public", "hot")); err == nil {
		fmt.Println("\nNote: Vite's dev server is running (public/hot), so pages load assets from")
		fmt.Println("this computer and look broken to others. Stop it and run `npm run build` first.")
	}
	return nil
}

var domainName = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}$`)

// cmdShareSetup connects `laman share` to the user's own domain: a one-time
// Cloudflare sign-in in the browser (the user approves it there), then a
// tunnel of laman's own. Everything is kept in config.ShareDir, apart from
// the user's ~/.cloudflared.
func cmdShareSetup(args []string) error {
	cfg, err := loadOrInit()
	if err != nil {
		return err
	}
	if len(args) == 1 && args[0] == "off" {
		cfg.ShareDomain, cfg.SharePattern = "", ""
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Println("`laman share` gives random trycloudflare.com links again.")
		fmt.Printf("laman's tunnel and Cloudflare sign-in stay in %s, so `laman share setup <domain>` is quick next time.\n", config.ShareDir())
		return nil
	}

	var domain, pattern string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--pattern" && i+1 < len(args):
			pattern, i = args[i+1], i+1
		case strings.HasPrefix(a, "--pattern="):
			pattern = strings.TrimPrefix(a, "--pattern=")
		case !strings.HasPrefix(a, "-") && domain == "":
			domain = strings.ToLower(strings.TrimSuffix(a, "."))
		default:
			return errors.New(shareUsage)
		}
	}
	if domain == "" {
		if cfg.ShareDomain != "" {
			fmt.Printf("Sharing on %s, as %s.\n", cfg.ShareDomain, cfg.ShareHost("{site}"))
			return nil
		}
		return errors.New(shareUsage)
	}
	if !domainName.MatchString(domain) {
		return fmt.Errorf("%q does not look like a domain (e.g. example.com)", domain)
	}
	if pattern == "" {
		pattern = "{site}"
	}
	if !strings.Contains(pattern, "{site}") || strings.Contains(pattern, ".") ||
		!daemon.ShareLabel.MatchString(strings.ReplaceAll(pattern, "{site}", "site")) {
		return errors.New("--pattern must contain {site} and stay one label, e.g. {site} or {site}-dev " +
			"(Cloudflare's free certificate covers only one level below your domain)")
	}

	if _, err := install.Cloudflared(); err != nil {
		return err
	}
	exe := install.CloudflaredExe()

	if _, err := os.Stat(config.ShareCert()); err != nil {
		fmt.Println("Sign in to Cloudflare in your browser. When it asks which domain to authorize,")
		fmt.Printf("choose %s. (Its DNS must be on Cloudflare; a free plan is enough.)\n\n", domain)
		if err := os.MkdirAll(config.ShareDir(), 0o755); err != nil {
			return err
		}
		// login writes cert.pem to $HOME/.cloudflared: laman's folder, not ~.
		login := exec.Command(exe, "tunnel", "login")
		login.Env = append(os.Environ(), "HOME="+config.ShareDir())
		login.Stdout, login.Stderr = os.Stdout, os.Stderr
		if err := login.Run(); err != nil {
			return fmt.Errorf("Cloudflare sign-in did not finish: %w", err)
		}
		if _, err := os.Stat(config.ShareCert()); err != nil {
			return errors.New("Cloudflare sign-in did not save a certificate; run `laman share setup` again")
		}
	}

	if _, err := os.Stat(config.ShareCredentials()); err != nil || cfg.ShareTunnel == "" {
		host, _ := os.Hostname()
		name := "laman-" + strings.ToLower(regexp.MustCompile(`[^A-Za-z0-9-]+`).ReplaceAllString(host, "-"))
		create, err := install.CloudflaredCommand(exe, "create", "--credentials-file", config.ShareCredentials(), name)
		if err != nil {
			return err
		}
		out, err := create.CombinedOutput()
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if strings.Contains(msg, "already exists") {
				msg += fmt.Sprintf("\nA tunnel named %s exists from an earlier setup. Delete it in Cloudflare (Zero Trust → Networks → Tunnels), then run this again.", name)
			}
			return fmt.Errorf("could not create laman's tunnel: %s", msg)
		}
		id := regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`).FindString(string(out))
		if id == "" {
			return fmt.Errorf("could not read the new tunnel's ID from: %s", out)
		}
		cfg.ShareTunnel = id
		fmt.Printf("Created Cloudflare tunnel %s\n", name)
	}

	cfg.ShareDomain = domain
	cfg.SharePattern = pattern
	if pattern == "{site}" {
		cfg.SharePattern = ""
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("\nDone. `laman share blog` now shares blog.test at https://%s\n", cfg.ShareHost("blog"))
	fmt.Println("`laman share --quick` still gives a random trycloudflare.com link.")
	return nil
}
