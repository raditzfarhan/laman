package main

import "testing"

func TestWorkerArgs(t *testing.T) {
	cases := []struct {
		kind string
		args []string
		ok   bool
	}{
		{"reverb", []string{"env"}, true},
		{"reverb", []string{"on", "acme"}, true},
		{"queue", []string{"status"}, true},
		{"queue", []string{"log", "acme"}, true},
		{"queue", nil, true},
		{"reverb", []string{"bogus"}, false},
		{"reverb", []string{"on", "acme", "x"}, false},
		{"queue", []string{"env"}, false},
	}
	for _, c := range cases {
		_, _, err := parseWorkerArgs(c.kind, c.args)
		if (err == nil) != c.ok {
			t.Errorf("parseWorkerArgs(%s, %v) err = %v, want ok=%v", c.kind, c.args, err, c.ok)
		}
	}
}
