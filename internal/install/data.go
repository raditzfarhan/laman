package install

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"laman/internal/config"
)

// Each MariaDB and Redis line keeps its own data folder (data/mariadb-12.3,
// data/redis-8.10). A newer server can read an older one's files but not
// the reverse, so sharing one folder would make switching back unsafe.

func MariaDBDataDir(version string) string {
	return filepath.Join(config.DataDir(), "mariadb-"+config.Line(version))
}

func RedisDataDir(version string) string {
	return filepath.Join(config.DataDir(), "redis-"+config.Line(version))
}

// HasMariaDBData reports whether a line's data folder is initialised.
func HasMariaDBData(version string) bool {
	return exists(filepath.Join(MariaDBDataDir(version), "mysql"))
}

// PinVersions records the MariaDB and Redis lines in use, so installing a
// newer one never switches by surprise. Before pinning existed laman ran
// the newest install, so that is what an unpinned config was using. Safe
// to call at any time; it only edits config.json.
func PinVersions(cfg *config.Config) error {
	changed := false
	for _, p := range []struct {
		kind string
		line *string
	}{{"mariadb", &cfg.MariaDB}, {"redis", &cfg.Redis}, {"postgres", &cfg.Postgres}} {
		if *p.line == "" {
			if v := config.Latest(p.kind); v != "" {
				*p.line, changed = config.LineOf(p.kind, v), true
			}
		}
	}
	if !changed {
		return nil
	}
	return cfg.Save()
}

// MoveLegacyData renames the old shared folders (data/mariadb, data/redis)
// to the pinned line's folder. Only call it while the servers are stopped.
func MoveLegacyData(cfg *config.Config) error {
	for _, m := range []struct{ old, dest string }{
		{filepath.Join(config.DataDir(), "mariadb"), MariaDBDataDir(cfg.MariaDB)},
		{filepath.Join(config.DataDir(), "redis"), RedisDataDir(cfg.Redis)},
	} {
		if !exists(m.old) || strings.HasSuffix(m.dest, "-") {
			continue
		}
		if exists(m.dest) {
			return fmt.Errorf("both %s and %s exist; keep one and remove the other", m.old, m.dest)
		}
		if err := os.Rename(m.old, m.dest); err != nil {
			return fmt.Errorf("move %s to %s: %w", m.old, m.dest, err)
		}
	}
	return nil
}

// CopyTree copies a folder into dest, which must not exist yet. It copies
// into dest.tmp first so a failed copy leaves nothing half-done behind.
func CopyTree(src, dest string) error {
	if exists(dest) {
		return fmt.Errorf("%s already exists", dest)
	}
	tmp := dest + ".tmp"
	os.RemoveAll(tmp)
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(tmp, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", path)
		}
		return copyFile(path, target)
	})
	if err == nil {
		err = os.Rename(tmp, dest)
	}
	if err != nil {
		os.RemoveAll(tmp)
	}
	return err
}

func copyFile(src, dest string) error {
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
