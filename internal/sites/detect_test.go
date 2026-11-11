package sites

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetect(t *testing.T) {
	dir := t.TempDir()
	at := func(p string) string { return filepath.Join(dir, p) }
	// Laravel: composer.json wins over its Vite package.json.
	write(t, at(`laravel\composer.json`), `{}`)
	write(t, at(`laravel\package.json`), `{"scripts":{"dev":"vite"},"devDependencies":{"vite":"6"}}`)
	write(t, at(`laravel\public\index.php`), `<?php`)
	write(t, at(`shop\package.json`), `{"scripts":{"dev":"nuxt dev"},"dependencies":{"nuxt":"3"}}`)
	write(t, at(`shop\pnpm-lock.yaml`), ``)
	write(t, at(`console\package.json`), `{"scripts":{"lint":"eslint"},"devDependencies":{"@quasar/app-webpack":"3"}}`)
	write(t, at(`api\package.json`), `{"dependencies":{"express":"4"}}`)
	write(t, at(`api\server.js`), ``)
	write(t, at(`landing\index.html`), `<h1>hi`)
	write(t, at(`group\web\package.json`), `{}`)
	write(t, at(`group\api\composer.json`), `{}`)
	write(t, at(`infra\main.tf`), ``)
	write(t, at(`lib\package.json`), `{"scripts":{"test":"vitest"}}`)

	want := map[string][3]string{
		"laravel": {KindPHP, "", ""},
		"shop":    {KindNode, "Nuxt", "pnpm run dev"},
		"console": {KindNode, "Quasar", `node_modules\.bin\quasar dev`},
		"api":     {KindNode, "Express", "node server.js"},
		"landing": {KindStatic, "", ""},
		"group":   {KindGroup, "", ""},
		"infra":   {KindNone, "", ""},
		"lib":     {KindNone, "", ""},
	}
	for _, s := range Discover([]Folder{{Dir: dir}}, "test", nil) {
		w, ok := want[s.Name]
		if !ok {
			continue
		}
		if got := [3]string{s.Kind, s.Framework, s.DevCmd}; got != w {
			t.Errorf("%s: got %q want %q", s.Name, got, w)
		}
	}
	if got := SubProjects(at("group")); len(got) != 2 {
		t.Errorf("SubProjects: %v", got)
	}
}

func TestPackageManager(t *testing.T) {
	dir := t.TempDir()
	at := func(p string) string { return filepath.Join(dir, p) }
	old := time.Now().Add(-time.Hour)

	write(t, at(`plain\package.json`), `{}`)
	// packageManager field wins over lock files.
	write(t, at(`field\package.json`), `{"packageManager":"pnpm@9.1.0"}`)
	write(t, at(`field\package-lock.json`), `{}`)
	// A stale bun lock next to node_modules that npm filled.
	write(t, at(`mixed\package.json`), `{}`)
	write(t, at(`mixed\bun.lockb`), ``)
	write(t, at(`mixed\package-lock.json`), `{}`)
	write(t, at(`mixed\node_modules\.package-lock.json`), `{}`)
	// Not installed yet: the newest lock file.
	write(t, at(`locks\package.json`), `{}`)
	write(t, at(`locks\package-lock.json`), `{}`)
	write(t, at(`locks\yarn.lock`), ``)
	os.Chtimes(at(`locks\package-lock.json`), old, old)
	write(t, at(`pnpmmods\package.json`), `{}`)
	write(t, at(`pnpmmods\node_modules\.modules.yaml`), ``)
	write(t, at(`bun\package.json`), `{}`)
	write(t, at(`bun\bun.lock`), ``)

	want := map[string]string{"plain": "npm", "field": "pnpm", "mixed": "npm", "locks": "yarn", "pnpmmods": "pnpm", "bun": "bun"}
	for name, exp := range want {
		if got := PackageManager(at(name)); got != exp {
			t.Errorf("PackageManager(%s) = %s, want %s", name, got, exp)
		}
	}
}
