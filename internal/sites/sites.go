// Package sites discovers the projects laman serves.
package sites

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Site struct {
	Name   string `json:"name"`
	Host   string `json:"host"`
	Path   string `json:"path"`
	Root   string `json:"root"`
	Linked bool   `json:"linked"`
	// Front is true when the web root has an index.php front controller
	// (Laravel, WordPress): unknown paths are then routed to it.
	Front bool `json:"front"`
	// PHP is the version serving the site; filled in by the daemon.
	PHP string `json:"php,omitempty"`
	// Isolated is true when the site is pinned with `laman isolate`.
	Isolated bool `json:"isolated,omitempty"`
	// App names the laman app this site serves (dbzuper, adminer, ...),
	// filled in by the daemon; empty for the user's own sites.
	App string `json:"app,omitempty"`
	// Folder is the project folder the site was found in; empty for links.
	Folder string `json:"folder,omitempty"`
	// Kind is what the folder holds (KindPHP, KindNode, ...). For a Node
	// app, Framework names it ("Nuxt") and DevCmd starts its dev server
	// ("pnpm run dev"). Proxy is the address a `laman proxy` site forwards
	// to ("127.0.0.1:3000").
	Kind      string `json:"kind"`
	Framework string `json:"framework,omitempty"`
	DevCmd    string `json:"devCmd,omitempty"`
	Proxy     string `json:"proxy,omitempty"`
}

// Folder is a project folder: every folder inside it is a site. Prefix,
// when set, goes in front of each site's name (acme + api ->
// acme-api.test), so generic names in a group do not clash.
type Folder struct {
	Dir    string `json:"dir"`
	Prefix string `json:"prefix,omitempty"`
}

// Dirs lists the folders' paths.
func Dirs(folders []Folder) []string {
	out := make([]string, len(folders))
	for i, f := range folders {
		out[i] = f.Dir
	}
	return out
}

// Discover returns one site per directory in the project folders, plus
// explicit links. When two folders hold a project of the same name, the
// folder listed first wins; a link with a parked project's name wins over it.
func Discover(folders []Folder, tld string, links map[string]string) []Site {
	list, _ := DiscoverAll(folders, tld, links)
	return list
}

// Clash is a project left unserved because an earlier project folder holds
// one with the same name.
type Clash struct {
	Name    string `json:"name"`
	Served  string `json:"served"`  // the folder served as <name>.<tld>
	Skipped string `json:"skipped"` // the one left out
}

// DiscoverAll is Discover that also reports name clashes between folders.
func DiscoverAll(folders []Folder, tld string, links map[string]string) ([]Site, []Clash) {
	byName := map[string]Site{}
	var clashes []Clash
	dirs := Dirs(folders)
	for _, f := range folders {
		dir := f.Dir
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			name := Slug(e.Name())
			if name == "" {
				continue
			}
			if f.Prefix != "" {
				name = f.Prefix + "-" + name
			}
			p := filepath.Join(dir, e.Name())
			// A group of projects added as a project folder of its own
			// (www\acme inside www) is not a site itself.
			if isOneOf(p, dirs) {
				continue
			}
			if prev, ok := byName[name]; ok {
				clashes = append(clashes, Clash{Name: name, Served: prev.Path, Skipped: p})
				continue
			}
			s := newSite(name, tld, p, false)
			s.Folder = dir
			byName[name] = s
		}
	}
	for name, p := range links {
		name = Slug(name)
		if name == "" {
			continue
		}
		if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
			continue
		}
		byName[name] = newSite(name, tld, p, true)
	}
	out := make([]Site, 0, len(byName))
	for _, s := range byName {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, clashes
}

func isOneOf(p string, dirs []string) bool {
	for _, d := range dirs {
		if strings.EqualFold(filepath.Clean(p), filepath.Clean(d)) {
			return true
		}
	}
	return false
}

// docRoot serves ./public when it exists (Laravel, Symfony), else the folder.
func docRoot(p string) string {
	pub := filepath.Join(p, "public")
	if fi, err := os.Stat(pub); err == nil && fi.IsDir() {
		return pub
	}
	return p
}

// Slug turns a folder name into a valid DNS label.
func Slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// Equal reports whether two discovery results would produce the same config.
func Equal(a, b []Site) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func Hosts(list []Site) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = s.Host
	}
	return out
}

func newSite(name, tld, path string, linked bool) Site {
	root := docRoot(path)
	_, err := os.Stat(filepath.Join(root, "index.php"))
	s := Site{Name: name, Host: name + "." + tld, Path: path, Root: root, Linked: linked, Front: err == nil}
	detect(&s)
	return s
}

// WithProxies adds `laman proxy` sites (name -> host:port), replacing a
// folder or link of the same name, and keeps the list sorted.
func WithProxies(list []Site, proxies map[string]string, tld string) []Site {
	if len(proxies) == 0 {
		return list
	}
	out := make([]Site, 0, len(list)+len(proxies))
	for _, s := range list {
		if _, ok := proxies[s.Name]; !ok {
			out = append(out, s)
		}
	}
	for name, target := range proxies {
		out = append(out, Site{Name: name, Host: name + "." + tld, Kind: KindProxy, Proxy: target})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ForDir finds the site whose folder contains dir (the deepest match wins).
func ForDir(list []Site, dir string) (Site, bool) {
	dir = strings.ToLower(filepath.Clean(dir))
	var best Site
	found := false
	for _, s := range list {
		p := strings.ToLower(filepath.Clean(s.Path))
		if dir == p || strings.HasPrefix(dir, p+string(filepath.Separator)) {
			if !found || len(p) > len(best.Path) {
				best, found = s, true
			}
		}
	}
	return best, found
}
