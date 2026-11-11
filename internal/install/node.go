package install

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"laman/internal/config"
)

const nodeDist = "https://nodejs.org/dist/"

var nodeVersion = regexp.MustCompile(`^v([0-9]+)\.([0-9]+)\.([0-9]+)$`)

type nodeRelease struct {
	Version string   `json:"version"`
	LTS     any      `json:"lts"` // false, or the codename
	Files   []string `json:"files"`
}

func (r nodeRelease) isLTS() bool { b, ok := r.LTS.(bool); return !ok || b }

// Node installs a Node.js release: the newest LTS when want is empty, the
// newest of a major line for "22", or an exact "22.11.0". Pre-releases are
// skipped. The zip is checked against the release's SHASUMS256.txt.
func Node(want string) (string, error) {
	var index []nodeRelease
	if err := getJSON(nodeDist+"index.json", &index); err != nil {
		return "", fmt.Errorf("Node: %w", err)
	}
	rel, err := pickNode(index, want)
	if err != nil {
		return "", err
	}
	version := strings.TrimPrefix(rel.Version, "v")
	dest := filepath.Join(config.BinDir("node"), version)
	if exists(filepath.Join(dest, "node.exe")) {
		fmt.Printf("Node %s is already installed\n", version)
		return version, writeNpmrc(dest)
	}

	name := "node-" + rel.Version + "-win-x64.zip"
	sums, err := getText(nodeDist + rel.Version + "/SHASUMS256.txt")
	if err != nil {
		return "", err
	}
	sum := ""
	for _, line := range strings.Split(sums, "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == name {
			sum = f[0]
		}
	}
	if !shaHex.MatchString(sum) {
		return "", fmt.Errorf("Node %s: %s is not in SHASUMS256.txt", version, name)
	}
	fmt.Printf("Installing Node %s\n", version)
	zip, err := fetch(nodeDist+rel.Version+"/"+name, sum)
	if err != nil {
		return "", err
	}
	if err := unpack(zip, dest); err != nil {
		return "", err
	}
	if !exists(filepath.Join(dest, "node.exe")) {
		return "", fmt.Errorf("Node %s: the zip has no node.exe", version)
	}
	return version, writeNpmrc(dest)
}

func pickNode(index []nodeRelease, want string) (nodeRelease, error) {
	want = strings.TrimPrefix(strings.TrimSpace(want), "v")
	if isRange(want) {
		return pickNodeRange(index, want)
	}
	for _, r := range index { // index.json is newest first
		if !nodeVersion.MatchString(r.Version) || !hasFile(r.Files, "win-x64-zip") {
			continue
		}
		v := strings.TrimPrefix(r.Version, "v")
		switch {
		case want == "" || want == "lts":
			if r.isLTS() {
				return r, nil
			}
		case want == "latest" || want == "current":
			return r, nil
		case v == want || strings.HasPrefix(v, want+"."):
			return r, nil
		}
	}
	if want == "" {
		return nodeRelease{}, fmt.Errorf("nodejs.org lists no LTS release for Windows x64")
	}
	return nodeRelease{}, fmt.Errorf("no Node release matches %q on nodejs.org", want)
}

// isRange tells a package.json range (">=22.13", "^20 || ^22") from a
// version or line ("22", "22.13.0") and the words lts/latest/current.
func isRange(s string) bool { return strings.ContainsAny(s, "<>=^~|xX* ") }

// pickNodeRange: the newest LTS release in the range, else the newest one.
func pickNodeRange(index []nodeRelease, rng string) (nodeRelease, error) {
	var newest *nodeRelease
	for i, r := range index {
		if !nodeVersion.MatchString(r.Version) || !hasFile(r.Files, "win-x64-zip") {
			continue
		}
		in, ok := config.InRange(strings.TrimPrefix(r.Version, "v"), rng)
		if !ok {
			return nodeRelease{}, fmt.Errorf("laman can't read the Node range %q", rng)
		}
		if !in {
			continue
		}
		if r.isLTS() {
			return r, nil
		}
		if newest == nil {
			newest = &index[i]
		}
	}
	if newest != nil {
		return *newest, nil
	}
	return nodeRelease{}, fmt.Errorf("no Node release on nodejs.org fits %q", rng)
}

func hasFile(files []string, f string) bool {
	for _, x := range files {
		if x == f {
			return true
		}
	}
	return false
}

// NpmPrefix is where `npm install -g` puts tools. It is shared by every Node
// version, so global tools survive `laman use node`, and it is on PATH.
func NpmPrefix() string { return filepath.Join(config.Home(), "npm") }

// writeNpmrc points npm's built-in config (node_modules/npm/npmrc, read
// before the user's ~/.npmrc) at the shared global prefix.
func writeNpmrc(nodeDir string) error {
	if err := os.MkdirAll(NpmPrefix(), 0o755); err != nil {
		return err
	}
	rc := filepath.Join(nodeDir, "node_modules", "npm", "npmrc")
	return os.WriteFile(rc, []byte("prefix="+NpmPrefix()+"\n"), 0o644)
}

// NodeDir returns the folder of an installed Node version, or "".
func NodeDir(version string) string {
	if version == "" {
		return ""
	}
	d := filepath.Join(config.BinDir("node"), version)
	if !exists(filepath.Join(d, "node.exe")) {
		return ""
	}
	return d
}
