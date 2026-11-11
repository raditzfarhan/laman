package install

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"laman/internal/config"
)

const composerSite = "https://getcomposer.org"

var shaHex = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// Composer installs the newest stable composer.phar, verified against the
// SHA-256 getcomposer.org publishes, and removes older copies. It returns
// the installed version.
func Composer() (string, error) {
	var versions struct {
		Stable []struct {
			Path    string `json:"path"`
			Version string `json:"version"`
		} `json:"stable"`
	}
	if err := getJSON(composerSite+"/versions", &versions); err != nil {
		return "", err
	}
	if len(versions.Stable) == 0 || versions.Stable[0].Version == "" {
		return "", fmt.Errorf("getcomposer.org lists no stable version")
	}
	version, path := versions.Stable[0].Version, versions.Stable[0].Path
	if !strings.HasPrefix(path, "/download/") || strings.Contains(path, "..") {
		return "", fmt.Errorf("unexpected Composer download path %q", path)
	}

	dest := filepath.Join(config.BinDir("composer"), version)
	if exists(filepath.Join(dest, "composer.phar")) {
		fmt.Printf("Composer %s is already installed\n", version)
		return version, nil
	}
	sum, err := getText(composerSite + path + ".sha256sum") // "<hex>  composer.phar"
	if err != nil {
		return "", err
	}
	if sum = strings.Fields(sum + " ")[0]; !shaHex.MatchString(sum) {
		return "", fmt.Errorf("unexpected Composer checksum %q", sum)
	}
	fmt.Printf("Installing Composer %s\n", version)
	phar, err := fetch(composerSite+path, sum)
	if err != nil {
		return "", err
	}

	tmp := dest + ".tmp"
	os.RemoveAll(tmp)
	if err := copyInto(phar, filepath.Join(tmp, "composer.phar")); err != nil {
		os.RemoveAll(tmp)
		return "", err
	}
	os.RemoveAll(dest)
	if err := os.Rename(tmp, dest); err != nil {
		os.RemoveAll(tmp)
		return "", err
	}
	for _, old := range config.Installed("composer") {
		if old != version {
			os.RemoveAll(filepath.Join(config.BinDir("composer"), old))
		}
	}
	return version, nil
}

// ComposerPhar returns the installed composer.phar, or "" if there is none.
func ComposerPhar() string {
	v := config.Latest("composer")
	if v == "" {
		return ""
	}
	p := filepath.Join(config.BinDir("composer"), v, "composer.phar")
	if !exists(p) {
		return ""
	}
	return p
}

func copyInto(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
