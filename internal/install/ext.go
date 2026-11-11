package install

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"laman/internal/config"
)

// PHP extensions, per installed PHP version. Two kinds:
//
//   - shipped with PHP: their DLLs are already in ext/; enabling one is a
//     php.ini edit.
//   - extra (Installable): downloaded on request. Xdebug comes from
//     xdebug.org, checked against the SHA-256 it publishes; the rest are
//     php.net's Windows builds (downloads.php.net/~windows/pecl), which
//     come without checksums, so those rely on HTTPS from php.net.
//
// Changes take effect when that version's PHP workers restart.

// Installable lists the extra extensions laman can download.
var Installable = []string{"xdebug", "apcu", "igbinary", "mongodb", "redis", "imagick"}

// ExtNotes are one-line descriptions for the dashboard and `laman ext`.
var ExtNotes = map[string]string{
	"xdebug": "step debugger and profiler", "apcu": "in-memory cache", "igbinary": "compact serializer",
	"mongodb": "MongoDB driver", "redis": "Redis client (phpredis)", "imagick": "ImageMagick images",
	"bz2": "bzip2 compression", "curl": "HTTP client", "exif": "image metadata", "fileinfo": "file type detection",
	"ftp": "FTP client", "gd": "image drawing", "gettext": "translations", "gmp": "big integers",
	"intl": "internationalisation", "ldap": "LDAP directory", "mbstring": "multibyte strings",
	"mysqli": "MySQL/MariaDB", "odbc": "ODBC databases", "openssl": "TLS and crypto", "opcache": "bytecode cache",
	"pdo_mysql": "PDO for MySQL/MariaDB", "pdo_pgsql": "PDO for PostgreSQL", "pdo_sqlite": "PDO for SQLite",
	"pdo_odbc": "PDO for ODBC", "pdo_firebird": "PDO for Firebird", "pgsql": "PostgreSQL",
	"soap": "SOAP web services", "sockets": "low-level sockets", "sodium": "modern crypto", "sqlite3": "SQLite",
	"tidy": "HTML cleanup", "xsl": "XSLT", "zip": "zip archives", "ffi": "foreign functions",
	"shmop": "shared memory", "sysvshm": "System V shared memory", "snmp": "SNMP", "enchant": "spell checking",
	"dba": "key-value databases", "com_dotnet": "COM and .NET",
}

// zendExts load with zend_extension= rather than extension=.
var zendExts = map[string]bool{"xdebug": true, "opcache": true}

// hiddenExts ship with PHP for its own test suite.
var hiddenExts = map[string]bool{"dl_test": true, "zend_test": true}

type Ext struct {
	Name        string `json:"name"`
	Note        string `json:"note,omitempty"`
	Enabled     bool   `json:"enabled"`
	Present     bool   `json:"present"`     // its DLL is in ext/
	Shipped     bool   `json:"shipped"`     // came with PHP (cannot be uninstalled)
	Installable bool   `json:"installable"` // laman can download it
}

func phpDir(version string) string { return filepath.Join(config.BinDir("php"), version) }

func isInstallable(name string) bool {
	for _, n := range Installable {
		if n == name {
			return true
		}
	}
	return false
}

// ListExt describes every extension of an installed PHP version.
func ListExt(version string) ([]Ext, error) {
	dir := phpDir(version)
	ini, err := os.ReadFile(filepath.Join(dir, "php.ini"))
	if err != nil {
		return nil, err
	}
	enabled := activeExts(string(ini))
	byName := map[string]*Ext{}
	entries, err := os.ReadDir(filepath.Join(dir, "ext"))
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		n := strings.ToLower(e.Name())
		if !strings.HasPrefix(n, "php_") || !strings.HasSuffix(n, ".dll") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(n, "php_"), ".dll")
		if hiddenExts[name] {
			continue
		}
		byName[name] = &Ext{Name: name, Present: true, Shipped: !isInstallable(name), Installable: isInstallable(name)}
	}
	for _, name := range Installable {
		if byName[name] == nil {
			byName[name] = &Ext{Name: name, Installable: true}
		}
	}
	var out []Ext
	for name, e := range byName {
		e.Enabled = enabled[name] && e.Present
		e.Note = ExtNotes[name]
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

var (
	// A trailing \r (php.ini saved with Windows line endings) is matched
	// and kept by the replacements below.
	activeLine    = regexp.MustCompile(`(?m)^[ \t]*(zend_)?extension[ \t]*=[ \t]*"?([^";\r\n]+?)"?[ \t]*(;[^\r\n]*)?\r?$`)
	commentedLine = regexp.MustCompile(`(?m)^[ \t]*;[ \t]*(zend_)?extension[ \t]*=[ \t]*"?([^";\r\n ]+?)"?[ \t]*\r?$`)
)

// extName normalises what php.ini names an extension: "php_soap.dll",
// "C:/php/ext/php_xdebug.dll" and "soap" all mean the same thing.
func extName(s string) string {
	s = strings.ToLower(filepath.Base(strings.ReplaceAll(strings.TrimSpace(s), `\`, "/")))
	return strings.TrimSuffix(strings.TrimPrefix(s, "php_"), ".dll")
}

func activeExts(ini string) map[string]bool {
	out := map[string]bool{}
	for _, m := range activeLine.FindAllStringSubmatch(ini, -1) {
		out[extName(m[2])] = true
	}
	return out
}

// crOf returns "\r" when a matched line ended with one.
func crOf(line string) string {
	if strings.HasSuffix(line, "\r") {
		return "\r"
	}
	return ""
}

func directive(name string) string {
	if zendExts[name] {
		return "zend_extension=" + name
	}
	return "extension=" + name
}

// xdebugSettings is added the first time Xdebug is enabled: step debugging
// only when a request asks for it (browser extension, XDEBUG_TRIGGER), so
// ordinary page loads stay fast.
const xdebugSettings = `
[xdebug]
xdebug.mode = debug,develop
xdebug.start_with_request = trigger
xdebug.client_host = 127.0.0.1
xdebug.client_port = 9003
`

// SetExt enables or disables an extension in a PHP version's php.ini.
func SetExt(version, name string, on bool) error {
	name = extName(name)
	path := filepath.Join(phpDir(version), "php.ini")
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	ini := string(b)
	if on {
		if !exists(filepath.Join(phpDir(version), "ext", "php_"+name+".dll")) {
			if isInstallable(name) {
				return fmt.Errorf("%s is not installed for PHP %s; run `laman ext install %s`", name, version, name)
			}
			return fmt.Errorf("PHP %s has no extension called %q; see `laman ext`", version, name)
		}
		if activeExts(ini)[name] {
			return nil
		}
		done := false
		ini = commentedLine.ReplaceAllStringFunc(ini, func(line string) string {
			m := commentedLine.FindStringSubmatch(line)
			if done || extName(m[2]) != name {
				return line
			}
			done = true
			return directive(name) + crOf(line)
		})
		if !done {
			ini = strings.TrimRight(ini, "\r\n") + "\n\n; added by laman\n" + directive(name) + "\n"
		}
		if name == "xdebug" && !strings.Contains(ini, "[xdebug]") {
			ini = strings.TrimRight(ini, "\r\n") + "\n" + xdebugSettings
		}
	} else {
		ini = activeLine.ReplaceAllStringFunc(ini, func(line string) string {
			if extName(activeLine.FindStringSubmatch(line)[2]) != name {
				return line
			}
			return ";" + strings.TrimLeft(line, " \t")
		})
	}
	return os.WriteFile(path, []byte(ini), 0o644)
}

// InstallExt downloads an extra extension for a PHP version and enables it.
func InstallExt(version, name string) error {
	name = extName(name)
	if !isInstallable(name) {
		return fmt.Errorf("laman cannot download %q; it installs %s", name, strings.Join(Installable, ", "))
	}
	dll := filepath.Join(phpDir(version), "ext", "php_"+name+".dll")
	if !exists(dll) {
		line := config.Line(version)
		var err error
		if name == "xdebug" {
			err = installXdebug(line, dll)
		} else {
			err = installPECL(name, line, version)
		}
		if err != nil {
			return err
		}
	}
	return SetExt(version, name, true)
}

// UninstallExt disables an extra extension and removes its files.
func UninstallExt(version, name string) error {
	name = extName(name)
	dir := phpDir(version)
	dll := filepath.Join(dir, "ext", "php_"+name+".dll")
	if !isInstallable(name) && exists(dll) {
		return fmt.Errorf("%s ships with PHP; disable it instead (`laman ext disable %s`)", name, name)
	}
	if err := SetExt(version, name, false); err != nil {
		return err
	}
	// Libraries it brought along (Imagick's ImageMagick DLLs) go too,
	// unless another extension still lists them.
	deps := readDeps(dir, name)
	still := map[string]bool{}
	files, _ := filepath.Glob(filepath.Join(dir, "ext", "*.deps"))
	for _, f := range files {
		if other := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "php_"), ".deps"); other != name {
			for _, d := range readDeps(dir, other) {
				still[d] = true
			}
		}
	}
	for _, d := range deps {
		if !still[d] {
			os.Remove(filepath.Join(dir, d))
		}
	}
	os.Remove(filepath.Join(dir, "ext", "php_"+name+".deps"))
	if err := os.Remove(dll); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w (stop laman's PHP first if it is in use)", filepath.Base(dll), err)
	}
	return nil
}

func readDeps(dir, name string) []string {
	b, err := os.ReadFile(filepath.Join(dir, "ext", "php_"+name+".deps"))
	if err != nil {
		return nil
	}
	return strings.Fields(string(b))
}

var xdebugLink = regexp.MustCompile(`title="SHA256:&nbsp;([0-9a-f]{64})" href='(/files/php_xdebug-([0-9]+\.[0-9]+\.[0-9]+)-([0-9]+\.[0-9]+)-nts-vs[0-9]+-x86_64\.dll)'`)

// installXdebug fetches the newest stable Xdebug for a PHP line from
// xdebug.org, verified against the SHA-256 the page lists for it.
func installXdebug(line, dest string) error {
	page, err := getText("https://xdebug.org/download")
	if err != nil {
		return fmt.Errorf("Xdebug: %w", err)
	}
	for _, m := range xdebugLink.FindAllStringSubmatch(page, -1) {
		if m[4] != line {
			continue
		}
		fmt.Printf("Installing Xdebug %s for PHP %s\n", m[3], line)
		file, err := fetch("https://xdebug.org"+m[2], m[1])
		if err != nil {
			return err
		}
		return copyInto(file, dest)
	}
	return fmt.Errorf("xdebug.org has no Windows build for PHP %s yet", line)
}

var peclDir = regexp.MustCompile(`href="([0-9]+\.[0-9]+\.[0-9]+)/"`)

// installPECL fetches the newest stable php.net Windows build of an
// extension for a PHP line: php_<name>.dll into ext/, and any libraries it
// needs next to php.exe (listed in ext/php_<name>.deps for uninstall).
func installPECL(name, line, version string) error {
	base := "https://downloads.php.net/~windows/pecl/releases/" + name + "/"
	index, err := getText(base)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	var versions []string
	for _, m := range peclDir.FindAllStringSubmatch(index, -1) {
		versions = append(versions, m[1]) // RC/beta folders do not match
	}
	sort.Slice(versions, func(i, j int) bool { return config.CompareVersions(versions[i], versions[j]) > 0 })
	file := regexp.MustCompile(`php_` + regexp.QuoteMeta(name) + `-[0-9.]+-` + regexp.QuoteMeta(line) + `-nts-v[sc][0-9]+-x64\.zip`)
	for i, v := range versions {
		if i == 6 {
			break // older releases will not have newer PHP lines either
		}
		listing, err := getText(base + v + "/")
		if err != nil {
			continue
		}
		zipName := file.FindString(listing)
		if zipName == "" {
			continue
		}
		fmt.Printf("Installing %s %s for PHP %s\n", name, v, line)
		zipPath, err := fetch(base+v+"/"+zipName, "")
		if err != nil {
			return err
		}
		return unpackExt(zipPath, name, phpDir(version))
	}
	return fmt.Errorf("php.net has no Windows build of %s for PHP %s yet", name, line)
}

// unpackExt copies php_<name>.dll into ext/ and other DLLs beside php.exe,
// never overwriting a DLL that PHP itself ships.
func unpackExt(zipPath, name, dir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()
	var deps []string
	found := false
	for _, f := range r.File {
		base := filepath.Base(f.Name)
		if f.FileInfo().IsDir() || !strings.EqualFold(filepath.Ext(base), ".dll") {
			continue
		}
		var dest string
		switch {
		case strings.EqualFold(base, "php_"+name+".dll"):
			dest, found = filepath.Join(dir, "ext", base), true
		case strings.HasPrefix(strings.ToLower(base), "php_"):
			continue // a different extension's DLL: not ours to add
		default:
			dest = filepath.Join(dir, base)
			if exists(dest) {
				continue // PHP already has this library
			}
			deps = append(deps, base)
		}
		if err := extractTo(f, dest); err != nil {
			return err
		}
	}
	if !found {
		return fmt.Errorf("%s: php_%s.dll is not in %s", name, name, filepath.Base(zipPath))
	}
	if len(deps) > 0 {
		return os.WriteFile(filepath.Join(dir, "ext", "php_"+name+".deps"), []byte(strings.Join(deps, "\n")+"\n"), 0o644)
	}
	return nil
}

func extractTo(f *zip.File, dest string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	tmp := dest + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}
