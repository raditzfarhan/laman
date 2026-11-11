package install

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"laman/internal/config"
)

// Redis has no official Windows build. redis-windows/redis-windows builds
// current Redis with MSYS2 in public GitHub Actions, and GitHub records a
// SHA-256 digest for each asset. We take the plain build, not the variant
// with a Windows-service wrapper: laman supervises the process itself.
const redisReleases = "https://api.github.com/repos/redis-windows/redis-windows/releases?per_page=100"

var redisAsset = regexp.MustCompile(`^Redis-[0-9.]+-Windows-x64-msys2\.zip$`)

type ghRelease struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name   string `json:"name"`
		URL    string `json:"browser_download_url"`
		Digest string `json:"digest"`
	} `json:"assets"`
}

// pickRedis returns the newest release of a line ("7.4") or of anything
// when want is empty, skipping drafts, pre-releases and releases without
// a plain Windows build. GitHub lists releases newest first.
func pickRedis(list []ghRelease, want string) (ghRelease, error) {
	for _, r := range list {
		v := strings.TrimPrefix(r.Tag, "v")
		if r.Draft || r.Prerelease || !versionTag.MatchString(v) || strings.Contains(v, "-") {
			continue
		}
		if want != "" && v != want && !strings.HasPrefix(v, want+".") {
			continue
		}
		for _, a := range r.Assets {
			if redisAsset.MatchString(a.Name) {
				return r, nil
			}
		}
	}
	if want == "" {
		return ghRelease{}, fmt.Errorf("Redis: no Windows build published")
	}
	return ghRelease{}, fmt.Errorf("Redis: no Windows build of %s is published by redis-windows", want)
}

// Redis installs the newest Redis build of a line ("7.4"), or the newest
// overall when want is empty, then phpredis for every installed PHP. It
// returns the Redis version.
func Redis(want string) (string, error) {
	var list []ghRelease
	if err := getJSON(redisReleases, &list); err != nil {
		return "", fmt.Errorf("Redis: %w", err)
	}
	rel, err := pickRedis(list, strings.TrimPrefix(want, "v"))
	if err != nil {
		return "", err
	}
	version := strings.TrimPrefix(rel.Tag, "v")
	if !versionTag.MatchString(version) {
		return "", fmt.Errorf("Redis: unexpected release tag %q", rel.Tag)
	}
	var url, sum string
	for _, a := range rel.Assets {
		if redisAsset.MatchString(a.Name) {
			url, sum = a.URL, strings.TrimPrefix(a.Digest, "sha256:")
		}
	}
	if url == "" {
		return "", fmt.Errorf("Redis %s: no Windows x64 build published", version)
	}
	if !shaHex.MatchString(sum) {
		return "", fmt.Errorf("Redis %s: GitHub lists no SHA-256 for the download", version)
	}
	if !strings.HasPrefix(url, "https://github.com/redis-windows/redis-windows/releases/download/") {
		return "", fmt.Errorf("Redis: unexpected download URL %q", url)
	}

	dest := filepath.Join(config.BinDir("redis"), version)
	if exists(filepath.Join(dest, "redis-server.exe")) {
		fmt.Printf("Redis %s is already installed\n", version)
	} else {
		fmt.Printf("Installing Redis %s\n", version)
		zip, err := fetch(url, sum)
		if err != nil {
			return "", err
		}
		if err := unpack(zip, dest); err != nil {
			return "", err
		}
		if !exists(filepath.Join(dest, "redis-server.exe")) {
			return "", fmt.Errorf("Redis %s: the zip has no redis-server.exe", version)
		}
	}
	for _, v := range config.Installed("php") {
		if err := PHPRedis(v); err != nil {
			fmt.Fprintf(os.Stderr, "warning: phpredis for PHP %s: %v\n", v, err)
		}
	}
	return version, nil
}

// RedisDir returns an installed Redis version's folder, or "".
func RedisDir(version string) string {
	if version == "" {
		return ""
	}
	d := filepath.Join(config.BinDir("redis"), version)
	if !exists(filepath.Join(d, "redis-server.exe")) {
		return ""
	}
	return d
}

// PHPRedis adds phpredis to one installed PHP and enables it; Laravel's
// default Redis client needs it.
func PHPRedis(phpVersion string) error { return InstallExt(phpVersion, "redis") }

// enableExtension appends extension=<name> to php.ini unless a line
// already loads it.
func enableExtension(ini, name string) error {
	b, err := os.ReadFile(ini)
	if err != nil {
		return err
	}
	if regexp.MustCompile(`(?m)^\s*extension\s*=\s*"?(php_)?` + regexp.QuoteMeta(name) + `(\.dll)?"?\s*$`).Match(b) {
		return nil
	}
	s := strings.TrimRight(string(b), "\r\n") + "\n\n; added by laman\nextension=" + name + "\n"
	return os.WriteFile(ini, []byte(s), 0o644)
}
