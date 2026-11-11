package install

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"laman/internal/config"
)

const mariadbAPI = "https://downloads.mariadb.org/rest-api/mariadb/"

type mariadbFile struct {
	FileName    string `json:"file_name"`
	PackageType string `json:"package_type"`
	OS          string `json:"os"`
	URL         string `json:"file_download_url"`
	Checksum    struct {
		Sha256 string `json:"sha256sum"`
	} `json:"checksum"`
}

// MariaDB installs the latest release of a major line ("11.4"), or of the
// newest stable LTS line when want is empty. It does not touch data: see
// InitMariaDB, and `laman upgrade mariadb` for moving data to a newer line.
func MariaDB(want string) (string, error) {
	major := want
	if major == "" {
		var err error
		if major, err = mariadbLTS(); err != nil {
			return "", err
		}
	} else if parts := strings.Split(major, "."); len(parts) > 2 {
		major = parts[0] + "." + parts[1]
	}

	var latest struct {
		Releases map[string]struct {
			Files []mariadbFile `json:"files"`
		} `json:"releases"`
	}
	if err := getJSON(mariadbAPI+major+"/latest/", &latest); err != nil {
		return "", fmt.Errorf("MariaDB %s: %w", major, err)
	}
	var version string
	var file *mariadbFile
	for v, r := range latest.Releases {
		for i, f := range r.Files {
			if f.OS == "Windows" && f.PackageType == "ZIP file" && strings.HasSuffix(f.FileName, "-winx64.zip") {
				version, file = v, &r.Files[i]
			}
		}
	}
	if file == nil {
		return "", fmt.Errorf("MariaDB %s: no Windows x64 zip published", major)
	}

	dest := filepath.Join(config.BinDir("mariadb"), version)
	if exists(dest) {
		fmt.Printf("MariaDB %s is already installed\n", version)
	} else {
		fmt.Printf("Installing MariaDB %s\n", version)
		url := strings.Replace(file.URL, "http://", "https://", 1)
		zip, err := fetch(url, file.Checksum.Sha256)
		if err != nil {
			return "", err
		}
		if err := unpack(zip, dest); err != nil {
			return "", err
		}
	}
	return version, nil
}

func mariadbLTS() (string, error) {
	var list struct {
		Major []struct {
			ID      string `json:"release_id"`
			Status  string `json:"release_status"`
			Support string `json:"release_support_type"`
		} `json:"major_releases"`
	}
	if err := getJSON(mariadbAPI, &list); err != nil {
		return "", err
	}
	best := ""
	for _, m := range list.Major {
		if m.Status == "Stable" && m.Support == "Long Term Support" && config.CompareVersions(m.ID, best) > 0 {
			best = m.ID
		}
	}
	if best == "" {
		return "", fmt.Errorf("no stable MariaDB LTS release found")
	}
	return best, nil
}

// MariaDBInstallDir is where an installed version lives.
func MariaDBInstallDir(version string) string {
	return filepath.Join(config.BinDir("mariadb"), version)
}

// MariaDBBin finds a binary that was renamed from mysql* to mariadb* over time.
func MariaDBBin(installDir string, names ...string) string {
	for _, n := range names {
		p := filepath.Join(installDir, "bin", n+".exe")
		if exists(p) {
			return p
		}
	}
	return ""
}

// InitMariaDB creates an empty data folder for a version's line (root
// user, no password) unless it already has one.
func InitMariaDB(version string, port int) error {
	data := MariaDBDataDir(version)
	if HasMariaDBData(version) {
		return nil
	}
	tool := MariaDBBin(MariaDBInstallDir(version), "mariadb-install-db", "mysql_install_db")
	if tool == "" {
		return fmt.Errorf("MariaDB install has no mariadb-install-db.exe")
	}
	fmt.Printf("Initialising an empty MariaDB %s data folder (root user, no password)\n", config.Line(version))
	os.RemoveAll(data)
	if err := os.MkdirAll(filepath.Dir(data), 0o755); err != nil {
		return err
	}
	cmd := exec.Command(tool, "--datadir="+data, "--port="+strconv.Itoa(port))
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		os.RemoveAll(data)
		return fmt.Errorf("mariadb-install-db: %w", err)
	}
	return nil
}
