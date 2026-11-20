package daemon

import (
	"regexp"
	"strings"
)

// Parsing of a project's .env into rows for the editor on the site detail
// page. readEnvFile (projectenv.go) reads through the same parser, so the
// facts shown there agree with the editor.

// envLine is one assignment line of a .env.
type envLine struct {
	Key, Value string
	Quote      byte   // 0, '\'' or '"'
	Line       int    // 1-based line the assignment starts on
	Multiline  bool   // the quoted value runs over several lines
	Comment    string // trailing " # ..." incl. the leading space; unquoted values only
}

// decodeEnvValue splits what follows the `=` (already trimmed) into the
// value, its quote, and the trailing comment of an unquoted value. ok is
// false when a quote is never closed on this text.
func decodeEnvValue(raw string) (value string, quote byte, comment string, ok bool) {
	if raw != "" && (raw[0] == '"' || raw[0] == '\'') {
		end := closingQuote(raw)
		if end < 0 {
			return raw, 0, "", false
		}
		value = raw[1:end]
		if raw[0] == '"' {
			value = strings.NewReplacer(`\"`, `"`, `\`, `\`).Replace(value)
		}
		return value, raw[0], "", true
	}
	if strings.HasPrefix(raw, "#") {
		return "", 0, raw, true
	}
	if i := strings.Index(raw, " #"); i >= 0 {
		v := strings.TrimRight(raw[:i], " \t")
		return v, 0, raw[len(v):], true
	}
	return raw, 0, "", true
}

// encodeEnvValue writes a value back as text: bare when that reads back the
// same, else in the preferred quote ('\” falls back to '"' when the value
// holds a single quote). Only inside '"' are `"` and `\` escaped.
func encodeEnvValue(value string, prefer byte) string {
	if prefer == 0 && !strings.ContainsAny(value, " \t#\"'\n\r") {
		return value
	}
	if prefer == '\'' && !strings.Contains(value, "'") {
		return "'" + value + "'"
	}
	return `"` + strings.NewReplacer(`\`, `\`, `"`, `\"`).Replace(value) + `"`
}

// parseEnv returns the assignment lines of text in file order. A quoted
// value that closes on a later line is one envLine with Multiline set.
func parseEnv(text string) []envLine {
	var out []envLine
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		m := envAssign.FindStringSubmatch(strings.TrimSpace(lines[i]))
		if m == nil {
			continue
		}
		raw := m[2]
		l := envLine{Key: m[1], Line: i + 1}
		v, q, c, ok := decodeEnvValue(raw)
		if !ok {
			// the closing quote may be on a later line
			joined := raw
			for j := i + 1; j < len(lines); j++ {
				joined += "\n" + lines[j]
				if closingQuote(joined) > 0 {
					v, q, c, ok = decodeEnvValue(joined)
					l.Multiline = true
					i = j
					break
				}
			}
		}
		l.Value, l.Quote, l.Comment = v, q, c
		out = append(out, l)
	}
	return out
}

var (
	secretKey = regexp.MustCompile(`(?i)(^|_)(KEY|SECRET|PASSWORD|PASS|PWD|TOKEN|AUTH|PRIVATE|CREDENTIALS?|SALT|DSN)(_|$)`)
	urlSecret = regexp.MustCompile(`^[a-z][a-z0-9+.-]*://[^/@:]*:[^/@]+@`)
)

// isSecretEnv says whether a value is hidden until the user reveals it.
// Empty values are never masked.
func isSecretEnv(key, value string) bool {
	if value == "" {
		return false
	}
	return secretKey.MatchString(key) || urlSecret.MatchString(value)
}

type envRow struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	Masked    bool   `json:"masked"`
	Quoted    string `json:"quoted"`
	Line      int    `json:"line"`
	Multiline bool   `json:"multiline"`
	Ignored   int    `json:"ignored,omitempty"` // line of the later assignment that overrides this one
}

type envMissing struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Masked bool   `json:"masked"`
}

// buildEnvView turns a .env and its .env.example into table rows and the
// example's keys that .env lacks. Masked values are left out.
func buildEnvView(env, example string) (rows []envRow, missing []envMissing) {
	lines := parseEnv(env)
	last := map[string]int{}
	have := map[string]bool{}
	for _, l := range lines {
		last[l.Key] = l.Line
		have[l.Key] = true
	}
	for _, l := range lines {
		r := envRow{Key: l.Key, Value: l.Value, Line: l.Line, Multiline: l.Multiline, Masked: isSecretEnv(l.Key, l.Value)}
		if l.Quote != 0 {
			r.Quoted = string(l.Quote)
		}
		if r.Masked {
			r.Value = ""
		}
		if last[l.Key] != l.Line {
			r.Ignored = last[l.Key]
		}
		rows = append(rows, r)
	}
	for _, l := range parseEnv(example) {
		if have[l.Key] {
			continue
		}
		have[l.Key] = true
		m := envMissing{Key: l.Key, Value: l.Value, Masked: isSecretEnv(l.Key, l.Value)}
		if m.Masked {
			m.Value = ""
		}
		missing = append(missing, m)
	}
	return rows, missing
}
