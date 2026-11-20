package daemon

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPlanQueue(t *testing.T) {
	horizon := t.TempDir()
	os.WriteFile(filepath.Join(horizon, "composer.json"), []byte(`{"require":{"laravel/horizon":"^5"}}`), 0o644)
	plain := t.TempDir()
	listen := []string{"queue:listen", "--tries=1", "--timeout=0"}

	tests := []struct {
		name    string
		dir     string
		vals    map[string]string
		queues  []string
		inline  bool
		note    string
		needs   string
		args    []string
		argsEnd string
	}{
		{name: "sync", dir: plain, vals: map[string]string{"QUEUE_CONNECTION": "sync"}, inline: true, note: "Jobs run inline (QUEUE_CONNECTION=sync)"},
		{name: "null", dir: plain, vals: map[string]string{"QUEUE_CONNECTION": "null"}, inline: true, note: "Jobs run inline (QUEUE_CONNECTION=null)"},
		{name: "redis local", dir: plain, vals: map[string]string{"QUEUE_CONNECTION": "redis", "REDIS_HOST": "127.0.0.1"}, needs: "redis", args: listen},
		{name: "redis unset host", dir: plain, vals: map[string]string{"QUEUE_CONNECTION": "redis"}, needs: "redis", args: listen},
		{name: "redis remote", dir: plain, vals: map[string]string{"QUEUE_CONNECTION": "redis", "REDIS_HOST": "redis.example.com"}, needs: "", args: listen},
		{name: "db mysql", dir: plain, vals: map[string]string{"QUEUE_CONNECTION": "database", "DB_CONNECTION": "mysql"}, needs: "mariadb", args: listen},
		{name: "db mariadb", dir: plain, vals: map[string]string{"QUEUE_CONNECTION": "database", "DB_CONNECTION": "mariadb"}, needs: "mariadb", args: listen},
		{name: "db pgsql", dir: plain, vals: map[string]string{"QUEUE_CONNECTION": "database", "DB_CONNECTION": "pgsql"}, needs: "postgres", args: listen},
		{name: "db sqlite", dir: plain, vals: map[string]string{"QUEUE_CONNECTION": "database", "DB_CONNECTION": "sqlite"}, needs: "", args: listen},
		{name: "unset is database", dir: plain, vals: map[string]string{"DB_CONNECTION": "mysql"}, needs: "mariadb", args: listen},
		{name: "horizon", dir: horizon, vals: map[string]string{"QUEUE_CONNECTION": "redis"}, needs: "redis", args: listen, note: "Horizon doesn't run on Windows — using queue:listen"},
		{name: "horizon queues", dir: horizon, vals: map[string]string{"QUEUE_CONNECTION": "redis"}, queues: []string{"high", "default"}, needs: "redis", note: "Horizon doesn't run on Windows — using queue:listen", argsEnd: "--queue=high,default"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := planQueue(tc.dir, tc.vals, tc.queues)
			if p.Inline != tc.inline || p.Note != tc.note || p.Needs != tc.needs {
				t.Fatalf("got inline=%v note=%q needs=%q", p.Inline, p.Note, p.Needs)
			}
			if tc.args != nil && !reflect.DeepEqual(p.Args, tc.args) {
				t.Fatalf("args = %v, want %v", p.Args, tc.args)
			}
			if tc.argsEnd != "" && (len(p.Args) == 0 || p.Args[len(p.Args)-1] != tc.argsEnd) {
				t.Fatalf("args = %v, want to end with %s", p.Args, tc.argsEnd)
			}
			if tc.inline && p.Args != nil {
				t.Fatalf("inline plan has args %v", p.Args)
			}
		})
	}
}

func TestComposerRequires(t *testing.T) {
	mk := func(json string) string {
		d := t.TempDir()
		if json != "" {
			os.WriteFile(filepath.Join(d, "composer.json"), []byte(json), 0o644)
		}
		return d
	}
	if !composerRequires(mk(`{"require":{"laravel/reverb":"^1"}}`), "laravel/reverb") {
		t.Error("require not found")
	}
	if !composerRequires(mk(`{"require-dev":{"laravel/reverb":"^1"}}`), "laravel/reverb") {
		t.Error("require-dev not found")
	}
	v := mk("")
	os.MkdirAll(filepath.Join(v, "vendor", "laravel", "reverb"), 0o755)
	if !composerRequires(v, "laravel/reverb") {
		t.Error("vendor not found")
	}
	if composerRequires(mk(`{"require":{"php":"^8"}}`), "laravel/reverb") {
		t.Error("false positive")
	}
	if composerRequires(mk(""), "laravel/reverb") {
		t.Error("false positive on empty dir")
	}
}

func TestReverbArgs(t *testing.T) {
	want := []string{"reverb:start", "--host=127.0.0.1", "--port=8101", "--hostname=127.0.0.1"}
	if got := reverbArgs(8101); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestUsesHorizon(t *testing.T) {
	mk := func(json string, vendor bool) string {
		d := t.TempDir()
		if json != "" {
			os.WriteFile(filepath.Join(d, "composer.json"), []byte(json), 0o644)
		}
		if vendor {
			os.MkdirAll(filepath.Join(d, "vendor", "laravel", "horizon"), 0o755)
		}
		return d
	}
	if !usesHorizon(mk(`{"require":{"laravel/horizon":"^5"}}`, false)) {
		t.Error("require not found")
	}
	// Only a dev dependency (installed under vendor too): the app does not
	// run Horizon itself.
	if usesHorizon(mk(`{"require-dev":{"laravel/horizon":"^5"}}`, true)) {
		t.Error("require-dev counted")
	}
	if !usesHorizon(mk("", true)) {
		t.Error("vendor fallback without composer.json not found")
	}
	if usesHorizon(mk(`{"require":{"php":"^8"}}`, false)) {
		t.Error("false positive")
	}
	dev := t.TempDir()
	os.WriteFile(filepath.Join(dev, "composer.json"), []byte(`{"require-dev":{"laravel/horizon":"^5"}}`), 0o644)
	os.MkdirAll(filepath.Join(dev, "vendor", "laravel", "horizon"), 0o755)
	if p := planQueue(dev, map[string]string{"QUEUE_CONNECTION": "redis"}, nil); p.Note != "" {
		t.Errorf("planQueue note for a dev-only Horizon = %q", p.Note)
	}
}
