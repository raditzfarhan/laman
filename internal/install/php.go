package install

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"laman/internal/config"
)

const phpReleases = "https://windows.php.net/downloads/releases/"

type phpBuild struct {
	Zip struct {
		Path   string `json:"path"`
		Sha256 string `json:"sha256"`
	} `json:"zip"`
}

// PHP installs the newest NTS x64 build of a minor line ("8.4"), or the
// newest line when want is empty. It returns the installed version.
func PHP(want string) (string, error) {
	var rel map[string]map[string]json.RawMessage
	if err := getJSON(phpReleases+"releases.json", &rel); err != nil {
		return "", err
	}
	minor, err := pickMinor(rel, want)
	if err != nil {
		return "", err
	}
	entry := rel[minor]
	var version string
	if err := json.Unmarshal(entry["version"], &version); err != nil {
		return "", fmt.Errorf("php %s: no version in releases.json", minor)
	}
	var build *phpBuild
	for key, raw := range entry {
		if strings.HasPrefix(key, "nts-") && strings.HasSuffix(key, "-x64") {
			build = &phpBuild{}
			if err := json.Unmarshal(raw, build); err != nil {
				return "", err
			}
			break
		}
	}
	if build == nil || build.Zip.Path == "" {
		return "", fmt.Errorf("php %s: no NTS x64 build published", version)
	}

	dest := filepath.Join(config.BinDir("php"), version)
	if exists(dest) {
		fmt.Printf("PHP %s is already installed\n", version)
		return version, nil
	}
	fmt.Printf("Installing PHP %s\n", version)
	zip, err := fetch(phpReleases+build.Zip.Path, build.Zip.Sha256)
	if err != nil {
		return "", err
	}
	if err := unpack(zip, dest); err != nil {
		return "", err
	}
	if err := writePHPIni(dest); err != nil {
		return "", fmt.Errorf("write php.ini: %w", err)
	}
	if config.Latest("redis") != "" {
		if err := PHPRedis(version); err != nil {
			fmt.Fprintf(os.Stderr, "warning: phpredis for PHP %s: %v\n", version, err)
		}
	}
	return version, nil
}

func pickMinor(rel map[string]map[string]json.RawMessage, want string) (string, error) {
	if want != "" {
		parts := strings.SplitN(want, ".", 3)
		if len(parts) < 2 {
			return "", fmt.Errorf("give a PHP line like 8.4, not %q", want)
		}
		minor := parts[0] + "." + parts[1]
		if _, ok := rel[minor]; !ok {
			return "", fmt.Errorf("PHP %s is not offered on windows.php.net (available: %s)", minor, strings.Join(keys(rel), ", "))
		}
		return minor, nil
	}
	k := keys(rel)
	if len(k) == 0 {
		return "", fmt.Errorf("releases.json is empty")
	}
	return k[len(k)-1], nil
}

func keys(rel map[string]map[string]json.RawMessage) []string {
	out := make([]string, 0, len(rel))
	for k := range rel {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return config.CompareVersions(out[i], out[j]) < 0 })
	return out
}

var phpExtensions = []string{
	"curl", "exif", "fileinfo", "gd", "intl", "mbstring", "mysqli",
	"openssl", "pdo_mysql", "pdo_pgsql", "pdo_sqlite", "pgsql", "sodium", "sqlite3", "zip",
}

// writePHPIni derives php.ini from php.ini-development with the extensions
// a typical Laravel/WordPress app needs, and a CA bundle so HTTPS works.
func writePHPIni(dir string) error {
	b, err := os.ReadFile(filepath.Join(dir, "php.ini-development"))
	if err != nil {
		return err
	}
	ini := strings.ReplaceAll(string(b), "\r\n", "\n")
	set := func(pattern, repl string) {
		ini = regexp.MustCompile(`(?m)`+pattern).ReplaceAllLiteralString(ini, repl)
	}
	set(`^;?\s*extension_dir\s*=\s*"ext"\s*$`, `extension_dir = "`+filepath.ToSlash(filepath.Join(dir, "ext"))+`"`)
	for _, ext := range phpExtensions {
		set(`^;extension=`+ext+`\s*$`, "extension="+ext)
	}
	set(`^memory_limit\s*=.*$`, "memory_limit = 512M")
	set(`^upload_max_filesize\s*=.*$`, "upload_max_filesize = 128M")
	set(`^post_max_size\s*=.*$`, "post_max_size = 128M")
	set(`^max_execution_time\s*=.*$`, "max_execution_time = 300")
	set(`^;date\.timezone\s*=.*$`, "date.timezone = UTC")

	if ca, err := caBundle(); err == nil {
		ca = filepath.ToSlash(ca)
		set(`^;curl\.cainfo\s*=.*$`, `curl.cainfo = "`+ca+`"`)
		set(`^;openssl\.cafile\s*=.*$`, `openssl.cafile = "`+ca+`"`)
	} else {
		fmt.Fprintf(os.Stderr, "warning: no CA bundle (%v); PHP HTTPS requests may fail\n", err)
	}
	return os.WriteFile(filepath.Join(dir, "php.ini"), []byte(ini), 0o644)
}

// caBundle downloads curl's Mozilla CA bundle once; PHP on Windows ships none.
func caBundle() (string, error) {
	dest := filepath.Join(config.EtcDir(), "cacert.pem")
	if exists(dest) {
		return dest, nil
	}
	if err := os.MkdirAll(config.EtcDir(), 0o755); err != nil {
		return "", err
	}
	pem, err := getText("https://curl.se/ca/cacert.pem")
	if err != nil {
		return "", err
	}
	if !strings.Contains(pem, "BEGIN CERTIFICATE") {
		return "", fmt.Errorf("unexpected CA bundle content")
	}
	return dest, os.WriteFile(dest, []byte(pem), 0o644)
}

// PHPLines lists the PHP lines (e.g. "8.4") windows.php.net currently offers.
func PHPLines() ([]string, error) {
	var rel map[string]map[string]json.RawMessage
	if err := getJSON(phpReleases+"releases.json", &rel); err != nil {
		return nil, err
	}
	return keys(rel), nil
}
