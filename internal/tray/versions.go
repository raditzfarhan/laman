package tray

import (
	"encoding/json"
	"strings"
	"sync"

	"fyne.io/systray"

	"laman/internal/client"
)

// Versions ▸ PHP ▸ ✓ 8.5.12 / 8.4.27, and the same for Node, MariaDB,
// PostgreSQL and Redis. Picking one switches the default (a database
// swaps in place). Installs and upgrades live in the dashboard and CLI,
// where there is room to confirm them.

type versionMenu struct {
	root  *systray.MenuItem
	kinds map[string]*kindMenu
	order []string
}

type kindMenu struct {
	item  *systray.MenuItem
	mu    sync.Mutex
	vers  []string // version shown by each pooled item
	items []*systray.MenuItem
}

type apiVersion struct {
	Kind      string   `json:"kind"`
	Name      string   `json:"name"`
	Active    string   `json:"active"`
	Installed []string `json:"installed"`
}

func newVersionMenu() *versionMenu {
	vm := &versionMenu{root: systray.AddMenuItem("Versions", "Switch the version laman uses"), kinds: map[string]*kindMenu{}}
	for _, k := range [][2]string{{"php", "PHP"}, {"node", "Node"}, {"mariadb", "MariaDB"}, {"postgres", "PostgreSQL"}, {"redis", "Redis"}} {
		vm.order = append(vm.order, k[0])
		vm.kinds[k[0]] = &kindMenu{item: vm.root.AddSubMenuItem(k[1], "")}
	}
	return vm
}

// update fills the menu from GET /api/versions. On error (laman stopped)
// the whole submenu is disabled.
func (vm *versionMenu) update(t *tray) {
	body, err := client.Do("GET", "/api/versions", nil)
	var resp struct{ Versions []apiVersion }
	if err == nil {
		err = json.Unmarshal(body, &resp)
	}
	if err != nil {
		vm.root.Disable()
		return
	}
	vm.root.Enable()
	for _, v := range resp.Versions {
		if km := vm.kinds[v.Kind]; km != nil {
			km.set(t, v)
		}
	}
}

func (km *kindMenu) set(t *tray, v apiVersion) {
	km.mu.Lock()
	defer km.mu.Unlock()
	if len(v.Installed) == 0 {
		km.item.SetTitle(v.Name + " (not installed)")
		km.item.Disable()
	} else {
		km.item.SetTitle(v.Name + " " + short(v.Active))
		km.item.Enable()
	}
	// Newest first, like the dashboard.
	list := make([]string, len(v.Installed))
	for i, x := range v.Installed {
		list[len(list)-1-i] = x
	}
	for len(km.items) < len(list) {
		i := len(km.items)
		item := km.item.AddSubMenuItemCheckbox("", "", false)
		km.items = append(km.items, item)
		km.vers = append(km.vers, "")
		kind := v.Kind
		go func() {
			for range item.ClickedCh {
				km.mu.Lock()
				ver := km.vers[i]
				km.mu.Unlock()
				if ver != "" {
					go t.useVersion(kind, ver)
				}
			}
		}()
	}
	for i, item := range km.items {
		if i < len(list) {
			km.vers[i] = list[i]
			item.SetTitle(short(list[i]))
			if list[i] == v.Active {
				item.Check()
			} else {
				item.Uncheck()
			}
			item.Show()
		} else {
			km.vers[i] = ""
			item.Hide()
		}
	}
}

// useVersion asks the daemon to switch, showing progress in the status line.
func (t *tray) useVersion(kind, version string) {
	t.mu.Lock()
	if t.busy {
		t.mu.Unlock()
		return
	}
	t.busy = true
	t.mu.Unlock()
	t.status.SetTitle("Switching to " + short(version) + "…")
	_, err := client.Do("POST", "/api/versions/use", map[string]string{"kind": kind, "version": version})
	t.mu.Lock()
	t.busy = false
	t.note = ""
	if err != nil {
		t.note = "laman: " + strings.TrimSpace(err.Error())
	}
	t.mu.Unlock()
	t.refresh()
}

func short(v string) string {
	if i := strings.LastIndex(v, "-"); i > 0 && !strings.Contains(v[i:], ".") {
		return v[:i] // PHP builds: "8.5.12-1" -> "8.5.12"
	}
	return v
}
