package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"laman/internal/sites"
)

func TestPortArgs(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{
		"dev":"vite --open","serve":"nuxt dev","start":"concurrently \"tsx watch\" \"vite\"","web":"node index.js"}}`), 0o644)
	cases := []struct{ cmd, want string }{
		{"npm run dev", " -- --port 3100 --strictPort"},
		{"pnpm run dev", " --port 3100 --strictPort"},
		{"yarn run serve", " --port 3100"},
		{"npm run start", ""}, // chained: reads $PORT or prints its own address
		{"npm run web", ""},
		{`node_modules\.bin\quasar dev`, " --port 3100"},
		{"node server.js", ""},
	}
	for _, c := range cases {
		if got := portArgs(sites.Site{Path: dir, DevCmd: c.cmd}, 3100); got != c.want {
			t.Errorf("%s: got %q want %q", c.cmd, got, c.want)
		}
	}
}

func TestLocalURL(t *testing.T) {
	for line, want := range map[string]string{
		"  ➜  Local:   http://localhost:5173/":           "5173",
		"  - Local:        http://localhost:3000":        "3000",
		"Listening on http://[::1]:3101":                 "3101",
		"App URL................ http://localhost:9000/": "9000",
		"  ➜  Network: http://192.168.1.4:5173/":         "",
	} {
		m := localURL.FindStringSubmatch(line)
		got := ""
		if m != nil {
			got = m[1]
		}
		if got != want {
			t.Errorf("%q: got %q want %q", line, got, want)
		}
	}
}
