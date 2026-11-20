package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"laman/internal/config"
	"laman/internal/sites"
)

// The .env store: every write of a site's .env goes through writeEnv, one at
// a time per site, checked against the fingerprint of what the page showed,
// after a dated backup kept in laman's data folder (never in the project).

const (
	envMaxSize     = 256 << 10
	envKeepBackups = 10
)

var errEnvChanged = errors.New(".env changed on disk since you opened it — reload")

var envBackupID = regexp.MustCompile(`^\d{8}-\d{6}(-\d+)?$`)

const envIDLayout = "20060102-150405"

// envFingerprint is the lowercase hex SHA-256 of a file's bytes.
func envFingerprint(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// envLock is the one mutex of a site's .env. It takes d.mu only to look it up.
func (d *daemon) envLock(name string) *sync.Mutex {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.envLocks == nil {
		d.envLocks = map[string]*sync.Mutex{}
	}
	m := d.envLocks[name]
	if m == nil {
		m = &sync.Mutex{}
		d.envLocks[name] = m
	}
	return m
}

type envSaved struct {
	Changed     bool   `json:"changed"`
	Fingerprint string `json:"fingerprint"`
	Backup      string `json:"backup,omitempty"` // backup id
}

// writeEnv runs edit on the site's .env under the site's lock and writes the
// result atomically, after backing up the file as it was. A non-empty
// fingerprint must match the file on disk, else errEnvChanged; "" skips the
// check. edit must not take d.mu.
func (d *daemon) writeEnv(s sites.Site, fingerprint string, edit func(orig string) (string, error)) (envSaved, error) {
	l := d.envLock(s.Name)
	l.Lock()
	defer l.Unlock()

	path := filepath.Join(s.Path, ".env")
	orig, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return envSaved{}, errors.New("no .env")
	}
	if err != nil {
		return envSaved{}, err
	}
	fp := envFingerprint(orig)
	if fingerprint != "" && fingerprint != fp {
		return envSaved{}, errEnvChanged
	}
	if len(orig) > envMaxSize {
		return envSaved{}, fmt.Errorf(".env is larger than %d KB", envMaxSize>>10)
	}
	out, err := edit(string(orig))
	if err != nil {
		return envSaved{}, err
	}
	if out == string(orig) {
		return envSaved{Fingerprint: fp}, nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return envSaved{}, err
	}
	perm := fi.Mode().Perm()
	id, err := saveEnvBackup(s.Name, orig, perm, time.Now())
	if err != nil {
		return envSaved{}, err
	}

	// Next to .env so the rename is atomic; it becomes .env (or is removed).
	tmp, err := os.CreateTemp(s.Path, ".env.laman-*")
	if err != nil {
		return envSaved{}, err
	}
	name := tmp.Name()
	_, werr := tmp.WriteString(out)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(name)
		return envSaved{}, werr
	}
	if err := os.Chmod(name, perm); err != nil {
		os.Remove(name)
		return envSaved{}, err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return envSaved{}, err
	}
	return envSaved{Changed: true, Fingerprint: envFingerprint([]byte(out)), Backup: id}, nil
}

// envBackupDir is where laman keeps a site's .env backups.
func envBackupDir(name string) string {
	return filepath.Join(config.DataDir(), "env-backups", name)
}

// newEnvBackupID is the first of base, base-2, base-3… not taken in dir.
func newEnvBackupID(dir string, t time.Time) string {
	base := t.Format(envIDLayout)
	id := base
	for n := 2; exists(filepath.Join(dir, id+".env")); n++ {
		id = base + "-" + strconv.Itoa(n)
	}
	return id
}

// migrateLegacyEnvBackup moves the single-file backup older versions kept
// (env-backups/<name>.env) into the site's folder, named from its mtime.
func migrateLegacyEnvBackup(name string) error {
	legacy := filepath.Join(config.DataDir(), "env-backups", name+".env")
	fi, err := os.Stat(legacy)
	if err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	dir := envBackupDir(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.Rename(legacy, filepath.Join(dir, newEnvBackupID(dir, fi.ModTime())+".env"))
}

// saveEnvBackup stores orig as the newest backup and keeps the newest
// envKeepBackups. It returns the new backup's id.
func saveEnvBackup(name string, orig []byte, perm os.FileMode, now time.Time) (string, error) {
	if err := migrateLegacyEnvBackup(name); err != nil {
		return "", err
	}
	dir := envBackupDir(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	id := newEnvBackupID(dir, now)
	path := filepath.Join(dir, id+".env")
	if err := os.WriteFile(path, orig, perm); err != nil {
		return "", err
	}
	if err := os.Chmod(path, perm); err != nil {
		return "", err
	}
	ids, err := envBackupIDs(dir)
	if err != nil {
		return id, err
	}
	for i := envKeepBackups; i < len(ids); i++ {
		os.Remove(filepath.Join(dir, ids[i]+".env"))
	}
	return id, nil
}

// envBackupIDs lists the backup ids in dir, newest first. Ids sort
// chronologically; of one second, a higher -N suffix is newer.
func envBackupIDs(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range ents {
		id := strings.TrimSuffix(e.Name(), ".env")
		if e.Type().IsRegular() && id != e.Name() && envBackupID.MatchString(id) {
			ids = append(ids, id)
		}
	}
	split := func(id string) (string, int) {
		n := 1
		if _, suf, ok := strings.Cut(id[len(envIDLayout):], "-"); ok {
			n, _ = strconv.Atoi(suf)
		}
		return id[:len(envIDLayout)], n
	}
	sort.Slice(ids, func(i, j int) bool {
		bi, ni := split(ids[i])
		bj, nj := split(ids[j])
		if bi != bj {
			return bi > bj
		}
		return ni > nj
	})
	return ids, nil
}

type envBackup struct {
	ID   string    `json:"id"`
	Time time.Time `json:"time"`
	Size int64     `json:"size"`
}

// listEnvBackups lists a site's backups, newest first (the legacy
// single-file backup included).
func listEnvBackups(name string) ([]envBackup, error) {
	if err := migrateLegacyEnvBackup(name); err != nil {
		return nil, err
	}
	dir := envBackupDir(name)
	ids, err := envBackupIDs(dir)
	if err != nil {
		return nil, err
	}
	out := make([]envBackup, 0, len(ids))
	for _, id := range ids {
		fi, err := os.Stat(filepath.Join(dir, id+".env"))
		if err != nil {
			continue
		}
		t, _ := time.ParseInLocation(envIDLayout, id[:len(envIDLayout)], time.Local)
		out = append(out, envBackup{ID: id, Time: t, Size: fi.Size()})
	}
	return out, nil
}

// readEnvBackup returns one backup's content.
func readEnvBackup(name, id string) ([]byte, error) {
	if !envBackupID.MatchString(id) {
		return nil, fmt.Errorf("bad backup id %q", id)
	}
	return os.ReadFile(filepath.Join(envBackupDir(name), id+".env"))
}
