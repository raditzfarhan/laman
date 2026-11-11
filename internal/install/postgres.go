package install

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"laman/internal/config"
)

// PostgreSQL's Windows binaries are built by EnterpriseDB, the source
// postgresql.org links to. EDB publishes no checksums for the zips, so this
// relies on HTTPS from get.enterprisedb.com. The zip bundles pgAdmin and
// StackBuilder; laman skips those (and the docs) when unpacking.
const (
	pgVersions = "https://www.postgresql.org/versions.json"
	edbZip     = "https://get.enterprisedb.com/postgresql/postgresql-%s-%d-windows-x64-binaries.zip"
)

var pgSkip = []string{"pgAdmin 4/", "StackBuilder/", "doc/", "symbols/", "include/"}

// Postgres installs the newest release of a major ("17"), or of the newest
// supported major when want is empty. It returns the version ("17.6").
func Postgres(want string) (string, error) {
	var list []struct {
		Major       string  `json:"major"`
		LatestMinor flexInt `json:"latestMinor"`
		Supported   bool    `json:"supported"`
	}
	if err := getJSON(pgVersions, &list); err != nil {
		return "", fmt.Errorf("PostgreSQL: %w", err)
	}
	major, minor := "", -1
	for _, v := range list {
		if want != "" {
			if v.Major == config.LineOf("postgres", want) {
				major, minor = v.Major, int(v.LatestMinor)
			}
			continue
		}
		if v.Supported && config.CompareVersions(v.Major, major) > 0 {
			major, minor = v.Major, int(v.LatestMinor)
		}
	}
	if major == "" {
		return "", fmt.Errorf("PostgreSQL %s is not a release postgresql.org lists", want)
	}
	if n, err := strconv.Atoi(major); err != nil || n < 10 {
		return "", fmt.Errorf("PostgreSQL %s is too old; laman supports 10 and newer", major)
	}

	// EDB can trail postgresql.org by a few days, and rebuilds get -2, -3:
	// try the newest minor first, then step back.
	for m := minor; m >= 0 && m > minor-3; m-- {
		version := fmt.Sprintf("%s.%d", major, m)
		dest := filepath.Join(config.BinDir("postgres"), version)
		if exists(filepath.Join(dest, "bin", "postgres.exe")) {
			fmt.Printf("PostgreSQL %s is already installed\n", version)
			return version, nil
		}
		for build := 3; build >= 1; build-- {
			url := fmt.Sprintf(edbZip, version, build)
			if !reachable(url) {
				continue
			}
			fmt.Printf("Installing PostgreSQL %s (about 300 MB)\n", version)
			zip, err := fetch(url, "")
			if err != nil {
				return "", err
			}
			if err := unpackSkipping(zip, dest, pgSkip); err != nil {
				return "", err
			}
			os.Remove(zip) // large, and a rebuild is fetched again anyway
			if !exists(filepath.Join(dest, "bin", "postgres.exe")) {
				return "", fmt.Errorf("PostgreSQL %s: the zip has no bin/postgres.exe", version)
			}
			enablePgsqlEverywhere()
			return version, nil
		}
	}
	return "", fmt.Errorf("EnterpriseDB has no Windows build of PostgreSQL %s.%d yet", major, minor)
}

// flexInt accepts a JSON number or a quoted number; versions.json uses both.
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	n, err := strconv.Atoi(strings.Trim(string(b), `"`))
	*f = flexInt(n)
	return err
}

// reachable reports whether url exists, without downloading it.
func reachable(url string) bool {
	req, err := http.NewRequest("HEAD", url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "laman/0.1")
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// enablePgsqlEverywhere turns on pdo_pgsql and pgsql in every installed
// PHP. PHP for Windows ships them, with libpq.dll, but leaves them off.
func enablePgsqlEverywhere() {
	for _, v := range config.Installed("php") {
		ini := filepath.Join(config.BinDir("php"), v, "php.ini")
		for _, ext := range []string{"pdo_pgsql", "pgsql"} {
			if err := enableExtension(ini, ext); err != nil {
				fmt.Fprintf(os.Stderr, "warning: enable %s for PHP %s: %v\n", ext, v, err)
			}
		}
	}
}

func PostgresInstallDir(version string) string {
	return filepath.Join(config.BinDir("postgres"), version)
}

func PostgresBin(version, name string) string {
	return filepath.Join(PostgresInstallDir(version), "bin", name+".exe")
}

func PostgresDataDir(version string) string {
	return filepath.Join(config.DataDir(), "postgres-"+config.LineOf("postgres", version))
}

func HasPostgresData(version string) bool {
	return exists(filepath.Join(PostgresDataDir(version), "PG_VERSION"))
}

// InitPostgres creates an empty cluster for a version's major: superuser
// "postgres", no password for connections from this machine (like
// MariaDB's root), UTF-8.
func InitPostgres(version string) error {
	if HasPostgresData(version) {
		return nil
	}
	data := PostgresDataDir(version)
	return initdb(version, data)
}

func initdb(version, data string, extra ...string) error {
	fmt.Printf("Initialising an empty PostgreSQL %s cluster (user postgres, no password)\n", config.LineOf("postgres", version))
	os.RemoveAll(data)
	if err := os.MkdirAll(filepath.Dir(data), 0o755); err != nil {
		return err
	}
	args := append([]string{"-D", data, "-U", "postgres", "--auth=trust", "--encoding=UTF8", "--locale=C"}, extra...)
	cmd := exec.Command(PostgresBin(version, "initdb"), args...)
	// initdb's chatter (and its pg_ctl hint) only matters when it fails.
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(data)
		return fmt.Errorf("initdb: %w\n%s", err, out)
	}
	return nil
}

// UpgradePostgres moves a cluster to a newer major with pg_upgrade, which
// copies (not links) the data, so the old cluster stays usable as a way
// back. Both servers must be stopped.
func UpgradePostgres(from, to string) error {
	// pg_upgrade needs the new cluster to match the old one's data page
	// checksums. PostgreSQL 18 turned them on by default; older majors off.
	sums, err := hasChecksums(from)
	if err != nil {
		return err
	}
	var flags []string
	if sums {
		flags = []string{"--data-checksums"}
	} else if n, _ := strconv.Atoi(config.LineOf("postgres", to)); n >= 18 {
		flags = []string{"--no-data-checksums"}
	}
	newData := PostgresDataDir(to)
	if err := initdb(to, newData, flags...); err != nil {
		return err
	}
	// pg_upgrade writes its logs into the current directory (older
	// releases) or the new data folder; keep the former out of the way.
	work, err := os.MkdirTemp(config.CacheDir(), "pg_upgrade-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	cmd := exec.Command(PostgresBin(to, "pg_upgrade"),
		"--old-bindir", filepath.Join(PostgresInstallDir(from), "bin"),
		"--new-bindir", filepath.Join(PostgresInstallDir(to), "bin"),
		"--old-datadir", PostgresDataDir(from),
		"--new-datadir", newData,
		"--username", "postgres")
	cmd.Dir = work
	// pg_upgrade prints ~60 progress lines and hints that do not apply here
	// (laman runs vacuumdb itself); show them only when it fails.
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(newData) // leave no half-upgraded cluster behind
		return fmt.Errorf("pg_upgrade: %w\n%s", err, out)
	}
	return nil
}

func hasPrefixAny(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// hasChecksums reads a stopped cluster's control file: a "Data page
// checksum version" of 0 means checksums are off.
func hasChecksums(version string) (bool, error) {
	out, err := exec.Command(PostgresBin(version, "pg_controldata"), "-D", PostgresDataDir(version)).Output()
	if err != nil {
		return false, fmt.Errorf("pg_controldata: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "Data page checksum version" {
			return strings.TrimSpace(v) != "0", nil
		}
	}
	return false, fmt.Errorf("pg_controldata did not report data page checksums")
}
