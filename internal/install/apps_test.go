package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPHPFits(t *testing.T) {
	for _, c := range []struct {
		v, rng string
		want   bool
	}{
		{"8.5.12-1", ">=8.4", true},
		{"8.3.36-1", ">=8.4", false},
		{"8.3.36-1", ">=7.2,<8.4", true},
		{"8.4.27-1", ">=7.2,<8.4", false},
		{"8.4", "", true},
	} {
		if got := PHPFits(c.v, c.rng); got != c.want {
			t.Errorf("PHPFits(%q, %q) = %v", c.v, c.rng, got)
		}
	}
}

func TestServerEnv(t *testing.T) {
	t.Setenv("LAMAN_HOME", t.TempDir())
	if ServerEnv("dbzuper") != nil {
		t.Fatal("env before any was written")
	}
	os.MkdirAll(filepath.Dir(ServerEnvPath("dbzuper")), 0o755)
	if err := setServerEnv("dbzuper", map[string]string{"DBZUPER_AUTO_LOGIN_KEY": "abc123"}); err != nil {
		t.Fatal(err)
	}
	if got := ServerEnv("dbzuper"); len(got) != 1 || got["DBZUPER_AUTO_LOGIN_KEY"] != "abc123" {
		t.Fatalf("ServerEnv = %v", got)
	}
	// Hand edits that could break out of the Caddyfile are dropped.
	os.WriteFile(ServerEnvPath("dbzuper"), []byte("# c\r\nOK=1\r\nlower=x\r\nBAD=a\"b\r\nSPACE=a b\r\nBRACE=}\r\nEMPTY=\r\n"), 0o600)
	if got := ServerEnv("dbzuper"); len(got) != 1 || got["OK"] != "1" {
		t.Fatalf("ServerEnv kept unsafe lines: %v", got)
	}
	if err := RemoveApp("dbzuper"); err != nil {
		t.Fatal(err)
	}
	if ServerEnv("dbzuper") != nil {
		t.Error("RemoveApp kept the server env")
	}
}

func TestSetEnv(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(p, []byte("APP_URL=http://localhost:8000\r\n# DBZUPER_PASSWORD_POLICY=strong\r\nAPP_DEBUG=true\r\n"), 0o644)
	if err := setEnv(p, map[string]string{"APP_URL": "https://dbzuper.test", "DBZUPER_PASSWORD_POLICY": "basic", "NEW_ONE": "x"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	s := string(b)
	for _, want := range []string{"APP_URL=https://dbzuper.test\n", "DBZUPER_PASSWORD_POLICY=basic\n", "APP_DEBUG=true\n", "NEW_ONE=x\n"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Count(s, "APP_URL=") != 1 {
		t.Errorf("APP_URL duplicated:\n%s", s)
	}
}

func TestPHPRedisAdminConfig(t *testing.T) {
	sample := "<?php\n$config = array(\n  'servers' => array(\n    array(\n      'name'   => 'local server',\n      'port'   => 6379,\n    ),\n    /*array(\n      'port'   => 6379,\n    ),*/\n  ),\n  'allow_no_auth' => false,\n);\n"
	out, err := phpRedisAdminConfig(sample, 6390)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"'laman Redis'", "'port'   => 6390,", "'allow_no_auth' => true,"} {
		if !strings.Contains(out, want) {
			t.Errorf("config lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "6379") != 1 {
		t.Errorf("only the first server should change:\n%s", out)
	}
	if _, err := phpRedisAdminConfig("<?php $config = array();", 6390); err == nil {
		t.Error("a changed sample should be refused")
	}
}
