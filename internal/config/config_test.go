package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"8.4.27-1", "8.4.27", 1},
		{"8.10.0", "8.9.9", 1},
		{"1.30.6", "1.30.6", 0},
		{"11.4", "12.3", -1},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestMatchAndSaveLoad(t *testing.T) {
	t.Setenv("LAMAN_HOME", t.TempDir())
	for _, v := range []string{"8.3.30", "8.4.2", "8.4.27-1", "8.40.0"} {
		os.MkdirAll(filepath.Join(BinDir("php"), v), 0o755)
	}
	if got := Match("php", "8.4"); got != "8.4.27-1" {
		t.Errorf("Match 8.4 = %q", got)
	}
	if got := Match("php", "8.4.2"); got != "8.4.2" {
		t.Errorf("Match 8.4.2 = %q", got)
	}
	if got := Latest("php"); got != "8.40.0" {
		t.Errorf("Latest = %q", got)
	}

	c := Default()
	c.HTTPPort = 8080
	c.Links["x"] = `C:\x`
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil || got.HTTPPort != 8080 || got.Links["x"] != `C:\x` {
		t.Fatalf("round trip failed: %+v %v", got, err)
	}
}

func TestPHPFor(t *testing.T) {
	t.Setenv("LAMAN_HOME", t.TempDir())
	for _, v := range []string{"8.3.30", "8.5.12-1"} {
		os.MkdirAll(filepath.Join(BinDir("php"), v), 0o755)
	}
	c := Default()
	c.PHP = "8.5.12-1"
	c.Isolated = map[string]string{"old": "8.3.30", "gone": "7.4.33"}
	if v, note := c.PHPFor("old"); v != "8.3.30" || note != "" {
		t.Errorf("old: %s %q", v, note)
	}
	if v, _ := c.PHPFor("new"); v != "8.5.12-1" {
		t.Errorf("new: %s", v)
	}
	if v, note := c.PHPFor("gone"); v != "8.5.12-1" || note == "" {
		t.Errorf("gone should fall back with a note: %s %q", v, note)
	}
	c.PHP = "8.4.1"
	if c.DefaultPHP() != "8.5.12-1" {
		t.Error("missing default should fall back to the newest installed")
	}
}

func TestEnabledDefaultsOn(t *testing.T) {
	c := Default()
	if !c.Enabled("redis") {
		t.Fatal("a service with no setting should be on")
	}
	c.SetEnabled("redis", false)
	if c.Enabled("redis") || !c.Enabled("mariadb") {
		t.Fatal("SetEnabled should only turn off redis")
	}
}

func TestDBClientAndServedLinks(t *testing.T) {
	c := Default()
	c.Links = map[string]string{"blog": `C:\code\blog`}
	if c.DefaultDBClient() != "" || c.ServedLinks()["db"] != "" {
		t.Fatal("no database app installed: no db site")
	}
	c.Apps = map[string]string{"adminer": "6.3.0", "dbzuper": "0.2.0"}
	if got := c.DefaultDBClient(); got != "dbzuper" {
		t.Fatalf("default db app = %q, want dbzuper (preferred)", got)
	}
	c.DBClient = "adminer"
	links := c.ServedLinks()
	if links["db"] != links["adminer"] || links["blog"] == "" || links["dbzuper"] == "" {
		t.Fatalf("served links = %v", links)
	}
	if c.AppFor("db") != "adminer" || c.AppFor("blog") != "" {
		t.Fatal("AppFor mismatch")
	}
	c.DBClient = "phpmyadmin" // not installed: fall back to preference order
	if got := c.DefaultDBClient(); got != "dbzuper" {
		t.Fatalf("got %q", got)
	}
}

func TestShareHost(t *testing.T) {
	c := &Config{}
	if c.ShareHost("blog") != "" {
		t.Error("no domain, no host")
	}
	c.ShareDomain = "example.com"
	if got := c.ShareHost("blog"); got != "blog.example.com" {
		t.Errorf("got %q", got)
	}
	c.SharePattern = "{site}-dev"
	if got := c.ShareHost("blog"); got != "blog-dev.example.com" {
		t.Errorf("got %q", got)
	}
}

func TestSitesDirs(t *testing.T) {
	t.Setenv("LAMAN_HOME", t.TempDir())
	main, more := t.TempDir(), t.TempDir()
	c := &Config{SitesDir: main}
	if _, err := c.AddSitesDir(filepath.Join(more, "missing")); err == nil {
		t.Error("a missing folder was added")
	}
	if _, err := c.AddSitesDir(more); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddSitesDir(strings.ToUpper(more)); err == nil {
		t.Error("the same folder was added twice")
	}
	if _, err := c.AddSitesDir(Home()); err == nil {
		t.Error("laman's own folder was added")
	}
	if got := c.SitesDirs(); len(got) != 2 || got[0] != main {
		t.Fatalf("SitesDirs = %v", got)
	}
	if _, err := c.RemoveSitesDir(main); err != nil {
		t.Fatal(err)
	}
	if c.SitesDir != more || len(c.MoreSitesDirs) != 0 {
		t.Errorf("after removing the main folder: %q %v", c.SitesDir, c.MoreSitesDirs)
	}
	if _, err := c.RemoveSitesDir(more); err == nil {
		t.Error("the last folder was removed")
	}
}

func TestFolderPrefix(t *testing.T) {
	t.Setenv("LAMAN_HOME", t.TempDir())
	www := t.TempDir()
	group := filepath.Join(www, "Acme")
	other := t.TempDir()
	for _, d := range []string{group} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	c := &Config{SitesDir: www}
	if _, err := c.AddSitesDir(group); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddSitesDir(other); err != nil {
		t.Fatal(err)
	}
	got := c.Folders()
	if got[1].Prefix != "acme" || got[0].Prefix != "" || got[2].Prefix != "" {
		t.Fatalf("a nested folder should get its name as prefix, others none: %+v", got)
	}
	if _, p, err := c.SetFolderPrefix(strings.ToUpper(group), "Acme Group"); err != nil || p != "acme-group" {
		t.Fatalf("SetFolderPrefix = %q, %v", p, err)
	}
	if _, _, err := c.SetFolderPrefix(group, "!!"); err == nil {
		t.Error("a prefix without letters was accepted")
	}
	if _, _, err := c.SetFolderPrefix(filepath.Join(www, "nope"), "x"); err == nil {
		t.Error("a prefix was set on a folder that is not a project folder")
	}
	if _, _, err := c.SetFolderPrefix(group, ""); err != nil || c.Folders()[1].Prefix != "" || c.FolderPrefix != nil {
		t.Errorf("clearing the prefix: %v %v", err, c.FolderPrefix)
	}
	c.SetFolderPrefix(group, "ac")
	if _, err := c.RemoveSitesDir(group); err != nil || c.FolderPrefix != nil {
		t.Errorf("removing the folder should drop its prefix: %v %v", err, c.FolderPrefix)
	}
}
