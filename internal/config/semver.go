package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Version ranges as package.json writes them in "engines": ">=22.13",
// "^20.19.0 || >=22.12.0", "18.x", "~22.1", "20 - 22". Pre-release tags
// are ignored.

// InRange reports whether version v is in rng. ok is false when rng is not
// a range laman understands, in which case v counts as fine.
func InRange(v, rng string) (in, ok bool) {
	rng = strings.TrimSpace(rng)
	if rng == "" || rng == "*" || rng == "x" {
		return true, true
	}
	ver, good := parseVer(v)
	if !good {
		return true, false
	}
	for _, alt := range strings.Split(rng, "||") {
		cmps, good := comparators(alt)
		if !good {
			return true, false
		}
		all := true
		for _, c := range cmps {
			if !c.match(ver) {
				all = false
				break
			}
		}
		if all {
			return true, true
		}
	}
	return false, true
}

type semver [3]int

func (a semver) cmp(b semver) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func parseVer(s string) (semver, bool) {
	var v semver
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	s, _, _ = strings.Cut(s, "-")
	s, _, _ = strings.Cut(s, "+")
	parts := strings.Split(s, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

type comparator struct {
	op string // >=, >, <=, <, =
	v  semver
}

func (c comparator) match(v semver) bool {
	d := v.cmp(c.v)
	switch c.op {
	case ">=":
		return d >= 0
	case ">":
		return d > 0
	case "<=":
		return d <= 0
	case "<":
		return d < 0
	}
	return d == 0
}

// partial reads "22", "22.13", "22.x", "22.13.1": the numbers given and how
// many there are (x and * stop the count).
func partial(s string) (semver, int, bool) {
	var v semver
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	s, _, _ = strings.Cut(s, "-")
	if s == "" {
		return v, 0, false
	}
	n := 0
	for i, p := range strings.Split(s, ".") {
		if i > 2 {
			return v, 0, false
		}
		if p == "x" || p == "X" || p == "*" {
			break
		}
		x, err := strconv.Atoi(p)
		if err != nil || x < 0 {
			return v, 0, false
		}
		v[i] = x
		n++
	}
	return v, n, true
}

// next is the first version past a partial one: 22 -> 23.0.0, 22.13 -> 22.14.0.
func next(v semver, n int) semver {
	switch n {
	case 1:
		return semver{v[0] + 1, 0, 0}
	case 2:
		return semver{v[0], v[1] + 1, 0}
	}
	return semver{v[0], v[1], v[2] + 1}
}

// comparators turns one alternative ("^20.19.0", ">=18 <21", "20 - 22")
// into simple comparisons.
func comparators(s string) ([]comparator, bool) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", " "))
	if s == "" || s == "*" {
		return nil, true
	}
	if lo, hi, found := strings.Cut(s, " - "); found {
		a, _, ok1 := partial(lo)
		b, n, ok2 := partial(hi)
		if !ok1 || !ok2 {
			return nil, false
		}
		out := []comparator{{">=", a}}
		if n == 3 {
			return append(out, comparator{"<=", b}), true
		}
		if n > 0 {
			out = append(out, comparator{"<", next(b, n)})
		}
		return out, true
	}
	// Join an operator written apart from its version: ">= 22".
	var words []string
	for _, w := range strings.Fields(s) {
		if len(words) > 0 && strings.Trim(words[len(words)-1], "<>=^~") == "" {
			words[len(words)-1] += w
			continue
		}
		words = append(words, w)
	}
	var out []comparator
	for _, w := range words {
		op := ""
		for _, p := range []string{">=", "<=", ">", "<", "=", "^", "~"} {
			if strings.HasPrefix(w, p) {
				op, w = p, w[len(p):]
				break
			}
		}
		v, n, ok := partial(w)
		if !ok {
			return nil, false
		}
		switch op {
		case ">=", "<", "=", "":
			if n == 0 { // "*"
				continue
			}
			if op == "" || op == "=" {
				if n == 3 {
					out = append(out, comparator{"=", v})
				} else {
					out = append(out, comparator{">=", v}, comparator{"<", next(v, n)})
				}
			} else {
				out = append(out, comparator{op, v})
			}
		case ">":
			if n == 3 {
				out = append(out, comparator{">", v})
			} else {
				out = append(out, comparator{">=", next(v, n)})
			}
		case "<=":
			if n == 3 {
				out = append(out, comparator{"<=", v})
			} else {
				out = append(out, comparator{"<", next(v, n)})
			}
		case "~":
			out = append(out, comparator{">=", v})
			if n <= 1 {
				out = append(out, comparator{"<", next(v, 1)})
			} else {
				out = append(out, comparator{"<", next(v, 2)})
			}
		case "^":
			out = append(out, comparator{">=", v})
			switch {
			case v[0] > 0 || n <= 1:
				out = append(out, comparator{"<", next(v, 1)})
			case v[1] > 0 || n == 2:
				out = append(out, comparator{"<", next(v, 2)})
			default:
				out = append(out, comparator{"<", next(v, 3)})
			}
		}
	}
	return out, true
}

// NodeEngines finds the nearest package.json at or above dir and returns
// its engines.node range ("" when it has none) and the file's path.
func NodeEngines(dir string) (rng, from string) {
	for ; ; dir = filepath.Dir(dir) {
		p := filepath.Join(dir, "package.json")
		if b, err := os.ReadFile(p); err == nil {
			var pkg struct {
				Engines map[string]any `json:"engines"`
			}
			if json.Unmarshal(b, &pkg) == nil {
				if s, ok := pkg.Engines["node"].(string); ok {
					return strings.TrimSpace(s), p
				}
			}
			return "", p
		}
		if parent := filepath.Dir(dir); parent == dir {
			return "", ""
		}
	}
}
