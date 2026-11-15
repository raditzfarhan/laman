package daemon

import (
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
			if end := strings.IndexByte(v[1:], v[0]); end >= 0 {
				v = v[1 : end+1]
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
	if _, set := vals["REDIS_HOST"]; set || len(roles) > 0 {
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
