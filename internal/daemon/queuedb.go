package daemon

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"laman/internal/config"
)

// Laravel's database queue driver: jobs wait in a table of the site's own
// database. laman reads those tables (and failed_jobs, which Laravel fills
// whatever the driver) with its PHP, which already has the PDO drivers.

//go:embed queuedb.php
var queueDBScript []byte

// dbTarget is how to reach a site's queue tables (sent to queuedb.php).
type dbTarget struct {
	Site     string `json:"site"` // host, e.g. blog.test
	Conn     string `json:"conn"` // mysql, mariadb, pgsql, sqlite
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Database string `json:"database"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Table    string `json:"table"`  // jobs
	Failed   string `json:"failed"` // failed_jobs
	Jobs     bool   `json:"jobs"`   // read the jobs table (database driver), not only failures
	TZ       string `json:"tz"`     // APP_TIMEZONE: how Laravel wrote failed_at
}

type dbCounts struct {
	Site   string `json:"site"`
	Queues []struct {
		Name     string `json:"name"`
		Waiting  int64  `json:"waiting"`
		Delayed  int64  `json:"delayed"`
		Reserved int64  `json:"reserved"`
		Failed   int64  `json:"failed"`
	} `json:"queues"`
	Error string `json:"error,omitempty"`
}

type dbJob struct {
	ID       string   `json:"id"`
	UUID     string   `json:"uuid,omitempty"` // failed jobs in Laravel 8+ tables
	Name     string   `json:"name"`
	Attempts int      `json:"attempts"`
	Pushed   *float64 `json:"pushed,omitempty"`
	Due      *float64 `json:"due,omitempty"`
	Started  *float64 `json:"started,omitempty"`
	FailedAt *float64 `json:"failedAt,omitempty"`
	Error    string   `json:"error,omitempty"`
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

// dbTargetFor works out a site's queue database from its .env the way
// Laravel's config does. why says what is in the way when it can't.
func dbTargetFor(host, dir string, vals map[string]string, jobs bool) (t dbTarget, why string) {
	get := func(k, def string) string {
		if v, ok := vals[k]; ok && v != "" {
			return v
		}
		return def
	}
	t = dbTarget{Site: host, Jobs: jobs, Table: get("DB_QUEUE_TABLE", "jobs"), Failed: "failed_jobs", TZ: get("APP_TIMEZONE", "UTC")}
	if !identifier.MatchString(t.Table) {
		return t, fmt.Sprintf("can't read a queue table named %q", t.Table)
	}
	t.Conn = strings.ToLower(get("DB_QUEUE_CONNECTION", get("DB_CONNECTION", "")))
	if t.Conn == "" {
		// Laravel 11+ falls back to SQLite in database/database.sqlite.
		if _, err := os.Stat(filepath.Join(dir, "database", "database.sqlite")); err != nil {
			return t, "no DB_CONNECTION in .env"
		}
		t.Conn = "sqlite"
	}
	switch t.Conn {
	case "sqlite":
		p := get("DB_DATABASE", filepath.Join(dir, "database", "database.sqlite"))
		if !filepath.IsAbs(p) {
			// Relative paths resolve from where PHP runs: try the project,
			// then its public folder.
			if _, err := os.Stat(filepath.Join(dir, p)); err == nil {
				p = filepath.Join(dir, p)
			} else {
				p = filepath.Join(dir, "public", p)
			}
		}
		if _, err := os.Stat(p); err != nil {
			return t, "its SQLite file doesn't exist yet"
		}
		t.Database = p
		return t, ""
	case "mysql", "mariadb", "pgsql":
		def := "3306"
		if t.Conn == "pgsql" {
			def = "5432"
		}
		t.Host = get("DB_HOST", "127.0.0.1")
		switch strings.ToLower(t.Host) {
		case "127.0.0.1", "localhost", "::1":
			t.Host = "127.0.0.1"
		default:
			return t, fmt.Sprintf("its database is on %s, not this computer", t.Host)
		}
		port, err := strconv.Atoi(get("DB_PORT", def))
		if err != nil {
			return t, "DB_PORT is not a number"
		}
		t.Port = port
		t.Database = get("DB_DATABASE", "laravel")
		t.Username = get("DB_USERNAME", "root")
		t.Password = get("DB_PASSWORD", "")
		return t, ""
	}
	return t, fmt.Sprintf("laman can't read %s databases", t.Conn)
}

// queueDB runs queuedb.php with the default PHP.
func (d *daemon) queueDB(in any) ([]byte, error) {
	v := d.cfg.DefaultPHP()
	if v == "" {
		return nil, errors.New("no PHP installed")
	}
	script := filepath.Join(config.EtcDir(), "queuedb.php")
	if old, err := os.ReadFile(script); err != nil || !bytes.Equal(old, queueDBScript) {
		if err := os.WriteFile(script, queueDBScript, 0o644); err != nil {
			return nil, err
		}
	}
	body, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(config.BinDir("php"), v, "php.exe"), script)
	noWindow(cmd)
	cmd.Stdin = bytes.NewReader(body)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String() + " " + out.String())
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, fmt.Errorf("reading database queues: %v %s", err, msg)
	}
	return out.Bytes(), nil
}

// serverUp reports quickly whether something listens on a TCP address, so
// a stopped MariaDB costs one short dial instead of a PHP timeout per site.
func serverUp(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// dbCountsCache keeps the last read for a few seconds: the page polls every
// few seconds and several tabs may be open.
type dbCountsCache struct {
	mu   sync.Mutex
	at   time.Time
	key  string
	data map[string]dbCounts
	err  error
}

func (d *daemon) dbQueueCounts(targets []dbTarget) (map[string]dbCounts, error) {
	if len(targets) == 0 {
		return map[string]dbCounts{}, nil
	}
	key, _ := json.Marshal(targets)
	c := &d.dbCounts
	c.mu.Lock()
	defer c.mu.Unlock()
	if string(key) == c.key && time.Since(c.at) < 4*time.Second {
		return c.data, c.err
	}
	out := map[string]dbCounts{}
	var send []dbTarget
	up := map[string]bool{}
	for _, t := range targets {
		if t.Conn != "sqlite" {
			addr := net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
			ok, seen := up[addr]
			if !seen {
				ok = serverUp(addr)
				up[addr] = ok
			}
			if !ok {
				name := "MariaDB"
				if t.Conn == "pgsql" {
					name = "PostgreSQL"
				}
				out[t.Site] = dbCounts{Site: t.Site, Error: fmt.Sprintf("nothing answers on port %d (is %s running?)", t.Port, name)}
				continue
			}
		}
		send = append(send, t)
	}
	var err error
	if len(send) > 0 {
		var raw []byte
		raw, err = d.queueDB(map[string]any{"action": "counts", "sites": send})
		if err == nil {
			var list []dbCounts
			if err = json.Unmarshal(raw, &list); err == nil {
				for _, r := range list {
					out[r.Site] = r
				}
			} else {
				err = fmt.Errorf("reading database queues: %v", err)
			}
		}
	}
	c.at, c.key, c.data, c.err = time.Now(), string(key), out, err
	return out, err
}

// dbJobs lists one part of a site's database queue: waiting, delayed,
// reserved or failed.
func (d *daemon) dbJobs(t dbTarget, queue, part string, limit int) ([]dbJob, error) {
	raw, err := d.queueDB(map[string]any{"action": "jobs", "site": t, "queue": queue, "part": part, "limit": limit})
	if err != nil {
		return nil, err
	}
	var r struct {
		Jobs  []dbJob `json:"jobs"`
		Error string  `json:"error"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("reading database queues: %v", err)
	}
	if r.Error != "" {
		return nil, errors.New(r.Error)
	}
	if r.Jobs == nil {
		r.Jobs = []dbJob{}
	}
	return r.Jobs, nil
}

// forgetFailed deletes failed jobs of one queue: the given ids, or all of
// them when ids is nil.
func (d *daemon) forgetFailed(t dbTarget, queue string, ids []string) (int64, error) {
	in := map[string]any{"action": "forget", "site": t, "queue": queue}
	if ids != nil {
		in["ids"] = ids
	}
	raw, err := d.queueDB(in)
	if err != nil {
		return 0, err
	}
	var r struct {
		Removed int64  `json:"removed"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return 0, fmt.Errorf("deleting failed jobs: %v", err)
	}
	if r.Error != "" {
		return 0, errors.New(r.Error)
	}
	d.dbCounts.forget()
	return r.Removed, nil
}

// retryFailed puts failed jobs back on their queue with the project's own
// `php artisan queue:retry`, which knows the site's queue connection
// (Redis or database) and its failed-job store. ids are uuids (or ids on
// old tables); none means every failed job of the queue.
func (d *daemon) retryFailed(dir, php, queue string, ids []string) (string, error) {
	if _, err := os.Stat(filepath.Join(dir, "artisan")); err != nil {
		return "", errors.New("this project has no artisan file")
	}
	if php == "" {
		php = d.cfg.DefaultPHP()
	}
	args := []string{"artisan", "queue:retry", "--no-interaction", "--no-ansi"}
	if ids == nil {
		args = append(args, "--queue="+queue)
	} else {
		for _, id := range ids {
			if !jobID.MatchString(id) {
				return "", fmt.Errorf("not a job id: %q", id)
			}
		}
		args = append(args, ids...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(config.BinDir("php"), php, "php.exe"), args...)
	cmd.Dir = dir
	noWindow(cmd)
	out, err := cmd.CombinedOutput()
	d.dbCounts.forget()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if len(text) > 300 {
			text = text[len(text)-300:]
		}
		return "", fmt.Errorf("php artisan queue:retry failed: %s", firstNonEmpty(text, err.Error()))
	}
	return text, nil
}

// Failed job ids: numbers, or uuids.
var jobID = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z-]{0,63}$`)

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (c *dbCountsCache) forget() {
	c.mu.Lock()
	c.at = time.Time{}
	c.mu.Unlock()
}
