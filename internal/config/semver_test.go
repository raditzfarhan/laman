package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInRange(t *testing.T) {
	cases := []struct {
		v, rng string
		in     bool
	}{
		{"22.12.0", ">=22.13", false},
		{"22.13.0", ">=22.13", true},
		{"26.13.0", ">=22.13", true},
		{"20.19.1", "^20.19.0 || >=22.12.0", true},
		{"21.0.0", "^20.19.0 || >=22.12.0", false},
		{"22.12.0", "^20.19.0 || >=22.12.0", true},
		{"18.20.0", "18.x", true},
		{"19.0.0", "18.x", false},
		{"22.1.9", "~22.1", true},
		{"22.2.0", "~22.1", false},
		{"20.5.0", "20 - 22", true},
		{"22.99.0", "20 - 22", true},
		{"23.0.0", "20 - 22", false},
		{"18.0.0", ">=16 <19", true},
		{"19.1.0", ">=16 <19", false},
		{"22.0.0", ">= 22", true},
		{"0.2.5", "^0.2.3", true},
		{"0.3.0", "^0.2.3", false},
		{"20.0.0", "20 || 22", true},
		{"21.0.0", "20 || 22", false},
		{"22.0.0", "*", true},
		{"22.0.0", "", true},
		{"22.12.0-1", ">22.11", true},
	}
	for _, c := range cases {
		in, ok := InRange(c.v, c.rng)
		if !ok || in != c.in {
			t.Errorf("InRange(%q, %q) = %v, %v; want %v", c.v, c.rng, in, ok, c.in)
		}
	}
	if _, ok := InRange("22.0.0", "latest"); ok {
		t.Error(`"latest" is not a range laman reads`)
	}
}

func TestNodeForEngines(t *testing.T) {
	t.Setenv("LAMAN_HOME", t.TempDir())
	for _, v := range []string{"22.12.0", "24.11.0", "26.13.0"} {
		os.MkdirAll(filepath.Join(BinDir("node"), v), 0o755)
	}
	proj := t.TempDir()
	write := func(p, s string) {
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c := &Config{Node: "22.12.0"}

	// No engines: the default.
	write(filepath.Join(proj, "a", "package.json"), `{"name":"a"}`)
	if v, from := c.NodeFor(filepath.Join(proj, "a")); v != "22.12.0" || from != "" {
		t.Errorf("no engines: %s %q", v, from)
	}
	// Default too old: the newest installed one that fits.
	write(filepath.Join(proj, "b", "package.json"), `{"engines":{"node":"^20.19 || ^24"}}`)
	if v, from := c.NodeFor(filepath.Join(proj, "b", "src")); v != "24.11.0" || filepath.Base(from) != "package.json" {
		t.Errorf("engines ^24: %s %q", v, from)
	}
	// Default fits: keep it.
	write(filepath.Join(proj, "c", "package.json"), `{"engines":{"node":">=20"}}`)
	if v, _ := c.NodeFor(filepath.Join(proj, "c")); v != "22.12.0" {
		t.Errorf("engines >=20: %s", v)
	}
	// .nvmrc beats engines.
	write(filepath.Join(proj, "d", "package.json"), `{"engines":{"node":">=24"}}`)
	write(filepath.Join(proj, "d", ".nvmrc"), "26\n")
	if v, _ := c.NodeFor(filepath.Join(proj, "d")); v != "26.13.0" {
		t.Errorf(".nvmrc: %s", v)
	}
	// Nothing fits: the default, with a message saying so.
	write(filepath.Join(proj, "e", "package.json"), `{"engines":{"node":"^18"}}`)
	if v, _ := c.NodeFor(filepath.Join(proj, "e")); v != "22.12.0" {
		t.Errorf("no fit: %s", v)
	}
	if NodeMismatch(filepath.Join(proj, "e"), "22.12.0") == "" {
		t.Error("NodeMismatch should explain ^18")
	}
	if NodeMismatch(filepath.Join(proj, "c"), "22.12.0") != "" {
		t.Error("NodeMismatch for a fitting version")
	}
}
