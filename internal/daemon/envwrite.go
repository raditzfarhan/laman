package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The only place laman edits a file of the user's project: it sets the
// Reverb keys in the project's .env, after a backup, and touches nothing else.

type envKV struct{ Key, Value string }

// reverbEnv is what laman wants in a project's .env for Reverb behind its
// proxy: clients use host/scheme/port, the Reverb server itself listens on
// loopback at reverbPort.
func reverbEnv(host, scheme string, port, reverbPort int) []envKV {
	p := strconv.Itoa(port)
	return []envKV{
		{"BROADCAST_CONNECTION", "reverb"},
		{"REVERB_HOST", host},
		{"REVERB_PORT", p},
		{"REVERB_SCHEME", scheme},
		{"REVERB_SERVER_HOST", "127.0.0.1"},
		{"REVERB_SERVER_PORT", strconv.Itoa(reverbPort)},
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

// keyLinePrefix returns the part of line before the value (leading space,
// optional `export `, `KEY=`) when line assigns key; ok is false otherwise.
func keyLinePrefix(line, key string) (string, bool) {
	rest := strings.TrimLeft(line, " \t")
	lead := line[:len(line)-len(rest)]
	if after, ok := strings.CutPrefix(rest, "export "); ok {
		trimmed := strings.TrimLeft(after, " \t")
		lead += rest[:len(rest)-len(after)] + after[:len(after)-len(trimmed)]
		rest = trimmed
	}
	if strings.HasPrefix(rest, key+"=") {
		return lead + key + "=", true
	}
	return "", false
}

// writeEnvKeys backs path up to path+".laman-backup", then replaces the first
// line of each key in place (keeping indentation and any `export `), appends
// missing keys, and writes the result atomically. Every other byte is kept.
func writeEnvKeys(path string, kvs []envKV) error {
	orig, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".laman-backup", orig, 0o644); err != nil {
		return err
	}
	eol := "\n"
	if strings.Contains(string(orig), "\r\n") {
		eol = "\r\n"
	}
	lines := strings.SplitAfter(string(orig), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for _, kv := range kvs {
		done := false
		for i, l := range lines {
			body := strings.TrimRight(l, "\r\n")
			if prefix, ok := keyLinePrefix(body, kv.Key); ok {
				lines[i] = prefix + kv.Value + l[len(body):]
				done = true
				break
			}
		}
		if done {
			continue
		}
		if n := len(lines); n > 0 && !strings.HasSuffix(lines[n-1], "\n") {
			lines[n-1] += eol
		}
		lines = append(lines, kv.Key+"="+kv.Value+eol)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".env.laman-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.WriteString(strings.Join(lines, ""))
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(name)
		return werr
	}
	if fi, err := os.Stat(path); err == nil {
		os.Chmod(name, fi.Mode().Perm())
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
