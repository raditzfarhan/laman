package main

import "testing"

func TestWorkerArgs(t *testing.T) {
	ok := []struct {
		kind         string
		args         []string
		action, site string
	}{
		{"reverb", []string{"env"}, "env", ""},
		{"reverb", []string{"on", "acme"}, "on", "acme"},
		{"queue", []string{"status"}, "status", ""},
		{"queue", []string{"log", "acme"}, "log", "acme"},
		{"queue", nil, "status", ""},
		{"queue", []string{}, "status", ""},
	}
	for _, c := range ok {
		a, s, err := parseWorkerArgs(c.kind, c.args)
		if err != nil || a != c.action || s != c.site {
			t.Errorf("parseWorkerArgs(%s, %v) = %q, %q, %v; want %q, %q", c.kind, c.args, a, s, err, c.action, c.site)
		}
	}
	bad := []struct {
		kind string
		args []string
	}{
		{"reverb", []string{"bogus"}},
		{"reverb", []string{"on", "acme", "x"}},
		{"queue", []string{"env"}},
	}
	for _, c := range bad {
		_, _, err := parseWorkerArgs(c.kind, c.args)
		if err == nil || err.Error() != workerUsage {
			t.Errorf("parseWorkerArgs(%s, %v) err = %v, want the usage text", c.kind, c.args, err)
		}
	}
}
