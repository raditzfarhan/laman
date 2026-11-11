package sites

import (
	"os"
	"path/filepath"
	"testing"
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
