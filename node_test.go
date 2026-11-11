package main

import "testing"

func TestNodeSpec(t *testing.T) {
	for in, want := range map[string]string{
		"v20.11.0\n":    "20.11.0",
		"20":            "20",
		" 22.3 \r\n# x": "22.3",
		"lts/*":         "",
		"lts/iron":      "",
		"node":          "",
		"":              "",
	} {
		if got := nodeSpec(in); got != want {
			t.Errorf("nodeSpec(%q) = %q, want %q", in, got, want)
		}
	}
}
