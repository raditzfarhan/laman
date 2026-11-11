package install

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"laman/internal/config"
)

const mailpitRelease = "https://api.github.com/repos/axllent/mailpit/releases/latest"

// versionTag keeps the tag safe to use as a folder name.
var versionTag = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*(-[0-9A-Za-z.]+)?$`)

// Mailpit installs the newest Mailpit release, verified against the SHA-256
// digest GitHub records for the asset. It returns the installed version.
func Mailpit() (string, error) {
	var rel struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := getJSON(mailpitRelease, &rel); err != nil {
		return "", fmt.Errorf("Mailpit: %w", err)
	}
	version := strings.TrimPrefix(rel.Tag, "v")
	if !versionTag.MatchString(version) {
		return "", fmt.Errorf("Mailpit: unexpected release tag %q", rel.Tag)
	}
	var url, sum string
	for _, a := range rel.Assets {
		if a.Name == "mailpit-windows-amd64.zip" {
			url, sum = a.URL, strings.TrimPrefix(a.Digest, "sha256:")
		}
	}
	if url == "" {
		return "", fmt.Errorf("Mailpit %s: no Windows x64 build published", version)
	}
	if !shaHex.MatchString(sum) {
		return "", fmt.Errorf("Mailpit %s: GitHub lists no SHA-256 for the download", version)
	}
	if !strings.HasPrefix(url, "https://github.com/axllent/mailpit/releases/download/") {
		return "", fmt.Errorf("Mailpit: unexpected download URL %q", url)
	}

	dest := filepath.Join(config.BinDir("mailpit"), version)
	if exists(filepath.Join(dest, "mailpit.exe")) {
		fmt.Printf("Mailpit %s is already installed\n", version)
		return version, nil
	}
	fmt.Printf("Installing Mailpit %s\n", version)
	zip, err := fetch(url, sum)
	if err != nil {
		return "", err
	}
	if err := unpack(zip, dest); err != nil {
		return "", err
	}
	if !exists(filepath.Join(dest, "mailpit.exe")) {
		return "", fmt.Errorf("Mailpit %s: the zip has no mailpit.exe", version)
	}
	return version, nil
}

// MailpitExe returns the newest installed mailpit.exe, or "".
func MailpitExe() string {
	v := config.Latest("mailpit")
	if v == "" {
		return ""
	}
	p := filepath.Join(config.BinDir("mailpit"), v, "mailpit.exe")
	if !exists(p) {
		return ""
	}
	return p
}
