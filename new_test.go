package main

import (
	"strings"
	"testing"
)

func TestParseNew(t *testing.T) {
	cases := []struct {
		args                   []string
		name, starter, db, php string
		in, template           string
		bad                    bool
	}{
		{args: []string{"shop"}, name: "shop", starter: "laravel"},
		{args: []string{"shop", "WP"}, name: "shop", starter: "wordpress"},
		{args: []string{"shop", "laravel-react", "--db", "pgsql", "--php=8.4"}, name: "shop", starter: "laravel-react", db: "postgres", php: "8.4"},
		{args: []string{"--in", `C:\work`, "acme", "vite", "--template", "vue"}, name: "acme", starter: "vite", in: `C:\work`, template: "vue"},
		{args: []string{"shop", "--db", "mysql"}, name: "shop", starter: "laravel", db: "mariadb"},
		{args: nil, bad: true},
		{args: []string{"a", "b", "c"}, bad: true},
		{args: []string{"shop", "--db"}, bad: true},
		{args: []string{"shop", "--force"}, bad: true},
	}
	for _, c := range cases {
		o, err := parseNew(c.args)
		if c.bad {
			if err == nil {
				t.Errorf("parseNew(%q) = %+v, want an error", c.args, o)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseNew(%q): %v", c.args, err)
			continue
		}
		if o.name != c.name || o.starter != c.starter || o.db != c.db || o.php != c.php || o.in != c.in || o.template != c.template {
			t.Errorf("parseNew(%q) = %+v", c.args, o)
		}
	}
}

func TestCheckName(t *testing.T) {
	for _, ok := range []string{"shop", "acme-api", "app2"} {
		if err := checkName(ok); err != nil {
			t.Errorf("checkName(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Shop", "my shop", "acme_api", "-x", "!!!", strings.Repeat("a", 51)} {
		if err := checkName(bad); err == nil {
			t.Errorf("checkName(%q) accepted it", bad)
		}
	}
	if err := checkName("My Shop"); err == nil || !strings.Contains(err.Error(), `"my-shop"`) {
		t.Errorf("checkName should suggest my-shop, got %v", err)
	}
}

func TestDBName(t *testing.T) {
	if got := dbName("acme-api"); got != "acme_api" {
		t.Errorf("dbName = %q", got)
	}
}

func TestStarters(t *testing.T) {
	for _, s := range starters {
		if starterByName(s.name) == nil {
			t.Errorf("starter %s not found by name", s.name)
		}
		if s.laravel() && (!s.needsDB || s.minPHP == "") {
			t.Errorf("Laravel starter %s should need a database and a minimum PHP", s.name)
		}
	}
	if starterByName("rails") != nil {
		t.Error("unknown starter found")
	}
}

func TestSafeName(t *testing.T) {
	for _, ok := range []string{"react-ts", "vue", "svelte-ts", "lit"} {
		if !safeName(ok) {
			t.Errorf("safeName(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "--help", "../x", "a b", `C:\x`, "a;b"} {
		if safeName(bad) {
			t.Errorf("safeName(%q) = true", bad)
		}
	}
}
