package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"laman/internal/config"
	"laman/internal/sites"
)

func TestSitesCarryPHPFit(t *testing.T) {
	t.Setenv("LAMAN_HOME", t.TempDir())
	for _, v := range []string{"8.4.15", "8.5.12-1"} {
		os.MkdirAll(filepath.Join(config.BinDir("php"), v), 0o755)
	}
	cfg := config.Default()
	cfg.PHP = "8.5.12-1"
	site := func(name, composer, kind, app string) sites.Site {
		dir := filepath.Join(t.TempDir(), name)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "composer.json"), []byte(composer), 0o644)
		return sites.Site{Name: name, Path: dir, Kind: kind, App: app}
	}
	old := site("acme-old", `{"require":{"php":">=8.1 <8.5"}}`, sites.KindPHP, "")
	fillPHPFit(cfg, &old, nil)
	if old.PHPFit == nil || old.PHPFit.Use != "8.4.15" {
		t.Fatalf("mismatch: want Use 8.4.15, got %+v", old.PHPFit)
	}
	fine := site("acme-new", `{"require":{"php":"^8.2"}}`, sites.KindPHP, "")
	fillPHPFit(cfg, &fine, nil)
	if fine.PHPFit != nil {
		t.Errorf("fitting site: got %+v", fine.PHPFit)
	}
	node := site("acme-web", `{"require":{"php":">=8.1 <8.5"}}`, sites.KindNode, "")
	fillPHPFit(cfg, &node, nil)
	app := site("db", `{"require":{"php":">=8.1 <8.5"}}`, sites.KindPHP, "dbzuper")
	fillPHPFit(cfg, &app, nil)
	if node.PHPFit != nil || app.PHPFit != nil {
		t.Errorf("non-PHP or laman app got a fit: %+v %+v", node.PHPFit, app.PHPFit)
	}
}
