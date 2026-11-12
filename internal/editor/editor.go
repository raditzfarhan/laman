// Package editor finds the code editors installed on this computer and
// builds the command that opens a folder in one.
package editor

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Editor is an editor found on this computer.
type Editor struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"-"`
}

type known struct {
	id, name string
	cmds     []string // names looked up on PATH
	paths    []string // usual install places; %VAR% expanded, globs allowed
}

// Order matters: with no choice saved, the first one found is used.
var editors = []known{
	{"vscode", "VS Code", []string{"code"}, []string{
		`%LOCALAPPDATA%\Programs\Microsoft VS Code\bin\code.cmd`,
		`%ProgramFiles%\Microsoft VS Code\bin\code.cmd`}},
	{"cursor", "Cursor", []string{"cursor"}, []string{
		`%LOCALAPPDATA%\Programs\cursor\resources\app\bin\cursor.cmd`,
		`%ProgramFiles%\cursor\resources\app\bin\cursor.cmd`}},
	{"windsurf", "Windsurf", []string{"windsurf"}, []string{
		`%LOCALAPPDATA%\Programs\Windsurf\bin\windsurf.cmd`}},
	{"zed", "Zed", []string{"zed"}, []string{
		`%LOCALAPPDATA%\Programs\Zed\bin\zed.exe`,
		`%LOCALAPPDATA%\Programs\Zed\Zed.exe`}},
	{"phpstorm", "PhpStorm", []string{"phpstorm", "phpstorm64"}, []string{
		`%LOCALAPPDATA%\JetBrains\Toolbox\scripts\phpstorm.cmd`,
		`%LOCALAPPDATA%\Programs\PhpStorm\bin\phpstorm64.exe`,
		`%ProgramFiles%\JetBrains\PhpStorm*\bin\phpstorm64.exe`}},
	{"sublime", "Sublime Text", []string{"subl"}, []string{
		`%ProgramFiles%\Sublime Text\subl.exe`,
		`%ProgramFiles%\Sublime Text 3\subl.exe`}},
}

// IDs lists every editor laman knows, found or not.
func IDs() []string {
	ids := make([]string, len(editors))
	for i, k := range editors {
		ids[i] = k.id
	}
	return ids
}

// lookPath and glob are swapped in tests.
var (
	lookPath = exec.LookPath
	glob     = filepath.Glob
	getenv   = os.Getenv
)

// Found lists the editors installed here, in the order above.
func Found() []Editor {
	var out []Editor
	for _, k := range editors {
		if p := locate(k); p != "" {
			out = append(out, Editor{ID: k.id, Name: k.name, Path: p})
		}
	}
	return out
}

func locate(k known) string {
	for _, c := range k.cmds {
		if p, err := lookPath(c); err == nil {
			return p
		}
	}
	for _, pat := range k.paths {
		pat = expand(pat)
		if strings.Contains(pat, `%`) {
			continue // a variable that isn't set
		}
		matches, _ := glob(pat)
		if len(matches) > 0 {
			sort.Strings(matches)
			return matches[len(matches)-1] // the newest version folder
		}
	}
	return ""
}

func expand(s string) string {
	for {
		i := strings.Index(s, "%")
		j := strings.Index(s[i+1:], "%")
		if i < 0 || j < 0 {
			return s
		}
		v := getenv(s[i+1 : i+1+j])
		if v == "" {
			return s
		}
		s = s[:i] + v + s[i+2+j:]
	}
}

// Pick returns the chosen editor if it is installed, else the first found.
func Pick(choice string, found []Editor) (Editor, bool) {
	for _, e := range found {
		if e.ID == choice {
			return e, true
		}
	}
	if len(found) > 0 {
		return found[0], true
	}
	return Editor{}, false
}

// Command opens dir in the editor.
func Command(e Editor, dir string) *exec.Cmd {
	cmd := exec.Command(e.Path, dir)
	cmd.Dir = dir
	return cmd
}
