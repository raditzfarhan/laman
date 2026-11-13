package install

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// WordPress downloads the newest WordPress release and unpacks it into
// dest, which must not exist yet. It returns the version. wordpress.org
// publishes SHA-1 sums only, so that is what the zip is checked against
// (on top of HTTPS).
func WordPress(dest string) (string, error) {
	var check struct {
		Offers []struct {
			Response string `json:"response"`
			Version  string `json:"version"`
			Download string `json:"download"`
		} `json:"offers"`
	}
	if err := getJSON("https://api.wordpress.org/core/version-check/1.7/", &check); err != nil {
		return "", fmt.Errorf("find the newest WordPress: %w", err)
	}
	if len(check.Offers) == 0 || check.Offers[0].Download == "" {
		return "", errors.New("find the newest WordPress: wordpress.org offered no download")
	}
	offer := check.Offers[0]
	if !strings.HasPrefix(offer.Download, "https://") || !strings.HasSuffix(offer.Download, ".zip") {
		return "", fmt.Errorf("unexpected WordPress download address %q", offer.Download)
	}
	want, err := getText(offer.Download + ".sha1")
	if err != nil {
		return "", fmt.Errorf("get the WordPress checksum: %w", err)
	}
	want = strings.TrimSpace(strings.Fields(want + " ")[0])
	if len(want) != 40 {
		return "", fmt.Errorf("unexpected WordPress checksum %q", want)
	}
	fmt.Printf("Downloading WordPress %s\n", offer.Version)
	zip, err := fetchAs(offer.Download, "wordpress-"+offer.Version+".zip", "")
	if err != nil {
		return "", err
	}
	got, err := fileSHA1(zip)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(got, want) {
		os.Remove(zip)
		return "", fmt.Errorf("checksum mismatch for WordPress %s: got %s, want %s", offer.Version, got, want)
	}
	if err := unpack(zip, dest); err != nil {
		return "", err
	}
	return offer.Version, nil
}

func fileSHA1(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// WPConfig fills in WordPress's wp-config-sample.php: database settings and
// fresh secret keys. salt makes one secret.
func WPConfig(sample, db, user, pass, host string, salt func() string) string {
	r := strings.NewReplacer(
		"'database_name_here'", phpQuote(db),
		"'username_here'", phpQuote(user),
		"'password_here'", phpQuote(pass),
		"'localhost'", phpQuote(host),
	)
	s := r.Replace(sample)
	for strings.Contains(s, "'put your unique phrase here'") {
		s = strings.Replace(s, "'put your unique phrase here'", phpQuote(salt()), 1)
	}
	return s
}

func phpQuote(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

// Salt is a random secret for WordPress's keys.
func Salt() string { return randomHex(32) }
