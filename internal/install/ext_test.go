package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"laman/internal/config"
)

// fakePHP makes a PHP install with empty extension DLLs and a php.ini.
func fakePHP(t *testing.T, ini string, dlls ...string) (version, dir string) {
	t.Setenv("LAMAN_HOME", t.TempDir())
	version = "8.5.12-1"
	dir = filepath.Join(config.BinDir("php"), version)
	os.MkdirAll(filepath.Join(dir, "ext"), 0o755)
	for _, d := range dlls {
		os.WriteFile(filepath.Join(dir, "ext", "php_"+d+".dll"), nil, 0o644)
	}
	os.WriteFile(filepath.Join(dir, "php.ini"), []byte(ini), 0o644)
	return version, dir
}

func readIni(t *testing.T, dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "php.ini"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func find(list []Ext, name string) Ext {
	for _, e := range list {
		if e.Name == name {
			return e
		}
	}
	return Ext{}
}

func TestEnableDisableShipped(t *testing.T) {
	v, dir := fakePHP(t, "[PHP]\r\nextension=curl\r\n;extension=soap\r\n;extension=gmp\r\n", "curl", "soap", "gmp", "zend_test")
	if err := SetExt(v, "php_soap.dll", true); err != nil { // any spelling works
		t.Fatal(err)
	}
	ini := readIni(t, dir)
	if !strings.Contains(ini, "\nextension=soap") || strings.Contains(ini, ";extension=soap") {
		t.Fatalf("soap not uncommented in place:\n%s", ini)
	}
	if strings.Count(ini, "extension=soap") != 1 {
		t.Fatalf("soap added twice:\n%s", ini)
	}
	if err := SetExt(v, "curl", false); err != nil {
		t.Fatal(err)
	}
	list, err := ListExt(v)
	if err != nil {
		t.Fatal(err)
	}
	if e := find(list, "soap"); !e.Enabled || !e.Shipped || e.Installable {
		t.Fatalf("soap = %+v", e)
	}
	if find(list, "curl").Enabled || find(list, "gmp").Enabled {
		t.Fatal("curl should be off, gmp untouched")
	}
	if find(list, "zend_test").Name != "" {
		t.Fatal("PHP's own test extensions should be hidden")
	}
	if e := find(list, "xdebug"); e.Present || !e.Installable {
		t.Fatalf("xdebug should be offered for install: %+v", e)
	}
}

func TestEnableMissingAndUnknown(t *testing.T) {
	v, _ := fakePHP(t, "[PHP]\n", "curl")
	if err := SetExt(v, "xdebug", true); err == nil || !strings.Contains(err.Error(), "laman ext install xdebug") {
		t.Fatalf("enabling an uninstalled extra should point at install, got %v", err)
	}
	if err := SetExt(v, "nope", true); err == nil {
		t.Fatal("enabling an unknown extension should fail")
	}
}

func TestXdebugIsZendWithSettingsOnce(t *testing.T) {
	v, dir := fakePHP(t, "[PHP]\n", "xdebug")
	for i := 0; i < 2; i++ {
		if err := SetExt(v, "xdebug", true); err != nil {
			t.Fatal(err)
		}
		SetExt(v, "xdebug", false)
	}
	SetExt(v, "xdebug", true)
	ini := readIni(t, dir)
	if strings.Count(ini, "[xdebug]") != 1 || !strings.Contains(ini, "xdebug.start_with_request = trigger") {
		t.Fatalf("xdebug settings missing or repeated:\n%s", ini)
	}
	if !strings.Contains(ini, "\nzend_extension=xdebug") {
		t.Fatalf("xdebug must load as a zend_extension:\n%s", ini)
	}
	if !activeExts(ini)["xdebug"] {
		t.Fatal("xdebug not active")
	}
}

func TestUninstall(t *testing.T) {
	v, dir := fakePHP(t, "[PHP]\nextension=imagick\nextension=curl\n", "imagick", "curl")
	// imagick brought two libraries; another extra still needs one of them.
	os.WriteFile(filepath.Join(dir, "CORE_RL_a.dll"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "CORE_RL_b.dll"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "ext", "php_imagick.deps"), []byte("CORE_RL_a.dll\nCORE_RL_b.dll\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "ext", "php_other.deps"), []byte("CORE_RL_b.dll\n"), 0o644)

	if err := UninstallExt(v, "curl"); err == nil {
		t.Fatal("uninstalling an extension PHP ships should be refused")
	}
	if err := UninstallExt(v, "imagick"); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(dir, "ext", "php_imagick.dll")) || exists(filepath.Join(dir, "CORE_RL_a.dll")) {
		t.Fatal("imagick and its own library should be gone")
	}
	if !exists(filepath.Join(dir, "CORE_RL_b.dll")) {
		t.Fatal("a library another extension lists must stay")
	}
	if activeExts(readIni(t, dir))["imagick"] {
		t.Fatal("imagick still enabled in php.ini")
	}
}
