package tray

import (
	"strings"
	"sync"

	"fyne.io/systray"

	"laman/internal/client"
	"laman/internal/daemon"
)

// Services ▸ ✓ MariaDB / PostgreSQL / ✓ Redis / ✓ Mailpit turns add-on
// servers on or off, now and for every later start. Only installed ones
// are listed.

type serviceMenu struct {
	root  *systray.MenuItem
	mu    sync.Mutex
	items map[string]*systray.MenuItem
	on    map[string]bool
}

var addons = [][2]string{{"mariadb", "MariaDB"}, {"postgres", "PostgreSQL"}, {"redis", "Redis"}, {"mailpit", "Mailpit"}}

func newServiceMenu(t *tray) *serviceMenu {
	sm := &serviceMenu{
		root:  systray.AddMenuItem("Services", "Choose which servers run with laman"),
		items: map[string]*systray.MenuItem{},
		on:    map[string]bool{},
	}
	for _, a := range addons {
		kind := a[0]
		item := sm.root.AddSubMenuItemCheckbox(a[1], "Runs with laman when ticked", false)
		item.Hide()
		sm.items[kind] = item
		go func() {
			for range item.ClickedCh {
				sm.mu.Lock()
				want := !sm.on[kind]
				sm.mu.Unlock()
				go t.setService(kind, want)
			}
		}()
	}
	return sm
}

// update shows installed add-ons: ticked when on (running or trying to),
// unticked when turned off. With laman stopped the submenu is disabled.
func (sm *serviceMenu) update(st *daemon.Status) {
	if st == nil {
		sm.root.Disable()
		return
	}
	sm.root.Enable()
	state := map[string]bool{} // kind -> on; absent = not installed
	for _, s := range st.Services {
		state[s.Name] = true
	}
	for _, kind := range st.Off {
		state[kind] = false
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	for kind, item := range sm.items {
		on, ok := state[kind]
		if !ok {
			item.Hide()
			continue
		}
		sm.on[kind] = on
		if on {
			item.Check()
		} else {
			item.Uncheck()
		}
		item.Show()
	}
}

func (t *tray) setService(kind string, on bool) {
	t.mu.Lock()
	if t.busy {
		t.mu.Unlock()
		return
	}
	t.busy = true
	t.mu.Unlock()
	verb := map[bool]string{true: "Starting ", false: "Stopping "}[on]
	t.status.SetTitle(verb + kind + "…")
	_, err := client.Do("POST", "/api/services/enable", map[string]any{"kind": kind, "enabled": on})
	t.mu.Lock()
	t.busy = false
	t.note = ""
	if err != nil {
		t.note = "laman: " + strings.TrimSpace(err.Error())
	}
	t.mu.Unlock()
	t.refresh()
}
