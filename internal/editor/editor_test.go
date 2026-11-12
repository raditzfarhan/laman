package editor

import (
	"errors"
	"testing"
)

func fake(t *testing.T, onPath map[string]string, files map[string][]string) {
	t.Helper()
	oldL, oldG, oldE := lookPath, glob, getenv
	t.Cleanup(func() { lookPath, glob, getenv = oldL, oldG, oldE })
	lookPath = func(c string) (string, error) {
		if p, ok := onPath[c]; ok {
			return p, nil
		}
		return "", errors.New("not found")
	}
	glob = func(p string) ([]string, error) { return files[p], nil }
	getenv = func(k string) string {
		return map[string]string{"LOCALAPPDATA": `C:\U\AppData\Local`, "ProgramFiles": `C:\Program Files`}[k]
	}
}

func TestFoundOrderAndPaths(t *testing.T) {
	fake(t, map[string]string{"cursor": `C:\bin\cursor.cmd`}, map[string][]string{
		`C:\U\AppData\Local\Programs\Microsoft VS Code\bin\code.cmd`: {`C:\U\AppData\Local\Programs\Microsoft VS Code\bin\code.cmd`},
		`C:\Program Files\JetBrains\PhpStorm*\bin\phpstorm64.exe`:    {`C:\Program Files\JetBrains\PhpStorm 2025.1\bin\phpstorm64.exe`, `C:\Program Files\JetBrains\PhpStorm 2026.2\bin\phpstorm64.exe`},
	})
	got := Found()
	if len(got) != 3 || got[0].ID != "vscode" || got[1].ID != "cursor" || got[2].ID != "phpstorm" {
		t.Fatalf("found %+v", got)
	}
	if got[1].Path != `C:\bin\cursor.cmd` {
		t.Errorf("PATH should win: %s", got[1].Path)
	}
	if got[2].Path != `C:\Program Files\JetBrains\PhpStorm 2026.2\bin\phpstorm64.exe` {
		t.Errorf("newest PhpStorm expected: %s", got[2].Path)
	}
}

func TestPick(t *testing.T) {
	found := []Editor{{ID: "vscode"}, {ID: "cursor"}}
	if e, _ := Pick("cursor", found); e.ID != "cursor" {
		t.Errorf("chosen: %s", e.ID)
	}
	if e, _ := Pick("zed", found); e.ID != "vscode" {
		t.Errorf("missing choice falls back to the first: %s", e.ID)
	}
	if e, _ := Pick("", found); e.ID != "vscode" {
		t.Errorf("automatic: %s", e.ID)
	}
	if _, ok := Pick("", nil); ok {
		t.Error("nothing installed")
	}
}

func TestExpandUnsetVariable(t *testing.T) {
	fake(t, nil, nil)
	if got := expand(`%NOPE%\x`); got != `%NOPE%\x` {
		t.Errorf("unset stays: %s", got)
	}
	if got := expand(`%ProgramFiles%\a`); got != `C:\Program Files\a` {
		t.Errorf("expanded: %s", got)
	}
}
