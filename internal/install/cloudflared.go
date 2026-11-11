package install

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"laman/internal/config"
)

const cloudflaredRelease = "https://api.github.com/repos/cloudflare/cloudflared/releases/latest"

// Cloudflared installs the newest cloudflared (Cloudflare's tunnel client,
// used by `laman share`), verified against the SHA-256 digest GitHub records
// for the asset. It returns the installed version.
func Cloudflared() (string, error) {
	var rel struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := getJSON(cloudflaredRelease, &rel); err != nil {
		return "", fmt.Errorf("cloudflared: %w", err)
	}
	version := strings.TrimPrefix(rel.Tag, "v")
	if !versionTag.MatchString(version) {
		return "", fmt.Errorf("cloudflared: unexpected release tag %q", rel.Tag)
	}
	var url, sum string
	for _, a := range rel.Assets {
		if a.Name == "cloudflared-windows-amd64.exe" {
			url, sum = a.URL, strings.TrimPrefix(a.Digest, "sha256:")
		}
	}
	if url == "" {
		return "", fmt.Errorf("cloudflared %s: no Windows x64 build published", version)
	}
	if !shaHex.MatchString(sum) {
		return "", fmt.Errorf("cloudflared %s: GitHub lists no SHA-256 for the download", version)
	}
	if !strings.HasPrefix(url, "https://github.com/cloudflare/cloudflared/releases/download/") {
		return "", fmt.Errorf("cloudflared: unexpected download URL %q", url)
	}

	dest := filepath.Join(config.BinDir("cloudflared"), version, "cloudflared.exe")
	if exists(dest) {
		return version, nil
	}
	fmt.Printf("Installing cloudflared %s (for sharing sites)\n", version)
	exe, err := fetchAs(url, "cloudflared-"+version+".exe", sum)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	if err := copyFile(exe, dest); err != nil {
		return "", err
	}
	return version, nil
}

// CloudflaredExe returns the newest installed cloudflared.exe, or "".
func CloudflaredExe() string {
	v := config.Latest("cloudflared")
	if v == "" {
		return ""
	}
	p := filepath.Join(config.BinDir("cloudflared"), v, "cloudflared.exe")
	if !exists(p) {
		return ""
	}
	return p
}

// CloudflaredCommand builds a cloudflared tunnel command that uses laman's
// own Cloudflare sign-in (config.ShareDir) and an empty config file, so the
// user's ~/.cloudflared is neither read nor written. args follow "tunnel",
// e.g. "route", "dns", id, hostname.
func CloudflaredCommand(exe string, args ...string) (*exec.Cmd, error) {
	conf, err := CloudflaredEmptyConfig()
	if err != nil {
		return nil, err
	}
	full := append([]string{"tunnel", "--config", conf, "--origincert", config.ShareCert(), "--no-autoupdate"}, args...)
	cmd := exec.Command(exe, full...)
	cmd.Env = append(os.Environ(), "HOME="+config.ShareDir())
	return cmd, nil
}

// CloudflaredEmptyConfig writes (once) and returns a config file with no
// settings, for cloudflared commands that must not read ~/.cloudflared.
func CloudflaredEmptyConfig() (string, error) {
	if err := os.MkdirAll(config.ShareDir(), 0o755); err != nil {
		return "", err
	}
	conf := filepath.Join(config.ShareDir(), "empty.yml")
	// One harmless setting: cloudflared logs an error for an empty file.
	err := os.WriteFile(conf, []byte("# laman: its own settings, so ~/.cloudflared/config.yml is not used.\nno-autoupdate: true\n"), 0o644)
	return conf, err
}
