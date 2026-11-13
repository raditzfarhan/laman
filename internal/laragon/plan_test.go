package laragon

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"laman/internal/config"
)

// planFixture is the Laragon fixture with real site folders and a laman
// config that has its own, unrelated project folder.
func planFixture(t *testing.T) (*Laragon, *config.Config) {
	t.Helper()
	t.Setenv("LAMAN_HOME", t.TempDir())
	root, www := fixture(t)
	write(t, filepath.Join(www, "acme", "composer.json"), `{"require":{"php":">=8.1 <8.5"}}`)
	write(t, filepath.Join(www, "shop_app", "public", "index.php"), "")
	l, err := Find(root)
	if err != nil {
		t.Fatal(err)
	}
	own := t.TempDir()
	cfg := config.Default()
	cfg.SitesDir = own
	cfg.TLD = "test"
	return l, cfg
}

func TestPlanFolders(t *testing.T) {
	l, cfg := planFixture(t)
	p := BuildPlan(l, cfg, nil, nil)
	if len(p.Folders) == 0 || p.Folders[0] != (PlanFolder{l.WWW, false}) {
		t.Errorf("Folders = %+v", p.Folders)
	}
	cfg.MoreSitesDirs = append(cfg.MoreSitesDirs, l.WWW)
	p = BuildPlan(l, cfg, nil, nil)
	if len(p.Folders) == 0 || p.Folders[0] != (PlanFolder{l.WWW, true}) {
		t.Errorf("Folders = %+v", p.Folders)
	}
}

func TestPlanDoesNotMutateConfig(t *testing.T) {
	l, cfg := planFixture(t)
	cfg.Links = map[string]string{"x": "y"}
	cfg.FolderPrefix = map[string]string{"a": "b"}
	cfg.MoreSitesDirs = make([]string, 0, 8)
	before := *cfg
	links := map[string]string{"x": "y"}
	prefix := map[string]string{"a": "b"}
	BuildPlan(l, cfg, nil, nil)
	if len(cfg.MoreSitesDirs) != 0 || !reflect.DeepEqual(cfg.Links, links) || !reflect.DeepEqual(cfg.FolderPrefix, prefix) || before.SitesDir != cfg.SitesDir {
		t.Errorf("config changed: %+v", cfg)
	}
}

func TestPlanRenamed(t *testing.T) {
	l, cfg := planFixture(t)
	p := BuildPlan(l, cfg, nil, nil)
	want := Rename{"shop_app.test", "shop-app.test"}
	found := false
	for _, r := range p.Renamed {
		if r == want {
			found = true
		}
		if r.From == "acme.test" {
			t.Errorf("acme.test listed: %+v", r)
		}
	}
	if !found {
		t.Errorf("Renamed = %+v", p.Renamed)
	}
}

func TestPlanDBs(t *testing.T) {
	l, cfg := planFixture(t)
	l.MySQLData = filepath.Join(t.TempDir(), "mysql-8.4")
	for _, d := range []string{"acme", "acme_test", "shop"} {
		os.MkdirAll(filepath.Join(l.MySQLData, d), 0o755)
	}
	p := BuildPlan(l, cfg, []string{"SHOP"}, nil)
	want := []PlanDB{{Name: "acme"}, {Name: "acme_test", Copy: true}, {Name: "shop", Exists: true}}
	if !reflect.DeepEqual(p.DBs, want) {
		t.Errorf("DBs = %+v", p.DBs)
	}
}

func TestPlanBlocked(t *testing.T) {
	l, cfg := planFixture(t)
	p := BuildPlan(l, cfg, nil, []string{"httpd.exe"})
	if !reflect.DeepEqual(p.Blocked, []string{"httpd.exe"}) {
		t.Errorf("Blocked = %v", p.Blocked)
	}
}

func TestPlanByHandAndLinks(t *testing.T) {
	l, cfg := planFixture(t)
	p := BuildPlan(l, cfg, nil, nil)
	if len(p.ByHand) != 1 || p.ByHand[0].Host != "api.local" {
		t.Errorf("ByHand = %+v", p.ByHand)
	}
	wantDir := filepath.Clean(`D:\work\api`)
	if len(p.Links) != 1 || p.Links[0].Name != "api" || filepath.Clean(p.Links[0].Dir) != wantDir || p.Links[0].Taken {
		t.Errorf("Links = %+v", p.Links)
	}
	cfg.Links = map[string]string{"api": `E:\other`}
	p = BuildPlan(l, cfg, nil, nil)
	if len(p.Links) != 1 || !p.Links[0].Taken {
		t.Errorf("Links = %+v", p.Links)
	}
}

func TestPlanClash(t *testing.T) {
	l, cfg := planFixture(t)
	os.MkdirAll(filepath.Join(cfg.SitesDir, "acme"), 0o755)
	p := BuildPlan(l, cfg, nil, nil)
	if len(p.Clashes) != 1 || !strings.Contains(p.Clashes[0], `Two project folders hold "acme"`) || !strings.Contains(p.Clashes[0], filepath.Join(l.WWW, "acme")+" is not") {
		t.Errorf("Clashes = %q", p.Clashes)
	}
	for _, r := range p.Renamed {
		if r.From == "acme.test" {
			t.Errorf("unserved site renamed: %+v", r)
		}
	}
}

func TestPlanPHP(t *testing.T) {
	l, cfg := planFixture(t)
	for _, v := range []string{"8.4.15", "8.5.12-1"} {
		os.MkdirAll(filepath.Join(config.BinDir("php"), v), 0o755)
	}
	cfg.PHP = "8.5.12-1"
	p := BuildPlan(l, cfg, nil, nil)
	if len(p.PHP) != 1 || p.PHP[0].Site != "acme" || p.PHP[0].Fit.Use != "8.4.15" {
		t.Errorf("PHP = %+v", p.PHP)
	}
}

func TestPlanLinkTakesOverFolderSite(t *testing.T) {
	l, cfg := planFixture(t)
	elsewhere := t.TempDir()
	os.MkdirAll(filepath.Join(cfg.SitesDir, "docs"), 0o755)
	linked := filepath.Join(elsewhere, "docs")
	os.MkdirAll(linked, 0o755)
	slash := filepath.ToSlash(linked)
	write(t, filepath.Join(l.Root, "etc", "apache2", "sites-enabled", "custom2.conf"), autoConf("docs.test", slash))
	l, _ = Find(l.Root)
	p := BuildPlan(l, cfg, nil, nil)
	found := false
	for _, c := range p.Clashes {
		if strings.Contains(c, `The link "docs"`) && strings.Contains(c, filepath.Join(cfg.SitesDir, "docs")) {
			found = true
		}
	}
	if !found {
		t.Errorf("Clashes = %q", p.Clashes)
	}
}

func TestPlanSameRootLinkedOnce(t *testing.T) {
	l, cfg := planFixture(t)
	dir := filepath.ToSlash(filepath.Join(t.TempDir(), "blog"))
	os.MkdirAll(dir, 0o755)
	write(t, filepath.Join(l.Root, "etc", "apache2", "sites-enabled", "b1.conf"), autoConf("blog.local", dir))
	write(t, filepath.Join(l.Root, "etc", "apache2", "sites-enabled", "b2.conf"), autoConf("blog2.local", dir))
	l, _ = Find(l.Root)
	p := BuildPlan(l, cfg, nil, nil)
	n := 0
	for _, k := range p.Links {
		if k.Name == "blog" {
			n++
			if k.Taken {
				t.Errorf("blog taken: %+v", k)
			}
		}
	}
	if n != 1 {
		t.Errorf("Links = %+v", p.Links)
	}
}
