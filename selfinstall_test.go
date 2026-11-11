package main

import "testing"

func TestPathWith(t *testing.T) {
	dir := `C:\Users\me\.laman\bin`
	cases := []struct {
		in, want string
		added    bool
	}{
		{``, dir, true},
		{`C:\a;C:\b`, `C:\a;C:\b;` + dir, true},
		{`C:\a;C:\b;`, `C:\a;C:\b;` + dir, true},
		{`C:\a;c:\users\ME\.laman\bin\;C:\b`, `C:\a;c:\users\ME\.laman\bin\;C:\b`, false},
	}
	t.Setenv("LAMANTESTHOME", `C:\Users\me`)
	cases = append(cases, struct {
		in, want string
		added    bool
	}{`C:\a;%LAMANTESTHOME%\.laman\bin`, `C:\a;%LAMANTESTHOME%\.laman\bin`, false})
	for _, c := range cases {
		got, added := pathWith(c.in, dir)
		if got != c.want || added != c.added {
			t.Errorf("pathWith(%q) = %q, %v; want %q, %v", c.in, got, added, c.want, c.added)
		}
	}
}

func TestPathWithout(t *testing.T) {
	dir := `C:\Users\me\.laman\bin`
	cases := []struct {
		in, want string
		removed  bool
	}{
		{`C:\a;C:\Users\me\.laman\bin;C:\b`, `C:\a;C:\b`, true},
		{`C:\a;c:\users\ME\.laman\bin\`, `C:\a`, true},
		{`C:\Users\me\.laman\bin`, ``, true},
		{`C:\a;C:\b`, `C:\a;C:\b`, false},
		{``, ``, false},
	}
	for _, c := range cases {
		got, removed := pathWithout(c.in, dir)
		if got != c.want || removed != c.removed {
			t.Errorf("pathWithout(%q) = %q, %v; want %q, %v", c.in, got, removed, c.want, c.removed)
		}
	}
}
