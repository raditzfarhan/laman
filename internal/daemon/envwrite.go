package daemon

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// The only place laman edits a file of the user's project: it sets the
// Reverb keys in the project's .env, after a backup kept in laman's own data
// folder (never in the project), and touches nothing else.

type envKV struct{ Key, Value string }

// reverbEnv is what laman wants in a project's .env for Reverb behind its
// proxy: the browser (VITE_*) uses the site's host/scheme/port, while PHP
// (REVERB_*) talks straight to the Reverb process on loopback over plain http,
// because that is simpler and faster than going through the proxy.
func reverbEnv(host, scheme string, port, reverbPort int) []envKV {
	p := strconv.Itoa(port)
	rp := strconv.Itoa(reverbPort)
	return []envKV{
		{"BROADCAST_CONNECTION", "reverb"},
		{"REVERB_HOST", "127.0.0.1"},
		{"REVERB_PORT", rp},
		{"REVERB_SCHEME", "http"},
		{"REVERB_SERVER_HOST", "127.0.0.1"},
		{"REVERB_SERVER_PORT", rp},
		{"VITE_REVERB_HOST", host},
		{"VITE_REVERB_PORT", p},
		{"VITE_REVERB_SCHEME", scheme},
	}
}

// envMismatch lists the keys of want whose value in vals differs or is missing.
func envMismatch(vals map[string]string, want []envKV) []string {
	var out []string
	for _, kv := range want {
		v, ok := vals[kv.Key]
		if ok && len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		if !ok || v != kv.Value {
			out = append(out, kv.Key)
		}
	}
	return out
}

type envChange struct {
	Op    string `json:"op"` // "set" | "delete"
	Key   string `json:"key"`
	Value string `json:"value"`
}

var envKeyName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// applyEnvChanges applies set/delete edits to .env text and returns the new
// text. Only the lines of the edited keys change: a set rewrites the value of
// the key's last assignment (prefix, spacing around `=`, quote style when
// still valid, trailing comment and line ending are kept) or appends the key;
// a delete drops every assignment line of the key.
func applyEnvChanges(text string, changes []envChange) (string, error) {
	if strings.Contains(strings.ReplaceAll(text, "\r\n", ""), "\r") {
		return "", errors.New("unusual line endings in .env; edit it by hand")
	}
	eol := "\n"
	if strings.Contains(text, "\r\n") {
		eol = "\r\n"
	}
	for _, c := range changes {
		if !envKeyName.MatchString(c.Key) {
			return "", errors.New("invalid key name " + strconv.Quote(c.Key))
		}
		switch c.Op {
		case "set":
			if strings.ContainsAny(c.Value, "\r\n") {
				return "", errors.New(c.Key + ": value has a line break; use Raw")
			}
		case "delete":
		default:
			return "", errors.New("unknown change " + strconv.Quote(c.Op))
		}
		lines := strings.SplitAfter(text, "\n")
		if lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		var at []int // 0-based lines assigning the key
		for _, l := range parseEnv(text) {
			if l.Key != c.Key {
				continue
			}
			if l.Multiline {
				return "", errors.New(c.Key + " has a multi-line value; edit it in Raw")
			}
			at = append(at, l.Line-1)
		}
		switch {
		case c.Op == "delete":
			drop := map[int]bool{}
			for _, i := range at {
				drop[i] = true
			}
			var kept []string
			for i, l := range lines {
				if !drop[i] {
					kept = append(kept, l)
				}
			}
			lines = kept
		case len(at) > 0:
			i := at[len(at)-1]
			nl, err := rewriteEnvLine(lines[i], c.Value)
			if err != nil {
				return "", errors.New(c.Key + ": " + err.Error())
			}
			lines[i] = nl
		default:
			if n := len(lines); n > 0 && !strings.HasSuffix(lines[n-1], "\n") {
				lines[n-1] += eol
			}
			lines = append(lines, c.Key+"="+encodeEnvValue(c.Value, 0)+eol)
		}
		text = strings.Join(lines, "")
	}
	return text, nil
}

var envLinePrefix = regexp.MustCompile(`^[ \t]*(?:export[ \t]+)?[A-Za-z_][A-Za-z0-9_]*[ \t]*=[ \t]*`)

// rewriteEnvLine replaces the value of one assignment line (with its EOL),
// keeping everything around it.
func rewriteEnvLine(line, value string) (string, error) {
	bom := ""
	if strings.HasPrefix(line, "\uFEFF") { // the BOM of the file's first line stays as is
		bom, line = "\uFEFF", line[len("\uFEFF"):]
	}
	body := strings.TrimRight(line, "\r\n")
	eol := line[len(body):]
	prefix := envLinePrefix.FindString(body)
	if prefix == "" {
		return "", errors.New("cannot edit this line in place; use Raw")
	}
	rest := body[len(prefix):]
	trimmed := strings.TrimRight(rest, " \t")
	space := rest[len(trimmed):]
	_, quote, comment, _ := decodeEnvValue(trimmed)
	tail := comment + space
	if quote != 0 {
		tail = trimmed[closingQuote(trimmed)+1:] + space
	}
	enc := encodeEnvValue(value, quote)
	if enc != "" && strings.HasPrefix(tail, "#") {
		tail = " " + tail
	}
	return bom + prefix + enc + tail + eol, nil
}
