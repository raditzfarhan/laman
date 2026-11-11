package install

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"laman/internal/config"
)

const bunRelease = "https://api.github.com/repos/oven-sh/bun/releases/latest"

// Bun installs the newest bun release, verified against the SHA-256 digest
// GitHub records for the asset. Dev servers of bun projects find it first
// on their PATH; `laman bun` runs it in a terminal.
func Bun() (string, error) {
	var rel struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := getJSON(bunRelease, &rel); err != nil {
		return "", fmt.Errorf("bun: %w", err)
	}
	version := strings.TrimPrefix(rel.Tag, "bun-v")
	if !versionTag.MatchString(version) {
		return "", fmt.Errorf("bun: unexpected release tag %q", rel.Tag)
	}
	var url, sum string
	for _, a := range rel.Assets {
		if a.Name == "bun-windows-x64.zip" {
			url, sum = a.URL, strings.TrimPrefix(a.Digest, "sha256:")
		}
	}
	if url == "" {
		return "", fmt.Errorf("bun %s: no Windows x64 build published", version)
	}
	if !shaHex.MatchString(sum) {
		return "", fmt.Errorf("bun %s: GitHub lists no SHA-256 for the download", version)
	}
	if !strings.HasPrefix(url, "https://github.com/oven-sh/bun/releases/download/") {
		return "", fmt.Errorf("bun: unexpected download URL %q", url)
	}

	dest := filepath.Join(config.BinDir("bun"), version)
	if exists(filepath.Join(dest, "bun.exe")) {
		fmt.Printf("bun %s is already installed\n", version)
		return version, nil
	}
	fmt.Printf("Installing bun %s\n", version)
	zip, err := fetch(url, sum)
	if err != nil {
		return "", err
	}
	if err := unpack(zip, dest); err != nil {
		return "", err
	}
	if !exists(filepath.Join(dest, "bun.exe")) {
		return "", fmt.Errorf("bun %s: the zip has no bun.exe", version)
	}
	return version, nil
}

// BunDir is the folder of the newest bun laman installed, or "".
func BunDir() string {
	v := config.Latest("bun")
	if v == "" {
		return ""
	}
	d := filepath.Join(config.BinDir("bun"), v)
	if !exists(filepath.Join(d, "bun.exe")) {
		return ""
	}
	return d
}

// HaveBun reports whether a dev server could run bun: laman's own, or one
// already on PATH.
func HaveBun() bool {
	if BunDir() != "" {
		return true
	}
	_, err := exec.LookPath("bun")
	return err == nil
}
