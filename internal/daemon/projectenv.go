package daemon

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// What a project is wired to, read from its own files (.env, wp-config.php)
// for the site detail page. Nothing here writes to the project.

// localPorts are laman's own service ports, to tell laman's servers from
// other ones on this computer.
type localPorts struct{ MySQL, Postgres, Redis, SMTP, MailUI int }

type dbFact struct {
	Kind  string `json:"kind"` // mariadb, postgres, sqlite, or the driver's own name
	Name  string `json:"name"` // database name; for sqlite the file, relative to the project
	Host  string `json:"host,omitempty"`
	Port  int    `json:"port,omitempty"`
	Local bool   `json:"local"` // laman's own server (or a sqlite file)
}

type redisFact struct {
	Host  string   `json:"host"`
	Port  int      `json:"port"`
	Local bool     `json:"local"`
	Roles []string `json:"roles,omitempty"` // cache, session, queue
}

type mailFact struct {
	Mailer  string `json:"mailer"`
	Host    string `json:"host,omitempty"`
	Port    int    `json:"port,omitempty"`
	Mailpit bool   `json:"mailpit"`
}

type queueFact struct {
	Driver string `json:"driver"`
}

type projectFacts struct {
	Framework string     `json:"framework,omitempty"`
	DB        *dbFact    `json:"db,omitempty"`
	Redis     *redisFact `json:"redis,omitempty"`
	Mail      *mailFact  `json:"mail,omitempty"`
	Queue     *queueFact `json:"queue,omitempty"`
}

var envAssign = regexp.MustCompile(`^(?:export[ \t]+)?([A-Za-z_][A-Za-z0-9_]*)[ \t]*=[ \t]*(.*?)[ \t]*$`)

// readEnvFile reads dir/.env as dotenv does: `export ` prefixes, quoted
// values, and ` #` comments after unquoted values. ok is false when there
// is no .env.
func readEnvFile(dir string) (map[string]string, bool) {
	b, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		return nil, false
	}
	vals := map[string]string{}
	for _, line := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		m := envAssign.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		v := m[2]
		if len(v) >= 1 && (v[0] == '"' || v[0] == '\'') {
			if end := closingQuote(v); end > 0 {
				v = v[1:end]
			}
		} else if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		} else if strings.HasPrefix(v, "#") {
			v = ""
		}
		vals[m[1]] = v
	}
	return vals, true
}

// closingQuote is the index of the quote that ends v (which starts with
// one), skipping \" inside double quotes; -1 when there is none.
func closingQuote(v string) int {
	for i := 1; i < len(v); i++ {
		if v[0] == '"' && v[i] == '\\' {
			i++
			continue
		}
		if v[i] == v[0] {
			return i
		}
	}
	return -1
}

func isLoopback(host string) bool {
	switch strings.ToLower(strings.Trim(host, "[]")) {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

// laravelFacts maps a Laravel .env to what the project uses, with
// Laravel's defaults for missing keys.
func laravelFacts(vals map[string]string, dir string, p localPorts) projectFacts {
	get := func(k, def string) string {
		if v, ok := vals[k]; ok && v != "" {
			return v
		}
		return def
	}
	f := projectFacts{Framework: "Laravel"}

	switch conn := strings.ToLower(get("DB_CONNECTION", "")); conn {
	case "", "sqlite":
		name := get("DB_DATABASE", "database/database.sqlite")
		if conn == "" {
			name = "database/database.sqlite"
		}
		if exists(filepath.Join(dir, filepath.FromSlash(name))) || (filepath.IsAbs(name) && exists(name)) {
			f.DB = &dbFact{Kind: "sqlite", Name: filepath.ToSlash(name), Local: true}
		}
	case "mysql", "mariadb", "pgsql":
		kind, def, own := "mariadb", "3306", p.MySQL
		if conn == "pgsql" {
			kind, def, own = "postgres", "5432", p.Postgres
		}
		host := get("DB_HOST", "127.0.0.1")
		port, _ := strconv.Atoi(get("DB_PORT", def))
		f.DB = &dbFact{Kind: kind, Name: get("DB_DATABASE", "laravel"), Host: host, Port: port, Local: isLoopback(host) && port == own}
	default:
		f.DB = &dbFact{Kind: conn, Name: get("DB_DATABASE", "")}
	}

	var roles []string
	if get("CACHE_STORE", get("CACHE_DRIVER", "")) == "redis" {
		roles = append(roles, "cache")
	}
	if get("SESSION_DRIVER", "") == "redis" {
		roles = append(roles, "session")
	}
	queue := get("QUEUE_CONNECTION", "database")
	if queue == "redis" {
		roles = append(roles, "queue")
	}
	// Only when something uses it: Laravel's stock .env always has REDIS_HOST.
	if len(roles) > 0 {
		host := get("REDIS_HOST", "127.0.0.1")
		port, _ := strconv.Atoi(get("REDIS_PORT", "6379"))
		f.Redis = &redisFact{Host: host, Port: port, Local: isLoopback(host) && port == p.Redis, Roles: roles}
	}

	if mailer := get("MAIL_MAILER", get("MAIL_DRIVER", "")); mailer != "" {
		m := &mailFact{Mailer: mailer}
		if mailer == "smtp" {
			m.Host = get("MAIL_HOST", "127.0.0.1")
			m.Port, _ = strconv.Atoi(get("MAIL_PORT", "2525"))
			m.Mailpit = isLoopback(m.Host) && m.Port == p.SMTP
		}
		f.Mail = m
	}
	f.Queue = &queueFact{Driver: queue}
	return f
}

var wpDefine = regexp.MustCompile(`define\(\s*['"](DB_NAME|DB_HOST)['"]\s*,\s*['"]([^'"]*)['"]\s*\)`)

// readProjectFacts works out what the project in dir uses: Laravel from its
// .env, WordPress from wp-config.php, anything else from DATABASE_URL and
// REDIS_URL. A project with none of these gives zero facts.
func readProjectFacts(dir string, p localPorts) projectFacts {
	vals, haveEnv := readEnvFile(dir)
	if _, key := vals["APP_KEY"]; key || exists(filepath.Join(dir, "artisan")) {
		f := laravelFacts(vals, dir, p)
		if major := laravelMajor(dir); major != "" {
			f.Framework = "Laravel " + major
		}
		return f
	}
	if b, err := os.ReadFile(filepath.Join(dir, "wp-config.php")); err == nil {
		db := &dbFact{Kind: "mariadb", Host: "localhost", Port: 3306}
		for _, m := range wpDefine.FindAllStringSubmatch(string(b), -1) {
			if m[1] == "DB_NAME" {
				db.Name = m[2]
			} else {
				db.Host = m[2]
				if h, port, ok := strings.Cut(m[2], ":"); ok {
					db.Host = h
					if n, err := strconv.Atoi(port); err == nil {
						db.Port = n
					}
				}
			}
		}
		db.Local = isLoopback(db.Host) && db.Port == p.MySQL
		return projectFacts{Framework: "WordPress", DB: db}
	}
	if !haveEnv {
		return projectFacts{}
	}
	var f projectFacts
	if raw := vals["DATABASE_URL"]; raw != "" {
		f.DB = dbFromURL(raw, p)
	}
	if raw := vals["REDIS_URL"]; raw != "" {
		if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
			port, _ := strconv.Atoi(u.Port())
			if port == 0 {
				port = 6379
			}
			f.Redis = &redisFact{Host: u.Hostname(), Port: port, Local: isLoopback(u.Hostname()) && port == p.Redis}
		}
	}
	return f
}

func dbFromURL(raw string, p localPorts) *dbFact {
	scheme, rest, ok := strings.Cut(raw, ":")
	if !ok {
		return nil
	}
	switch strings.ToLower(scheme) {
	case "file", "sqlite":
		return &dbFact{Kind: "sqlite", Name: strings.TrimPrefix(rest, "//"), Local: true}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	kind, own, def := strings.ToLower(u.Scheme), 0, 0
	switch kind {
	case "mysql", "mariadb":
		kind, own, def = "mariadb", p.MySQL, 3306
	case "postgres", "postgresql":
		kind, own, def = "postgres", p.Postgres, 5432
	}
	port, _ := strconv.Atoi(u.Port())
	if port == 0 {
		port = def
	}
	return &dbFact{Kind: kind, Name: strings.TrimPrefix(u.Path, "/"), Host: u.Hostname(), Port: port,
		Local: own != 0 && isLoopback(u.Hostname()) && port == own}
}

// laravelMajor is laravel/framework's major version from composer.lock
// ("12" for v12.3.0), or "".
func laravelMajor(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "composer.lock"))
	if err != nil {
		return ""
	}
	var lock struct {
		Packages []struct{ Name, Version string } `json:"packages"`
	}
	if json.Unmarshal(b, &lock) != nil {
		return ""
	}
	for _, pkg := range lock.Packages {
		if pkg.Name == "laravel/framework" {
			major, _, _ := strings.Cut(strings.TrimPrefix(pkg.Version, "v"), ".")
			if _, err := strconv.Atoi(major); err == nil {
				return major
			}
		}
	}
	return ""
}
