package sites

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// What a project folder holds, which decides how laman serves it.
const (
	KindPHP    = "php"    // PHP (index.php, composer.json): served by PHP
	KindStatic = "static" // plain files with an index.html
	KindNode   = "node"   // a JavaScript app run by its own dev server
	KindProxy  = "proxy"  // `laman proxy`: forwarded to a port
	KindGroup  = "group"  // holds several projects in its subfolders
	KindNone   = "none"   // nothing a browser can open (infra, docs, notes)
)

type pkgJSON struct {
	Main            string            `json:"main"`
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

// frameworks, most specific first: the first dependency found names the app.
var frameworks = []struct{ dep, name string }{
	{"nuxt", "Nuxt"}, {"next", "Next.js"}, {"@remix-run/dev", "Remix"}, {"astro", "Astro"},
	{"@sveltejs/kit", "SvelteKit"}, {"@angular/core", "Angular"}, {"@quasar/app-vite", "Quasar"},
	{"@quasar/app-webpack", "Quasar"}, {"@quasar/app", "Quasar"}, {"@vue/cli-service", "Vue CLI"},
	{"react-scripts", "Create React App"}, {"vite", "Vite"}, {"@nestjs/core", "NestJS"},
	{"express", "Express"}, {"fastify", "Fastify"}, {"koa", "Koa"}, {"hono", "Hono"},
}

// servers are dependencies whose apps start with `node <main>` when the
// package has no dev or start script.
var servers = map[string]bool{"Express": true, "Fastify": true, "Koa": true, "Hono": true}

// detect fills in s.Kind, s.Framework and s.DevCmd.
func detect(s *Site) {
	p, root := s.Path, s.Root
	if exists(filepath.Join(root, "index.php")) || exists(filepath.Join(p, "composer.json")) || exists(filepath.Join(p, "index.php")) {
		s.Kind = KindPHP
		return
	}
	if fw, cmd, ok := nodeApp(p); ok {
		s.Kind, s.Framework, s.DevCmd = KindNode, fw, cmd
		return
	}
	if exists(filepath.Join(root, "index.html")) || exists(filepath.Join(root, "index.htm")) {
		s.Kind = KindStatic
		return
	}
	if hasPHPFiles(root) {
		s.Kind = KindPHP
		return
	}
	if len(SubProjects(p)) > 0 {
		s.Kind = KindGroup
		return
	}
	s.Kind = KindNone
}

// nodeApp reads package.json: the framework and the command that starts the
// app's dev server, if it has one.
func nodeApp(dir string) (framework, cmd string, ok bool) {
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return "", "", false
	}
	var pkg pkgJSON
	if json.Unmarshal(b, &pkg) != nil {
		return "", "", false
	}
	for _, f := range frameworks {
		if pkg.Dependencies[f.dep] != "" || pkg.DevDependencies[f.dep] != "" {
			framework = f.name
			break
		}
	}
	pm := PackageManager(dir)
	for _, name := range []string{"dev", "serve", "start"} {
		if strings.TrimSpace(pkg.Scripts[name]) != "" {
			if framework == "" {
				framework = "Node"
			}
			return framework, pm + " run " + name, true
		}
	}
	switch {
	case framework == "Quasar":
		return framework, `node_modules\.bin\quasar dev`, true
	case servers[framework]:
		main := pkg.Main
		if main == "" {
			for _, f := range []string{"index.js", "server.js", "app.js", "main.js"} {
				if exists(filepath.Join(dir, f)) {
					main = f
					break
				}
			}
		}
		if main != "" && exists(filepath.Join(dir, main)) && safeArg(main) {
			return framework, "node " + filepath.ToSlash(main), true
		}
	}
	return "", "", false
}

// PackageManager works out npm, pnpm, yarn or bun for a project: the
// "packageManager" field in package.json, else the tool that filled
// node_modules, else the newest lock file, else npm. Projects often carry a
// stale lock file from another tool, so the lock file comes last.
func PackageManager(dir string) string {
	if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		var pkg struct {
			PackageManager string `json:"packageManager"`
		}
		if json.Unmarshal(b, &pkg) == nil {
			name, _, _ := strings.Cut(pkg.PackageManager, "@")
			switch name = strings.TrimSpace(name); name {
			case "npm", "pnpm", "yarn", "bun":
				return name
			}
		}
	}
	nm := filepath.Join(dir, "node_modules")
	for _, m := range []struct{ file, pm string }{
		{".modules.yaml", "pnpm"}, {".pnpm", "pnpm"},
		{".yarn-integrity", "yarn"}, {".yarn-state.yml", "yarn"},
		{".package-lock.json", "npm"},
	} {
		if exists(filepath.Join(nm, m.file)) {
			return m.pm
		}
	}
	best, bestTime := "npm", int64(0)
	for _, l := range []struct{ file, pm string }{
		{"pnpm-lock.yaml", "pnpm"}, {"yarn.lock", "yarn"}, {"bun.lock", "bun"},
		{"bun.lockb", "bun"}, {"package-lock.json", "npm"},
	} {
		if st, err := os.Stat(filepath.Join(dir, l.file)); err == nil && st.ModTime().UnixNano() > bestTime {
			best, bestTime = l.pm, st.ModTime().UnixNano()
		}
	}
	return best
}

// SubProjects lists the folders inside dir that are projects of their own
// (shop\api, shop\web-app), by folder name.
func SubProjects(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() || strings.HasPrefix(n, ".") || n == "node_modules" || n == "vendor" {
			continue
		}
		sub := filepath.Join(dir, n)
		for _, f := range []string{"package.json", "composer.json", "index.php", "index.html", filepath.Join("public", "index.php")} {
			if exists(filepath.Join(sub, f)) {
				out = append(out, n)
				break
			}
		}
	}
	return out
}

func hasPHPFiles(dir string) bool {
	m, _ := filepath.Glob(filepath.Join(dir, "*.php"))
	return len(m) > 0
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// safeArg keeps a file name from package.json out of trouble on a command
// line: letters, digits and . _ - / \ only.
func safeArg(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-/\\", r)) {
			return false
		}
	}
	return s != "" && !strings.Contains(s, "..")
}
