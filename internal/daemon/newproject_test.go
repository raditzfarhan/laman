package daemon

import (
	"slices"
	"testing"

	"laman/internal/config"
)

func TestNewArgs(t *testing.T) {
	cfg := &config.Config{SitesDir: `C:\Sites`, MoreSitesDirs: []string{`D:\work`}}
	got, ok := newArgs(cfg, "shop", "laravel-react", "postgres", "8.4", `d:\WORK\`)
	want := []string{"new", "shop", "laravel-react", "--db", "postgres", "--php", "8.4", "--in", `d:\WORK\`}
	if !ok || !slices.Equal(got, want) {
		t.Errorf("newArgs = %q, %v", got, ok)
	}
	if got, ok := newArgs(cfg, "acme-api", "php", "", "", ""); !ok || !slices.Equal(got, []string{"new", "acme-api", "php"}) {
		t.Errorf("newArgs minimal = %q, %v", got, ok)
	}
	bad := [][5]string{
		{"Shop", "laravel", "", "", ""},
		{"shop", "rails", "", "", ""},
		{"shop", "laravel", "oracle", "", ""},
		{"shop", "laravel", "", "8.4; rm", ""},
		{"shop", "laravel", "", "", `C:\Windows`},
		{"--help", "laravel", "", "", ""},
		{"a--b", "laravel", "", "", ""},
	}
	for _, b := range bad {
		if args, ok := newArgs(cfg, b[0], b[1], b[2], b[3], b[4]); ok {
			t.Errorf("newArgs(%q) accepted: %q", b, args)
		}
	}
}
