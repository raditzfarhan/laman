package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInComposerRange(t *testing.T) {
	cases := []struct {
		v, rng string
		in     bool
	}{
		{"8.5.1", "^8.2", true},
		{"8.1.9", "^8.2", false},
		{"8.4.15", ">=8.1 <8.5", true},
		{"8.5.0", ">=8.1,<8.5", false},
		{"8.3.2", "^8.1|^8.2", true},
		{"7.4.33", "^8.1 || ^8.2", false},
		{"8.5.0", "~8.2", true},
		{"8.3.0", "~8.2.0", false},
		{"8.2.7", "8.2.*", true},
		{"8.5.12-1", ">=8.2@stable", true},
		// "|" without spaces must not swallow the alternatives around it.
		{"8.5.1", "^7.2|^7.3", false},
		{"8.5.1", "~7.3|~7.4", false},
		{"8.5.1", "~7.4|~8.0.0", false},
		{"8.5.1", "~7.4|~8.0", true}, // ~8.0 is >=8.0 <9.0
		{"7.2.5", "^7.1.3|7.2.*", true},
		{"8.0.1", "~7.4|~8.0", true},
		{"8.5.1", "^7.4||^8.0", true},
	}
	for _, c := range cases {
		in, ok := InComposerRange(c.v, c.rng)
		if !ok || in != c.in {
			t.Errorf("InComposerRange(%q, %q) = %v, %v; want %v, true", c.v, c.rng, in, ok, c.in)
		}
	}
}

func writeComposer(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestComposerPHPPrefersPlatform(t *testing.T) {
	dir := writeComposer(t, `{"require":{"php":"^8.2"},"config":{"platform":{"php":"8.3.0"}}}`)
	rng, from := ComposerPHP(dir)
	if rng != "8.3.0" || from != "composer.json (config.platform.php)" {
		t.Errorf("got %q, %q", rng, from)
	}
	dir = writeComposer(t, `{"require":{"php":"^8.2"}}`)
	if rng, from := ComposerPHP(dir); rng != "^8.2" || from != "composer.json" {
		t.Errorf("got %q, %q", rng, from)
	}
}

func TestComposerPHPNone(t *testing.T) {
	if rng, from := ComposerPHP(t.TempDir()); rng != "" || from != "" {
		t.Errorf("got %q, %q", rng, from)
	}
	dir := writeComposer(t, `{"require":{"acme/shop":"^1"}}`)
	if rng, from := ComposerPHP(dir); rng != "" || from != "" {
		t.Errorf("got %q, %q", rng, from)
	}
}

func TestPHPFitFor(t *testing.T) {
	t.Setenv("LAMAN_HOME", t.TempDir())
	for _, v := range []string{"8.3.30", "8.4.15", "8.5.12-1"} {
		os.MkdirAll(filepath.Join(BinDir("php"), v), 0o755)
	}
	c := Default()
	c.PHP = "8.5.12-1"
	check := func(rng string, lines []string, wantNil bool, use, install string) {
		t.Helper()
		dir := writeComposer(t, `{"require":{"php":"`+rng+`"}}`)
		fit := c.PHPFitFor("acme", dir, lines)
		if wantNil {
			if fit != nil {
				t.Errorf("%s: want nil, got %+v", rng, fit)
			}
			return
		}
		if fit == nil {
			t.Fatalf("%s: got nil", rng)
		}
		if fit.Use != use || fit.Install != install || fit.Needs != rng || fit.RunsOn != "8.5.12-1" || fit.From != "composer.json" {
			t.Errorf("%s: got %+v", rng, fit)
		}
	}
	check("^8.2", nil, true, "", "")
	check(">=8.1 <8.5", nil, false, "8.4.15", "")
	check("~7.4.0", []string{"7.4", "8.4"}, false, "", "7.4")
	check(">=9", []string{"8.4"}, false, "", "")
	if fit := c.PHPFitFor("acme", writeComposer(t, `{"require":{"php":"banana"}}`), nil); fit != nil {
		t.Errorf("unparseable: got %+v", fit)
	}
	if fit := c.PHPFitFor("acme", t.TempDir(), nil); fit != nil {
		t.Errorf("none: got %+v", fit)
	}
}
