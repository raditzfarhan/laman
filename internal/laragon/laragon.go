// Package laragon reads an existing Laragon install: where its folders are,
// which sites and databases it has, and whether its servers are running. It
// only ever reads; nothing under a Laragon folder is written.
package laragon

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"laman/internal/desktop"
)

// DefaultRoot is where Laragon installs unless told otherwise.
const DefaultRoot = `C:\laragon`

// Laragon describes one install.
type Laragon struct {
	Root, WWW, DataDir string
	// MySQLVersion is the folder name under bin\mysql, e.g. mysql-8.4.0-winx64.
	MySQLVersion string
	// MySQLBin is <Root>\bin\mysql\<Version>\bin; MySQLData is the data
	// folder of that major.minor. Both are empty when MySQL isn't set up.
	MySQLBin, MySQLData string
	VHosts              []VHost
}

// VHost is one site Laragon serves.
type VHost struct {
	// File is the conf file's path, Host its ServerName as written
	// (e.g. acme.test), Root its document root with a trailing \public removed.
	File, Host, Root string
	// Auto: made by Laragon's auto virtual hosts. Custom: any other conf.
	Auto, Custom bool
	// Extra: the conf has directives beyond the basics (rewrites, proxies...)
	// that an import can't carry over.
	Extra bool
}

// Find reads the Laragon install at root.
func Find(root string) (*Laragon, error) {
	ini := filepath.Join(root, "usr", "laragon.ini")
	if !exists(filepath.Join(root, "laragon.exe")) && !exists(ini) {
		return nil, fmt.Errorf("%s is not a Laragon folder", root)
	}
	cfg := readINI(ini)
	l := &Laragon{
		Root:    root,
		WWW:     filepath.Join(root, "www"),
		DataDir: filepath.Join(root, "data"),
	}
	if v := cfg["apache"]["documentroot"]; v != "" {
		l.WWW = filepath.Clean(v)
	}
	if v := cfg["preferences"]["datadir"]; v != "" {
		l.DataDir = filepath.Clean(v)
	}
	if v := cfg["mysql"]["version"]; v != "" {
		l.MySQLVersion = v
		l.MySQLBin = filepath.Join(root, "bin", "mysql", v, "bin")
		if m := versionRe.FindStringSubmatch(v); m != nil {
			l.MySQLData = filepath.Join(l.DataDir, "mysql-"+m[1])
		}
	}
	nginx := cfg["nginx"]["use"] == "-1" && cfg["apache"]["use"] != "-1"
	if nginx {
		l.VHosts = readVHosts(filepath.Join(root, "etc", "nginx", "sites-enabled"), parseNginx)
	} else {
		l.VHosts = readVHosts(filepath.Join(root, "etc", "apache2", "sites-enabled"), parseApache)
	}
	return l, nil
}

// serverExes are the Laragon programs that are servers; the rest of bin
// (terminal, git, editors, runtimes) must not block an import.
var serverExes = map[string]bool{
	"httpd.exe": true, "nginx.exe": true, "mysqld.exe": true, "mariadbd.exe": true,
	"postgres.exe": true, "pg_ctl.exe": true, "redis-server.exe": true,
	"memcached.exe": true, "mongod.exe": true, "php-cgi.exe": true, "mailpit.exe": true,
}

// RunningServers names the Laragon servers (httpd.exe, mysqld.exe...)
// currently running from under <Root>in, lower-cased and sorted.
func (l *Laragon) RunningServers() []string {
	return serversIn(desktop.ProcessesUnder(filepath.Join(l.Root, "bin")))
}

// serversIn keeps the server programs among full exe paths, as sorted,
// deduplicated lower-case base names.
func serversIn(exes []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range exes {
		n := strings.ToLower(filepath.Base(p))
		if serverExes[n] && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// Database is one MySQL database in Laragon's data folder.
type Database struct {
	Name string
	Size int64 // bytes on disk
}

var (
	versionRe = regexp.MustCompile(`(\d+\.\d+)`)
	escapeRe  = regexp.MustCompile(`@([0-9a-fA-F]{4})`)
	copyRe    = regexp.MustCompile(`_(test(_\d+|ing)?|backup|bak|old|copy|\d{8})$`)
	systemDBs = map[string]bool{"mysql": true, "sys": true, "performance_schema": true, "information_schema": true}
)

// Databases lists the user databases by reading the data folder, sorted by
// name. It returns nil when MySQL isn't set up.
func (l *Laragon) Databases() ([]Database, error) {
	if l.MySQLData == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(l.MySQLData)
	if err != nil {
		return nil, err
	}
	var out []Database
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), "#") || systemDBs[e.Name()] {
			continue
		}
		var size int64
		filepath.WalkDir(filepath.Join(l.MySQLData, e.Name()), func(_ string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				if fi, err := d.Info(); err == nil {
					size += fi.Size()
				}
			}
			return nil
		})
		out = append(out, Database{Name: decodeName(e.Name()), Size: size})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// decodeName turns MySQL's @XXXX folder-name escapes back into characters.
func decodeName(s string) string {
	return escapeRe.ReplaceAllStringFunc(s, func(m string) string {
		n, _ := strconv.ParseUint(m[1:], 16, 32)
		return string(rune(n))
	})
}

// LooksLikeCopy reports whether a database name looks like a throwaway
// copy: a _test, _backup, _old... or dated (_YYYYMMDD) suffix.
func LooksLikeCopy(name string) bool { return copyRe.MatchString(name) }

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// readINI reads a Windows ini into section -> key -> value, all lowercased
// in the names. A missing file gives an empty result.
func readINI(path string) map[string]map[string]string {
	out := map[string]map[string]string{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	sec := ""
	for _, line := range strings.Split(strings.TrimPrefix(string(b), "\ufeff"), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || line[0] == ';' || line[0] == '#':
		case line[0] == '[' && line[len(line)-1] == ']':
			sec = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
		default:
			if k, v, ok := strings.Cut(line, "="); ok {
				if out[sec] == nil {
					out[sec] = map[string]string{}
				}
				out[sec][strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
			}
		}
	}
	return out
}

// readVHosts parses every .conf in dir (except Laragon's 00-default.conf).
func readVHosts(dir string, parse func(text string) (host, root string, extra bool)) []VHost {
	entries, _ := os.ReadDir(dir)
	var out []VHost
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(name), ".conf") || strings.EqualFold(name, "00-default.conf") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		host, root, extra := parse(strings.TrimPrefix(string(b), "\ufeff"))
		if host == "" || root == "" {
			continue
		}
		auto := strings.HasPrefix(strings.ToLower(name), "auto.")
		out = append(out, VHost{File: filepath.Join(dir, name), Host: host, Root: cleanRoot(root), Auto: auto, Custom: !auto, Extra: extra})
	}
	return out
}

// cleanRoot normalises a document root and drops a trailing \public.
func cleanRoot(p string) string {
	p = filepath.Clean(filepath.FromSlash(strings.Trim(strings.TrimSpace(p), `"'`)))
	if strings.EqualFold(filepath.Base(p), "public") {
		p = filepath.Dir(p)
	}
	return p
}

// fields splits a directive line into words, dropping quotes and a trailing
// semicolon.
func fields(line string) []string {
	line = strings.TrimSuffix(strings.TrimSpace(line), ";")
	f := strings.Fields(line)
	for i := range f {
		f[i] = strings.Trim(f[i], `"'`)
	}
	return f
}

func parseApache(text string) (host, root string, extra bool) {
	for _, line := range strings.Split(text, "\n") {
		f := fields(line)
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		switch d := strings.ToLower(f[0]); {
		case d == "documentroot" && len(f) > 1:
			if root == "" {
				root = strings.Join(f[1:], " ")
			}
		case d == "servername" && len(f) > 1:
			if host == "" {
				host = f[1]
			}
		case d == "serveralias", d == "<virtualhost", d == "</virtualhost>",
			d == "<directory", d == "</directory>", d == "allowoverride", d == "require",
			strings.HasPrefix(d, "ssl"):
		default:
			extra = true
		}
	}
	return
}

func parseNginx(text string) (host, root string, extra bool) {
	for _, line := range strings.Split(text, "\n") {
		f := fields(line)
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		switch d := strings.ToLower(strings.TrimSuffix(f[0], "{")); {
		case d == "server_name" && len(f) > 1:
			if host == "" {
				host = f[1]
			}
		case d == "root" && len(f) > 1:
			if root == "" {
				root = strings.Join(f[1:], " ")
			}
		case d == "", d == "server", d == "}", d == "listen", d == "index", d == "charset",
			strings.HasPrefix(d, "ssl_"):
		default:
			extra = true
		}
	}
	return
}
