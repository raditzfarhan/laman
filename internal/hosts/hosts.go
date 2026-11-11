// Package hosts maintains a laman-owned block in the Windows hosts file.
package hosts

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	beginMarker = "# BEGIN laman"
	endMarker   = "# END laman"
	// The tool was called perch before; its block is removed on the next sync.
	legacyBegin = "# BEGIN perch"
	legacyEnd   = "# END perch"
)

func Path() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "drivers", "etc", "hosts")
}

// Merge replaces (or appends) the laman block in content. Everything outside
// the markers is left untouched; line endings follow the existing file.
func Merge(content string, names []string) string {
	eol := "\n"
	if strings.Contains(content, "\r\n") || content == "" {
		eol = "\r\n"
	}
	var kept []string
	inBlock := false
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		switch strings.TrimSpace(line) {
		case beginMarker, legacyBegin:
			inBlock = true
			continue
		case endMarker, legacyEnd:
			inBlock = false
			continue
		}
		if !inBlock {
			kept = append(kept, line)
		}
	}
	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
		kept = kept[:len(kept)-1]
	}
	if len(names) > 0 {
		if len(kept) > 0 {
			kept = append(kept, "")
		}
		kept = append(kept, beginMarker)
		for _, n := range names {
			kept = append(kept, "127.0.0.1 "+n)
		}
		kept = append(kept, endMarker)
	}
	return strings.Join(kept, eol) + eol
}

// InSync reports whether the hosts file already has exactly these names.
func InSync(names []string) bool {
	b, err := os.ReadFile(Path())
	if err != nil {
		return false
	}
	return Merge(string(b), names) == string(b)
}

// Sync writes the block. It returns an error wrapping os.ErrPermission when
// the process is not elevated.
func Sync(names []string) (changed bool, err error) {
	p := Path()
	b, err := os.ReadFile(p)
	if err != nil {
		return false, err
	}
	next := Merge(string(b), names)
	if next == string(b) {
		return false, nil
	}
	return true, os.WriteFile(p, []byte(next), 0o644)
}
