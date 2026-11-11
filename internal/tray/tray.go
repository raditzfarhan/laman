// Package tray is laman's Windows notification-area icon. It runs as
// lamanw.exe (no console window) and talks to the daemon over the control
// API, so it keeps working while laman itself is stopped.
package tray

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"

	"laman/internal/caddyconf"
	"laman/internal/client"
	"laman/internal/config"
	"laman/internal/daemon"
	"laman/internal/desktop"
)

// Run shows the tray icon until the user quits. A second copy exits at once.
func Run() {
	if ok, _ := desktop.SingleInstance("laman-tray"); !ok {
		return
	}
	systray.Run(onReady, func() {})
}

type tray struct {
	status, dash, hosts, mail, sites, folder, start, stop, restart, startup, quit *systray.MenuItem

	mu       sync.Mutex
	siteURLs []string
	siteItem []*systray.MenuItem
	noSites  *systray.MenuItem
	busy     bool // a start/stop is in progress
	mailURL  string
	lastIcon iconKey
	note     string // last action error, shown until the next success
	versions *versionMenu
	services *serviceMenu
}

func onReady() {
	systray.SetTooltip("laman")
	t := &tray{}
	t.setIcon(stopped)
	t.status = systray.AddMenuItem("laman: checking…", "")
	t.status.Disable()
	systray.AddSeparator()
	t.dash = systray.AddMenuItem("Open dashboard", "Open the laman dashboard in your browser")
	// Shown only while sites are missing from the hosts file; clicking it
	// raises the Windows admin prompt (never on its own).
	t.hosts = systray.AddMenuItem("Fix hosts file…", "Add the missing .test addresses (asks for admin)")
	t.hosts.Hide()
	t.mail = systray.AddMenuItem("Open mail", "See the mail your apps sent, caught by Mailpit")
	t.mail.Hide()
	t.sites = systray.AddMenuItem("Sites", "")
	t.noSites = t.sites.AddSubMenuItem("No sites yet", "")
	t.noSites.Disable()
	t.folder = systray.AddMenuItem("Open sites folder", "")
	t.versions = newVersionMenu()
	t.services = newServiceMenu(t)
	systray.AddSeparator()
	t.start = systray.AddMenuItem("Start", "Start PHP, Caddy and MariaDB")
	t.stop = systray.AddMenuItem("Stop", "Stop all services")
	t.restart = systray.AddMenuItem("Restart", "")
	systray.AddSeparator()
	t.startup = systray.AddMenuItemCheckbox("Start with Windows", "Show this icon when you sign in", desktop.StartupEnabled())
	t.quit = systray.AddMenuItem("Quit tray", "Hide this icon; laman keeps running")

	go t.events()
	go func() {
		for {
			t.refresh()
			time.Sleep(3 * time.Second)
		}
	}()
}

func (t *tray) events() {
	for {
		select {
		case <-t.dash.ClickedCh:
			desktop.OpenURL(client.BaseURL() + "/")
		case <-t.hosts.ClickedCh:
			go t.runCLI("hosts")
		case <-t.mail.ClickedCh:
			t.mu.Lock()
			u := t.mailURL
			t.mu.Unlock()
			if u != "" {
				desktop.OpenURL(u)
			}
		case <-t.folder.ClickedCh:
			if cfg, err := config.Load(); err == nil {
				desktop.OpenFolder(cfg.SitesDir)
			}
		case <-t.start.ClickedCh:
			go t.runCLI("start")
		case <-t.restart.ClickedCh:
			go t.runCLI("restart")
		case <-t.stop.ClickedCh:
			go t.runCLI("stop")
		case <-t.startup.ClickedCh:
			enable := !t.startup.Checked()
			if err := desktop.SetStartup(enable, desktop.WindowedPath(desktop.CLIPath())); err == nil {
				if enable {
					t.startup.Check()
				} else {
					t.startup.Uncheck()
				}
			}
		case <-t.quit.ClickedCh:
			systray.Quit()
			return
		}
	}
}

// runCLI runs `laman <cmd>` without a window; start already knows how to
// detach the daemon and report port clashes.
func (t *tray) runCLI(cmd string) {
	t.mu.Lock()
	if t.busy {
		t.mu.Unlock()
		return
	}
	t.busy = true
	t.mu.Unlock()
	t.status.SetTitle(map[string]string{"start": "Starting…", "stop": "Stopping…", "restart": "Restarting…",
		"hosts": "Approve the Windows prompt…"}[cmd])
	out, err := desktop.RunHidden(desktop.CLIPath(), cmd)
	t.mu.Lock()
	t.busy = false
	t.note = ""
	if err != nil {
		t.note = cmd + " failed: " + lastLine(string(out))
	}
	t.mu.Unlock()
	t.refresh()
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	l := strings.TrimSpace(strings.TrimPrefix(lines[len(lines)-1], "laman:"))
	if len(l) > 90 {
		l = l[:87] + "…"
	}
	return l
}

func (t *tray) refresh() {
	t.mu.Lock()
	busy, note := t.busy, t.note
	t.mu.Unlock()
	if busy {
		return
	}
	body, err := client.Do("GET", "/status", nil)
	var st daemon.Status
	if err == nil {
		err = json.Unmarshal(body, &st)
	}
	if err != nil {
		t.setIcon(stopped)
		title := "laman is stopped"
		if note != "" {
			title = note
		}
		t.status.SetTitle(title)
		systray.SetTooltip("laman: stopped")
		t.start.Enable()
		t.stop.Disable()
		t.restart.Disable()
		t.dash.Disable()
		t.setMail("")
		t.hosts.Hide()
		t.setSites(nil)
		t.versions.update(t)
		t.services.update(nil)
		return
	}

	problem := ""
	for _, s := range st.Services {
		if s.State != "running" {
			problem = fmt.Sprintf("%s is %s", s.Name, s.State)
			break
		}
	}
	if problem == "" && st.HostsStale {
		problem = "hosts file needs `laman hosts`"
	}
	if problem == "" && len(st.Notes) > 0 {
		problem = st.Notes[0]
	}
	if problem != "" {
		t.setIcon(warning)
		t.status.SetTitle("laman: " + problem)
	} else {
		t.setIcon(running)
		t.status.SetTitle(fmt.Sprintf("laman is running · PHP %s · %d site(s)", shortPHP(st.PHP), len(st.Sites)))
	}
	if note != "" {
		t.status.SetTitle(note) // e.g. a version switch that failed
	}
	t.versions.update(t)
	t.services.update(&st)
	systray.SetTooltip("laman: running")
	t.start.Disable()
	t.stop.Enable()
	t.restart.Enable()
	t.dash.Enable()
	t.setMail(st.MailURL)
	if st.HostsStale {
		t.hosts.Show()
	} else {
		t.hosts.Hide()
	}

	https := st.HTTPSPort > 0 && st.HTTPSTrusted
	var entries [][2]string
	for _, s := range st.Sites {
		port := st.HTTPPort
		if https {
			port = st.HTTPSPort
		}
		entries = append(entries, [2]string{s.Host, caddyconf.URL(s.Host, https, port)})
	}
	t.setSites(entries)
}

func shortPHP(v string) string {
	if i := strings.IndexAny(v, "-"); i > 0 {
		return v[:i]
	}
	return v
}

type iconKey struct {
	s     status
	light bool
	set   bool
}

// setIcon redraws only when the state or the taskbar theme changed; the
// theme is re-read on every poll so switching Windows to light mode follows.
// setMail shows "Open mail" only while Mailpit is installed and laman runs.
func (t *tray) setMail(url string) {
	t.mu.Lock()
	t.mailURL = url
	t.mu.Unlock()
	if url == "" {
		t.mail.Hide()
	} else {
		t.mail.Show()
	}
}

func (t *tray) setIcon(s status) {
	k := iconKey{s, desktop.LightTaskbar(), true}
	if t.lastIcon == k {
		return
	}
	t.lastIcon = k
	systray.SetIcon(iconFor(k.s, k.light))
}

// setSites shows one submenu entry per site, reusing menu items so their
// click handlers stay valid.
func (t *tray) setSites(entries [][2]string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for len(t.siteItem) < len(entries) {
		i := len(t.siteItem)
		item := t.sites.AddSubMenuItem("", "")
		t.siteItem = append(t.siteItem, item)
		t.siteURLs = append(t.siteURLs, "")
		go func() {
			for range item.ClickedCh {
				t.mu.Lock()
				url := t.siteURLs[i]
				t.mu.Unlock()
				if url != "" {
					desktop.OpenURL(url)
				}
			}
		}()
	}
	for i, item := range t.siteItem {
		if i < len(entries) {
			item.SetTitle(entries[i][0])
			item.SetTooltip(entries[i][1])
			t.siteURLs[i] = entries[i][1]
			item.Show()
		} else {
			t.siteURLs[i] = ""
			item.Hide()
		}
	}
	if len(entries) == 0 {
		t.noSites.Show()
	} else {
		t.noSites.Hide()
	}
}
