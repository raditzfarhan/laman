// Package update finds laman's newest release on GitHub and downloads it.
//
// A release carries laman.exe (what `laman update` installs), the setup
// program, and checksums.txt with the SHA-256 of each file. A download is
// only used when its checksum matches.
package update

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"laman/internal/version"
)

// Release is the newest published release.
type Release struct {
	Version string `json:"version"` // "0.2.0"
	URL     string `json:"url"`     // the release page (notes)
	exe     string // download URL of laman.exe
	sums    string // download URL of checksums.txt
}

// ErrNoReleases means the repository has no published release yet.
var ErrNoReleases = errors.New("no laman release has been published yet")

// httpClient does every request; callers bound them with their context.
var httpClient = &http.Client{Timeout: 10 * time.Minute}

// API is the GitHub API base; tests point it elsewhere.
var API = "https://api.github.com"

// Latest asks GitHub for the newest release (drafts and pre-releases are
// skipped by GitHub's "latest").
func Latest(ctx context.Context) (Release, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/repos/%s/releases/latest", API, version.Repo), nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "laman/"+version.Version)
	res, err := httpClient.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("checking for updates: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return Release{}, ErrNoReleases
	}
	if res.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("checking for updates: GitHub answered %s", res.Status)
	}
	var body struct {
		Tag    string `json:"tag_name"`
		URL    string `json:"html_url"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&body); err != nil {
		return Release{}, fmt.Errorf("checking for updates: %w", err)
	}
	r := Release{Version: strings.TrimPrefix(body.Tag, "v"), URL: body.URL}
	for _, a := range body.Assets {
		switch strings.ToLower(a.Name) {
		case "laman.exe":
			r.exe = a.URL
		case "checksums.txt":
			r.sums = a.URL
		}
	}
	if _, ok := parse(r.Version); !ok {
		return Release{}, fmt.Errorf("checking for updates: the newest release is tagged %q, not a version", body.Tag)
	}
	return r, nil
}

// Newer reports whether version a is newer than b ("0.10.0" > "0.9.1").
// A version that doesn't parse is never newer.
func Newer(a, b string) bool {
	x, ok1 := parse(a)
	y, ok2 := parse(b)
	if !ok1 {
		return false
	}
	if !ok2 {
		return true
	}
	for i := range x {
		if x[i] != y[i] {
			return x[i] > y[i]
		}
	}
	return false
}

func parse(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// Download saves the release's laman.exe to dest after checking it
// against checksums.txt.
func Download(ctx context.Context, r Release, dest string) error {
	if r.exe == "" || r.sums == "" {
		return fmt.Errorf("release %s has no laman.exe or checksums.txt", r.Version)
	}
	sums, err := fetch(ctx, r.sums, 1<<20)
	if err != nil {
		return err
	}
	want := ""
	sc := bufio.NewScanner(strings.NewReader(string(sums)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.EqualFold(strings.TrimPrefix(f[1], "*"), "laman.exe") {
			want = strings.ToLower(f[0])
		}
	}
	if len(want) != 64 {
		return errors.New("checksums.txt has no SHA-256 for laman.exe")
	}
	body, err := fetch(ctx, r.exe, 256<<20)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("laman.exe does not match its checksum (got %s, want %s); not installing it", got, want)
	}
	return os.WriteFile(dest, body, 0o755)
}

func fetch(ctx context.Context, url string, limit int64) ([]byte, error) {
	if !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("refusing to download over plain HTTP: %s", url)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "laman/"+version.Version)
	res, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", url, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: %s", url, res.Status)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", url, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("downloading %s: larger than expected", url)
	}
	return b, nil
}
