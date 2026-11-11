// Package install downloads and unpacks PHP and MariaDB.
package install

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"laman/internal/config"
)

var client = &http.Client{Timeout: 30 * time.Minute}

func get(url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "laman/0.1")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp, nil
}

func getJSON(url string, v any) error {
	resp, err := get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

func getText(url string) (string, error) {
	resp, err := get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

// fetch downloads url into the cache and verifies sha256 when given.
func fetch(url, wantSHA string) (string, error) {
	return fetchAs(url, filepath.Base(url), wantSHA)
}

// fetchAs is fetch with an explicit cache file name, for URLs whose last
// part is not unique (GitHub tag archives are all "v1.2.3.zip").
func fetchAs(url, name, wantSHA string) (string, error) {
	if err := os.MkdirAll(config.CacheDir(), 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(config.CacheDir(), name)
	if wantSHA != "" {
		if sum, err := fileSHA(dest); err == nil && strings.EqualFold(sum, wantSHA) {
			return dest, nil
		}
	}
	resp, err := get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	part := dest + ".part"
	f, err := os.Create(part)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	pr := &progress{name: filepath.Base(dest), total: resp.ContentLength}
	_, err = io.Copy(io.MultiWriter(f, h, pr), resp.Body)
	pr.done()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(part)
		return "", err
	}
	if sum := hex.EncodeToString(h.Sum(nil)); wantSHA != "" && !strings.EqualFold(sum, wantSHA) {
		os.Remove(part)
		return "", fmt.Errorf("checksum mismatch for %s: got %s, want %s", filepath.Base(dest), sum, wantSHA)
	}
	return dest, os.Rename(part, dest)
}

func fileSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type progress struct {
	name     string
	total, n int64
	last     time.Time
}

func (p *progress) Write(b []byte) (int, error) {
	p.n += int64(len(b))
	if time.Since(p.last) > 150*time.Millisecond {
		p.last = time.Now()
		p.print()
	}
	return len(b), nil
}

func (p *progress) print() {
	mb := func(n int64) float64 { return float64(n) / (1 << 20) }
	if p.total > 0 {
		fmt.Fprintf(os.Stderr, "\r  %s  %.1f / %.1f MB", p.name, mb(p.n), mb(p.total))
	} else {
		fmt.Fprintf(os.Stderr, "\r  %s  %.1f MB", p.name, mb(p.n))
	}
}

func (p *progress) done() { p.print(); fmt.Fprintln(os.Stderr) }

// unpack extracts a zip into dest, stripping a single shared top-level
// directory. It extracts to dest.tmp first so a failed install leaves nothing.
func unpack(zipPath, dest string) error { return unpackSkipping(zipPath, dest, nil) }

// unpackSkipping is unpack, leaving out entries whose path (after the top
// directory is stripped) starts with one of skip.
func unpackSkipping(zipPath, dest string, skip []string) (err error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()
	tmp := dest + ".tmp"
	os.RemoveAll(tmp)
	defer func() {
		if err != nil {
			os.RemoveAll(tmp)
		}
	}()
	prefix := commonTop(r.File)
	base := filepath.Clean(tmp) + string(os.PathSeparator)
	for _, f := range r.File {
		name := strings.TrimPrefix(f.Name, prefix)
		if name == "" || hasPrefixAny(name, skip) {
			continue
		}
		target := filepath.Join(tmp, filepath.FromSlash(name))
		if !strings.HasPrefix(target, base) {
			return fmt.Errorf("unsafe path in archive: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := extractFile(f, target); err != nil {
			return err
		}
	}
	return os.Rename(tmp, dest)
}

func extractFile(f *zip.File, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.Create(target)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func commonTop(files []*zip.File) string {
	top := ""
	for _, f := range files {
		i := strings.Index(f.Name, "/")
		if i < 0 {
			return "" // a file at the root: nothing to strip
		}
		seg := f.Name[:i+1]
		if top == "" {
			top = seg
		} else if seg != top {
			return ""
		}
	}
	return top
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
