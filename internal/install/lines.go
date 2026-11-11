package install

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"laman/internal/config"
)

// Lines lists the release lines a component can install, newest first:
// PHP "8.5", Node majors "24", MariaDB "12.3", PostgreSQL "18", Redis
// "8.10". It asks the same sources the installers use.
func Lines(kind string) ([]string, error) {
	var out []string
	switch kind {
	case "php":
		l, err := PHPLines()
		if err != nil {
			return nil, err
		}
		out = l
	case "node":
		var index []nodeRelease
		if err := getJSON(nodeDist+"index.json", &index); err != nil {
			return nil, err
		}
		// LTS majors, plus the newest major even before it becomes LTS.
		seen := map[string]bool{}
		for i, r := range index {
			if !nodeVersion.MatchString(r.Version) || !hasFile(r.Files, "win-x64-zip") {
				continue
			}
			major := strings.SplitN(strings.TrimPrefix(r.Version, "v"), ".", 2)[0]
			if !seen[major] && (r.isLTS() || i < 3) {
				seen[major] = true
				out = append(out, major)
			}
		}
	case "mariadb":
		var list struct {
			Major []struct {
				ID     string  `json:"release_id"`
				Status string  `json:"release_status"`
				EOL    *string `json:"release_eol_date"`
			} `json:"major_releases"`
		}
		if err := getJSON(mariadbAPI, &list); err != nil {
			return nil, err
		}
		today := time.Now().Format("2006-01-02")
		for _, m := range list.Major {
			// Stable and still maintained (no end date yet, or a future one).
			if m.Status == "Stable" && (m.EOL == nil || *m.EOL >= today) {
				out = append(out, m.ID)
			}
		}
	case "postgres":
		var list []struct {
			Major     string `json:"major"`
			Supported bool   `json:"supported"`
		}
		if err := getJSON(pgVersions, &list); err != nil {
			return nil, err
		}
		for _, v := range list {
			if v.Supported {
				out = append(out, v.Major)
			}
		}
	case "redis":
		var list []ghRelease
		if err := getJSON(redisReleases, &list); err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, r := range list {
			v := strings.TrimPrefix(r.Tag, "v")
			if r.Draft || r.Prerelease || !versionTag.MatchString(v) || strings.Contains(v, "-") {
				continue
			}
			if _, err := pickRedis([]ghRelease{r}, ""); err != nil {
				continue // no plain Windows build in this release
			}
			if line := config.Line(v); !seen[line] {
				seen[line] = true
				out = append(out, line)
			}
		}
	default:
		return nil, fmt.Errorf("unknown component %q", kind)
	}
	sort.Slice(out, func(i, j int) bool { return config.CompareVersions(out[i], out[j]) > 0 })
	if kind == "node" && len(out) > 5 {
		out = out[:5] // older LTS majors are past end of life
	}
	return out, nil
}
