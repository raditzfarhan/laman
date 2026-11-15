package daemon

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var testPorts = localPorts{MySQL: 3306, Postgres: 5432, Redis: 6379, SMTP: 1025, MailUI: 8025}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadEnvFileForms(t *testing.T) {
	dir := t.TempDir()
	if _, ok := readEnvFile(dir); ok {
		t.Fatal("readEnvFile on a folder without .env: ok = true")
	}
	writeFile(t, dir, ".env", strings.Join([]string{
		"export DB_HOST=127.0.0.1",
		"DB_DATABASE=acme # main db",
		`APP_NAME="Acme # Shop"`,
		"MAIL_HOST='127.0.0.1'",
		"# COMMENTED=1",
	}, "\r\n")+"\r\n")
	vals, ok := readEnvFile(dir)
	if !ok {
		t.Fatal("ok = false")
	}
	for k, want := range map[string]string{
		"DB_HOST": "127.0.0.1", "DB_DATABASE": "acme", "APP_NAME": "Acme # Shop", "MAIL_HOST": "127.0.0.1",
	} {
		if vals[k] != want {
			t.Errorf("%s = %q, want %q", k, vals[k], want)
		}
	}
	if _, set := vals["COMMENTED"]; set {
		t.Error("a commented-out line was read")
	}
}

func envMap(s string) map[string]string {
	m := map[string]string{}
	for _, f := range strings.Fields(s) {
		k, v, _ := strings.Cut(f, "=")
		m[k] = v
	}
	return m
}

func TestLaravelFacts(t *testing.T) {
	cases := []struct {
		name   string
		env    string
		sqlite bool
		db     *dbFact
		redis  *redisFact
		mail   *mailFact
		queue  *queueFact
	}{
		{name: "mysql defaults", env: "DB_CONNECTION=mysql DB_DATABASE=acme",
			db: &dbFact{Kind: "mariadb", Name: "acme", Host: "127.0.0.1", Port: 3306, Local: true}},
		{name: "mariadb localhost", env: "DB_CONNECTION=mariadb DB_HOST=localhost",
			db: &dbFact{Kind: "mariadb", Name: "laravel", Host: "localhost", Port: 3306, Local: true}},
		{name: "pgsql", env: "DB_CONNECTION=pgsql DB_DATABASE=shop",
			db: &dbFact{Kind: "postgres", Name: "shop", Host: "127.0.0.1", Port: 5432, Local: true}},
		{name: "sqlite default", env: "APP_KEY=x", sqlite: true,
			db: &dbFact{Kind: "sqlite", Name: "database/database.sqlite", Local: true}},
		{name: "remote db", env: "DB_CONNECTION=mysql DB_HOST=db.example.com",
			db: &dbFact{Kind: "mariadb", Name: "laravel", Host: "db.example.com", Port: 3306}},
		{name: "redis everywhere", env: "CACHE_STORE=redis SESSION_DRIVER=redis QUEUE_CONNECTION=redis",
			redis: &redisFact{Host: "127.0.0.1", Port: 6379, Local: true, Roles: []string{"cache", "session", "queue"}},
			queue: &queueFact{Driver: "redis"}},
		{name: "old cache key", env: "CACHE_DRIVER=redis",
			redis: &redisFact{Host: "127.0.0.1", Port: 6379, Local: true, Roles: []string{"cache"}}},
		{name: "no redis", env: "CACHE_STORE=file"},
		{name: "redis host only", env: "REDIS_HOST=127.0.0.1",
			redis: &redisFact{Host: "127.0.0.1", Port: 6379, Local: true}},
		{name: "mailpit", env: "MAIL_MAILER=smtp MAIL_HOST=127.0.0.1 MAIL_PORT=1025",
			mail: &mailFact{Mailer: "smtp", Host: "127.0.0.1", Port: 1025, Mailpit: true}},
		{name: "log mailer", env: "MAIL_MAILER=log", mail: &mailFact{Mailer: "log"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if c.sqlite {
				writeFile(t, dir, "database/database.sqlite", "")
			}
			f := laravelFacts(envMap(c.env), dir, testPorts)
			if f.Framework != "Laravel" {
				t.Errorf("Framework = %q", f.Framework)
			}
			if c.db != nil && !reflect.DeepEqual(f.DB, c.db) {
				t.Errorf("DB = %+v, want %+v", f.DB, c.db)
			}
			if !reflect.DeepEqual(f.Redis, c.redis) {
				t.Errorf("Redis = %+v, want %+v", f.Redis, c.redis)
			}
			if c.mail != nil && !reflect.DeepEqual(f.Mail, c.mail) {
				t.Errorf("Mail = %+v, want %+v", f.Mail, c.mail)
			}
			wantQ := c.queue
			if wantQ == nil {
				wantQ = &queueFact{Driver: "database"}
			}
			if !reflect.DeepEqual(f.Queue, wantQ) {
				t.Errorf("Queue = %+v, want %+v", f.Queue, wantQ)
			}
		})
	}
}

func TestFactsLocalButOtherPort(t *testing.T) {
	f := laravelFacts(envMap("DB_CONNECTION=mysql DB_PORT=3307 CACHE_STORE=redis REDIS_PORT=6380"), t.TempDir(), testPorts)
	if f.DB == nil || f.DB.Local || f.DB.Port != 3307 {
		t.Errorf("DB = %+v, want external on 3307", f.DB)
	}
	if f.Redis == nil || f.Redis.Local || f.Redis.Port != 6380 {
		t.Errorf("Redis = %+v, want external on 6380", f.Redis)
	}
}
