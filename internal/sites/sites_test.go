package sites

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Blog":         "blog",
		"my_cool app!": "my-cool-app",
		"--x--":        "x",
		"Café 2":       "caf-2",
		"___":          "",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDiscover(t *testing.T) {
	dir := t.TempDir()
	mk := func(p string) {
		if err := os.MkdirAll(filepath.Join(dir, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mk("Laravel App/public")
	mk("plain")
	mk(".hidden")
	os.WriteFile(filepath.Join(dir, "file.txt"), nil, 0o644)
	ext := t.TempDir()

	got := Discover(fs(dir), "test", map[string]string{"plain": ext, "gone": filepath.Join(ext, "nope")})
	if len(got) != 2 {
		t.Fatalf("want 2 sites, got %+v", got)
	}
	if got[0].Name != "laravel-app" || got[0].Host != "laravel-app.test" ||
		got[0].Root != filepath.Join(dir, "Laravel App", "public") {
		t.Errorf("laravel site wrong: %+v", got[0])
	}
	if got[1].Name != "plain" || !got[1].Linked || got[1].Root != ext {
		t.Errorf("link should override parked folder: %+v", got[1])
	}
}

func TestForDir(t *testing.T) {
	list := []Site{{Name: "a", Path: `C:\Sites\a`}, {Name: "ab", Path: `C:\Sites\ab`}, {Name: "inner", Path: `C:\Sites\a\packages\inner`}}
	cases := map[string]string{
		`C:\Sites\a`:                    "a",
		`c:\sites\A\app\Http`:           "a",
		`C:\Sites\ab`:                   "ab",
		`C:\Sites\a\packages\inner\src`: "inner",
	}
	for dir, want := range cases {
		if s, ok := ForDir(list, dir); !ok || s.Name != want {
			t.Errorf("ForDir(%q) = %q, %v; want %q", dir, s.Name, ok, want)
		}
	}
	if _, ok := ForDir(list, `C:\elsewhere`); ok {
		t.Error("unrelated dir should not match")
	}
}

func TestDiscoverSeveralFolders(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	for _, p := range []string{filepath.Join(a, "blog"), filepath.Join(a, "shop"), filepath.Join(b, "Blog"), filepath.Join(b, "api")} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	list, clashes := DiscoverAll(fs(a, b), "test", nil)
	var names []string
	for _, s := range list {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "api,blog,shop" {
		t.Fatalf("sites = %v", names)
	}
	for _, s := range list {
		if s.Name == "blog" && s.Path != filepath.Join(a, "blog") {
			t.Errorf("the first folder should win: %s", s.Path)
		}
	}
	if len(clashes) != 1 || clashes[0].Name != "blog" || clashes[0].Skipped != filepath.Join(b, "Blog") {
		t.Errorf("clashes = %+v", clashes)
	}
	// A missing folder is skipped, not an error.
	if got := Discover(fs(filepath.Join(a, "nope"), b), "test", nil); len(got) != 2 {
		t.Errorf("got %d sites", len(got))
	}
}

func TestGroupFolderIsNotASite(t *testing.T) {
	www := t.TempDir()
	group := filepath.Join(www, "acme")
	for _, p := range []string{filepath.Join(www, "blog"), filepath.Join(group, "billing"), filepath.Join(group, "portal")} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var names []string
	for _, s := range Discover(fs(www, strings.ToUpper(group)), "test", nil) {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "billing,blog,portal" {
		t.Errorf("sites = %v (the group folder itself should not be one)", names)
	}
}

func fs(dirs ...string) []Folder {
	out := make([]Folder, len(dirs))
	for i, d := range dirs {
		out[i] = Folder{Dir: d}
	}
	return out
}

func TestFolderPrefix(t *testing.T) {
	www := t.TempDir()
	group := filepath.Join(www, "acme")
	for _, p := range []string{filepath.Join(www, "api"), filepath.Join(group, "api"), filepath.Join(group, "Stock App")} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	list, clashes := DiscoverAll([]Folder{{Dir: www}, {Dir: group, Prefix: "acme"}}, "test", nil)
	var hosts []string
	for _, s := range list {
		hosts = append(hosts, s.Host+"@"+filepath.Base(s.Folder))
	}
	want := "acme-api.test@acme,acme-stock-app.test@acme,api.test@" + filepath.Base(www)
	if strings.Join(hosts, ",") != want || len(clashes) != 0 {
		t.Errorf("hosts = %v, clashes = %v; want %s", hosts, clashes, want)
	}
}
