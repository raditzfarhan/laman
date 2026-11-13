package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// PHPFit describes a site whose composer.json PHP requirement the site's
// current PHP does not meet. Use is the newest installed PHP that fits;
// Install is the newest installable line that fits when none is installed.
type PHPFit struct {
	Needs, From, RunsOn, Use, Install string
}

// ComposerPHP reads a project's PHP requirement: config.platform.php, else
// require.php. Both are "" when there is no composer.json or no constraint.
func ComposerPHP(dir string) (rng, from string) {
	b, err := os.ReadFile(filepath.Join(dir, "composer.json"))
	if err != nil {
		return "", ""
	}
	var j struct {
		Require map[string]any `json:"require"`
		Config  struct {
			Platform map[string]any `json:"platform"`
		} `json:"config"`
	}
	if json.Unmarshal(b, &j) != nil {
		return "", ""
	}
	if s, _ := j.Config.Platform["php"].(string); strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s), "composer.json (config.platform.php)"
	}
	if s, _ := j.Require["php"].(string); strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s), "composer.json"
	}
	return "", ""
}

// InComposerRange is InRange for Composer constraint syntax.
func InComposerRange(v, rng string) (in, ok bool) {
	return InRange(v, normalizeComposer(rng))
}

func normalizeComposer(rng string) string {
	// strip @stability flags
	fields := strings.Fields(strings.ReplaceAll(rng, ",", " "))
	for i, f := range fields {
		if k := strings.Index(f, "@"); k >= 0 {
			fields[i] = f[:k]
		}
	}
	var out []string
	for _, f := range fields {
		switch {
		case f == "":
		case strings.Contains(f, "|"):
			out = append(out, "||") // "|" and "||" both mean or
		case strings.HasPrefix(f, "~"):
			out = append(out, tilde(f[1:])...)
		default:
			out = append(out, f)
		}
	}
	return strings.Join(out, " ")
}

// tilde turns Composer's ~X.Y into >=X.Y <X+1 and ~X.Y.Z into >=X.Y.Z <X.Y+1.
func tilde(s string) []string {
	v, n, ok := partial(s)
	if !ok || n == 0 {
		return []string{"~" + s}
	}
	hi := semver{v[0] + 1}
	if n >= 3 {
		hi = semver{v[0], v[1] + 1}
	}
	return []string{">=" + s, "<" + strconv.Itoa(hi[0]) + "." + strconv.Itoa(hi[1]) + "." + strconv.Itoa(hi[2])}
}

// PHPFitFor reports how site's PHP fails its composer.json requirement, or
// nil when there is no (understood) requirement or the site's PHP fits.
func (c *Config) PHPFitFor(site, dir string, lines []string) *PHPFit {
	rng, from := ComposerPHP(dir)
	if rng == "" {
		return nil
	}
	runs, _ := c.PHPFor(site)
	if _, ok := InComposerRange("8.0.0", rng); !ok {
		return nil
	}
	if in, _ := InComposerRange(runs, rng); in {
		return nil
	}
	fit := &PHPFit{Needs: rng, From: from, RunsOn: runs}
	inst := Installed("php")
	for i := len(inst) - 1; i >= 0; i-- {
		if in, ok := InComposerRange(inst[i], rng); in && ok {
			fit.Use = inst[i]
			return fit
		}
	}
	for _, l := range lines {
		if in, ok := InComposerRange(l, rng); in && ok && (fit.Install == "" || CompareVersions(l, fit.Install) > 0) {
			fit.Install = l
		}
	}
	return fit
}
