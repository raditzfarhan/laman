package laragon

import (
	"fmt"
	"path/filepath"
	"strings"

	"laman/internal/config"
	"laman/internal/sites"
)

// Plan is everything an import would do, worked out without changing
// anything. The CLI and the dashboard show it and then apply it.
type Plan struct {
	Root string `json:"root"`
	// Blocked lists running Laragon servers ("httpd.exe"); while it is
	// non-empty the import cannot be applied.
	Blocked []string     `json:"blocked"`
	Folders []PlanFolder `json:"folders"`
	// Links are custom vhost roots outside WWW, served with laman link.
	Links []PlanLink `json:"links"`
	// Renamed lists Laragon hosts whose laman address differs.
	Renamed []Rename `json:"renamed"`
	// ByHand are custom vhosts laman cannot reproduce (extra directives,
	// names outside laman's TLD).
	ByHand []VHost `json:"byHand"`
	// Clashes has one sentence per site laman will not serve.
	Clashes []string `json:"clashes"`
	DBs     []PlanDB `json:"dbs"`
	// PHP lists imported sites whose composer.json rules out the PHP they
	// would run on.
	PHP []SiteFit `json:"php"`
	// Notes are sentences about the databases the user should read: Laragon
	// runs MariaDB, or a database may be half-copied by an earlier run.
	Notes []string `json:"notes"`
}

// PlanFolder is a project folder to add; Already when laman has it.
type PlanFolder struct {
	Dir     string `json:"dir"`
	Already bool   `json:"already"`
}

// PlanLink is a site to link; Taken when laman already has a link by that name.
type PlanLink struct {
	Name  string `json:"name"`
	Dir   string `json:"dir"`
	Taken bool   `json:"taken"`
}

// Rename is a site's address in Laragon (From) and in laman (To).
type Rename struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// PlanDB is a Laragon database. Exists: laman has one by that name (never
// overwritten). Copy: it looks like a backup. The UI ticks !Exists && !Copy.
type PlanDB struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Exists bool   `json:"exists"`
	Copy   bool   `json:"copy"`
}

// SiteFit is an imported site whose PHP needs a different version.
type SiteFit struct {
	Site string        `json:"site"`
	Fit  config.PHPFit `json:"fit"`
}

// BuildPlan works out what importing l into laman (cfg) would do. running
// is l.RunningServers(); lamanDBs are the databases laman already has. cfg
// is not changed.
func BuildPlan(l *Laragon, cfg *config.Config, lamanDBs []string, running []string) Plan {
	p := Plan{Root: l.Root, Blocked: running}

	// Work on a copy of everything the plan adds to, so cfg stays as it was.
	cp := *cfg
	cp.MoreSitesDirs = append([]string(nil), cfg.MoreSitesDirs...)
	cp.Links = map[string]string{}
	for k, v := range cfg.Links {
		cp.Links[k] = v
	}
	if cfg.FolderPrefix != nil {
		cp.FolderPrefix = map[string]string{}
		for k, v := range cfg.FolderPrefix {
			cp.FolderPrefix[k] = v
		}
	}

	already := false
	for _, d := range cfg.SitesDirs() {
		if sameDir(d, l.WWW) {
			already = true
		}
	}
	p.Folders = []PlanFolder{{Dir: l.WWW, Already: already}}
	if !already {
		cp.AddSitesDir(l.WWW) // fails only when WWW is missing; then nothing to serve
	}

	// Custom vhosts outside WWW become links; the ones laman cannot
	// reproduce are also listed for the user to check.
	suffix := "." + strings.ToLower(cfg.TLD)
	linkOf := map[string]string{} // vhost file -> link name
	newLinks := map[string]bool{}
	usedNames := map[string]bool{}
	before, _ := sites.DiscoverAll(cp.Folders(), cp.TLD, cfg.ServedLinks())
	for _, b := range before {
		usedNames[b.Name] = true
	}
	byDir := map[string]string{} // lower-cased link dir -> link name
	for _, v := range l.VHosts {
		if !v.Custom {
			continue
		}
		if v.Extra || !strings.HasSuffix(strings.ToLower(v.Host), suffix) {
			p.ByHand = append(p.ByHand, v)
		}
		if v.Root == "" || inDir(v.Root, l.WWW) {
			continue
		}
		name := sites.Slug(filepath.Base(v.Root))
		if name == "" {
			continue
		}
		dir := filepath.Clean(v.Root)
		if prev, dup := byDir[strings.ToLower(dir)]; dup { // two vhosts, one folder: one link
			linkOf[v.File] = prev
			continue
		}
		byDir[strings.ToLower(dir)] = name
		// A link would replace a site laman already serves (links win over
		// folders); the import never does that.
		_, own := cp.Links[name]
		taken := own || usedNames[name]
		p.Links = append(p.Links, PlanLink{Name: name, Dir: filepath.Clean(v.Root), Taken: taken})
		linkOf[v.File] = name
		if !taken {
			cp.Links[name] = filepath.Clean(v.Root)
			newLinks[name] = true
		}
	}

	served, clashes := sites.DiscoverAll(cp.Folders(), cp.TLD, cp.ServedLinks())
	for _, c := range clashes {
		if inDir(c.Skipped, l.WWW) || inDir(c.Served, l.WWW) {
			p.Clashes = append(p.Clashes, fmt.Sprintf("Two project folders hold %q: %s is served as %s.%s, %s is not. Rename one, or serve it under another name with `laman link <name>` in that folder.",
				filepath.Base(c.Skipped), c.Served, c.Name, cp.TLD, c.Skipped))
		}
	}

	for _, v := range l.VHosts {
		if v.Root == "" || v.Host == "" {
			continue
		}
		var s *sites.Site
		for i := range served {
			switch {
			case inDir(v.Root, l.WWW):
				if sameDir(served[i].Path, v.Root) && sameDir(served[i].Folder, l.WWW) {
					s = &served[i]
				}
			case newLinks[linkOf[v.File]]:
				if served[i].Name == linkOf[v.File] {
					s = &served[i]
				}
			}
		}
		if s != nil && !strings.EqualFold(s.Host, v.Host) {
			p.Renamed = append(p.Renamed, Rename{From: v.Host, To: s.Host})
		}
	}

	for _, s := range served {
		if (s.Folder != "" && sameDir(s.Folder, l.WWW)) || (s.Linked && newLinks[s.Name]) {
			if fit := cp.PHPFitFor(s.Name, s.Path, nil); fit != nil {
				p.PHP = append(p.PHP, SiteFit{Site: s.Name, Fit: *fit})
			}
		}
	}

	have := map[string]bool{}
	for _, n := range lamanDBs {
		have[strings.ToLower(n)] = true
	}
	var dbs []Database
	if l.MariaDB {
		p.Notes = append(p.Notes, ErrMariaDB.Error()+": its databases can't be copied; export them from Laragon by hand.")
	} else {
		dbs, _ = l.Databases()
	}
	for _, d := range dbs {
		p.DBs = append(p.DBs, PlanDB{Name: d.Name, Size: d.Size, Exists: have[strings.ToLower(d.Name)], Copy: LooksLikeCopy(d.Name)})
	}
	// Empty lists are [] in JSON, never null: the dashboard maps over them.
	if p.Blocked == nil {
		p.Blocked = []string{}
	}
	if p.Folders == nil {
		p.Folders = []PlanFolder{}
	}
	if p.Links == nil {
		p.Links = []PlanLink{}
	}
	if p.Renamed == nil {
		p.Renamed = []Rename{}
	}
	if p.ByHand == nil {
		p.ByHand = []VHost{}
	}
	if p.Clashes == nil {
		p.Clashes = []string{}
	}
	if p.DBs == nil {
		p.DBs = []PlanDB{}
	}
	if p.PHP == nil {
		p.PHP = []SiteFit{}
	}
	if p.Notes == nil {
		p.Notes = []string{}
	}
	return p
}

func sameDir(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// inDir reports whether p is dir or inside it.
func inDir(p, dir string) bool {
	p, dir = strings.ToLower(filepath.Clean(p)), strings.ToLower(filepath.Clean(dir))
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}
