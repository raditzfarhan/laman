package daemon

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"laman/internal/redisq"
	"laman/internal/sites"
)

// The Queues view: Laravel queues kept in laman's Redis, matched to the
// sites they belong to through each site's .env.

type queueRow struct {
	redisq.Queue
	Site   string `json:"site,omitempty"`   // host of the site the queue belongs to
	Driver string `json:"driver"`           // redis or database
	Conn   string `json:"conn,omitempty"`   // database: mysql, pgsql, sqlite...
	Failed int64  `json:"failed,omitempty"` // in the site's failed_jobs table
}

type otherDriver struct {
	Site   string `json:"site"`
	Driver string `json:"driver"` // QUEUE_CONNECTION, e.g. "sync"
}

type uncheckedSite struct {
	Site   string `json:"site"`
	Why    string `json:"why"`              // the same for sites with the same problem
	Detail string `json:"detail,omitempty"` // this site's database, e.g. "mysql shop on 127.0.0.1:3306"
}

type queuesReply struct {
	Redis     bool            `json:"redis"`  // laman's Redis is running
	Queues    []queueRow      `json:"queues"` // queues with something waiting, running or failed
	Idle      []string        `json:"idle,omitempty"`
	Unchecked []uncheckedSite `json:"unchecked,omitempty"`
	Other     []otherDriver   `json:"other,omitempty"` // Laravel sites queueing elsewhere (sync...)
	Error     string          `json:"error,omitempty"`
}

// laravelEnv is what a site's .env says about its queue.
type laravelEnv struct {
	prefix string // Redis key prefix
	db     int    // REDIS_DB
	driver string // QUEUE_CONNECTION ("" when unset)
	vals   map[string]string
}

var envLine = regexp.MustCompile(`(?m)^[ \t]*([A-Z_][A-Z0-9_]*)[ \t]*=[ \t]*(.*?)[ \t]*$`)

// readLaravelEnv reads a site's .env; ok is false when it has none.
func readLaravelEnv(dir string) (laravelEnv, bool) {
	b, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		return laravelEnv{}, false
	}
	vals := map[string]string{}
	for _, m := range envLine.FindAllStringSubmatch(strings.ReplaceAll(string(b), "\r\n", "\n"), -1) {
		v := m[2]
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		vals[m[1]] = v
	}
	if _, laravel := vals["APP_KEY"]; !laravel {
		return laravelEnv{}, false
	}
	e := laravelEnv{driver: vals["QUEUE_CONNECTION"], vals: vals}
	if p, set := vals["REDIS_PREFIX"]; set {
		e.prefix = p
	} else {
		name := vals["APP_NAME"]
		if name == "" {
			name = "laravel"
		}
		e.prefix = laravelSlug(name) + "_database_"
	}
	e.db, _ = strconv.Atoi(vals["REDIS_DB"])
	return e, true
}

var (
	slugDrop = regexp.MustCompile(`[^a-z0-9_\s]+`)
	slugRuns = regexp.MustCompile(`[_\s]+`)
)

// laravelSlug matches Laravel's Str::slug($s, '_') for ASCII names.
func laravelSlug(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, "@", "_at_")
	s = slugDrop.ReplaceAllString(s, "")
	s = slugRuns.ReplaceAllString(s, "_")
	return strings.Trim(s, "_")
}

func (d *daemon) redisAddr() (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.handles["redis"] == nil {
		return "", false
	}
	return fmt.Sprintf("127.0.0.1:%d", d.cfg.RedisPort), true
}

func (d *daemon) queues() queuesReply {
	addr, running := d.redisAddr()
	reply := queuesReply{Redis: running, Queues: []queueRow{}}

	d.mu.Lock()
	sitesCopy := d.sites
	d.mu.Unlock()

	byPrefix := map[string]string{}
	dbs := map[int]bool{0: true}
	var redisHosts, dbHosts []string
	var targets []dbTarget
	conn := map[string]string{}
	targetOf := map[string]dbTarget{}
	for _, s := range sitesCopy {
		if s.App != "" {
			continue
		}
		e, ok := readLaravelEnv(s.Path)
		if !ok {
			continue
		}
		driver := e.driver
		if driver == "" {
			driver = "database" // Laravel 11's default
		}
		switch driver {
		case "redis":
			byPrefix[e.prefix] = s.Host
			dbs[e.db] = true
			redisHosts = append(redisHosts, s.Host)
			// Failures still land in the database's failed_jobs.
			if t, why := dbTargetFor(s.Host, s.Path, e.vals, false); why == "" {
				targets = append(targets, t)
			}
		case "database":
			t, why := dbTargetFor(s.Host, s.Path, e.vals, true)
			if why != "" {
				reply.Unchecked = append(reply.Unchecked, uncheckedSite{Site: s.Host, Why: why})
				continue
			}
			targets = append(targets, t)
			targetOf[s.Host] = t
			dbHosts = append(dbHosts, s.Host)
			conn[s.Host] = t.Conn
		default:
			reply.Other = append(reply.Other, otherDriver{Site: s.Host, Driver: driver})
		}
	}

	counts, err := d.dbQueueCounts(targets)
	if err != nil {
		reply.Error = err.Error()
	}
	active := map[string]bool{}
	for _, host := range dbHosts {
		c, ok := counts[host]
		if !ok {
			continue // PHP did not run; reply.Error says why
		}
		if c.Error != "" {
			reply.Unchecked = append(reply.Unchecked, uncheckedSite{Site: host, Why: c.Error, Detail: describeDB(targetOf[host])})
			continue
		}
		for _, q := range c.Queues {
			if q.Waiting+q.Delayed+q.Reserved+q.Failed == 0 {
				continue
			}
			reply.Queues = append(reply.Queues, queueRow{
				Queue: redisq.Queue{Name: q.Name, Waiting: q.Waiting, Delayed: q.Delayed, Reserved: q.Reserved},
				Site:  host, Driver: "database", Conn: conn[host], Failed: q.Failed,
			})
			active[host] = true
		}
		if !active[host] {
			reply.Idle = append(reply.Idle, host)
		}
	}

	// Redis queues, with each one's failures from its site's database.
	usedFailed := map[string]bool{} // host + "|" + queue
	failedOf := func(host, name string) int64 {
		for _, q := range counts[host].Queues {
			if q.Name == name {
				usedFailed[host+"|"+name] = true
				return q.Failed
			}
		}
		return 0
	}
	if running {
		var dbList []int
		for db := range dbs {
			dbList = append(dbList, db)
		}
		sort.Ints(dbList)
		qs, err := redisq.Queues(addr, dbList)
		if err != nil {
			reply.Error = strings.TrimSpace(reply.Error + " " + err.Error())
		}
		for _, q := range qs {
			host := byPrefix[q.Prefix]
			row := queueRow{Queue: q, Site: host, Driver: "redis"}
			if host != "" {
				row.Failed = failedOf(host, q.Name)
			}
			if q.Waiting+q.Delayed+q.Reserved+row.Failed == 0 {
				continue
			}
			reply.Queues = append(reply.Queues, row)
			if host != "" {
				active[host] = true
			}
		}
	}
	for _, host := range redisHosts {
		for _, q := range counts[host].Queues {
			if q.Failed > 0 && !usedFailed[host+"|"+q.Name] {
				reply.Queues = append(reply.Queues, queueRow{Queue: redisq.Queue{Name: q.Name}, Site: host, Driver: "redis", Failed: q.Failed})
				active[host] = true
			}
		}
		switch {
		case active[host]:
		case running:
			reply.Idle = append(reply.Idle, host)
		default:
			reply.Unchecked = append(reply.Unchecked, uncheckedSite{Site: host, Why: "it uses Redis, and laman's Redis is off"})
		}
	}

	// Failures first, then the busiest.
	sort.SliceStable(reply.Queues, func(i, j int) bool {
		a, b := reply.Queues[i], reply.Queues[j]
		if (a.Failed > 0) != (b.Failed > 0) {
			return a.Failed > 0
		}
		if ta, tb := a.Waiting+a.Delayed+a.Reserved+a.Failed, b.Waiting+b.Delayed+b.Reserved+b.Failed; ta != tb {
			return ta > tb
		}
		return a.Site+a.Name < b.Site+b.Name
	})
	sort.Strings(reply.Idle)
	sort.Slice(reply.Unchecked, func(i, j int) bool { return reply.Unchecked[i].Site < reply.Unchecked[j].Site })
	return reply
}

func (d *daemon) siteByHost(host string) (sites.Site, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, s := range d.sites {
		if s.Host == host && s.App == "" {
			return s, true
		}
	}
	return sites.Site{}, false
}

// siteQueueTarget finds a site's queue database for the job lists.
func (d *daemon) siteQueueTarget(host string) (dbTarget, string, error) {
	d.mu.Lock()
	sitesCopy := d.sites
	d.mu.Unlock()
	for _, s := range sitesCopy {
		if s.Host != host || s.App != "" {
			continue
		}
		e, ok := readLaravelEnv(s.Path)
		if !ok {
			return dbTarget{}, "", fmt.Errorf("%s has no Laravel .env", host)
		}
		driver := e.driver
		if driver == "" {
			driver = "database"
		}
		t, why := dbTargetFor(s.Host, s.Path, e.vals, driver == "database")
		if why != "" {
			return t, driver, errors.New(why)
		}
		return t, driver, nil
	}
	return dbTarget{}, "", fmt.Errorf("no site %s", host)
}

func (d *daemon) queueRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/queues", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, d.queues())
	})
	mux.HandleFunc("GET /api/queues/jobs", func(w http.ResponseWriter, r *http.Request) {
		addr, ok := d.redisAddr()
		if !ok {
			fail(w, http.StatusConflict, "Redis is not running")
			return
		}
		db, _ := strconv.Atoi(r.URL.Query().Get("db"))
		jobs, err := redisq.Jobs(addr, db, r.URL.Query().Get("key"), r.URL.Query().Get("part"), 20)
		if err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		if jobs == nil {
			jobs = []redisq.Job{}
		}
		writeJSON(w, jobs)
	})
	// Jobs of a database queue, or the failed jobs of any queue.
	mux.HandleFunc("GET /api/queues/dbjobs", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		part := q.Get("part")
		t, driver, err := d.siteQueueTarget(q.Get("site"))
		if err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		if part != "failed" && driver != "database" {
			fail(w, http.StatusBadRequest, "%s queues with %s", t.Site, driver)
			return
		}
		jobs, err := d.dbJobs(t, q.Get("queue"), part, 20)
		if err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		writeJSON(w, jobs)
	})
	// Failed jobs: retry (back on their queue) or forget (delete). IDs
	// picks jobs; All takes every failed job of the queue.
	mux.HandleFunc("POST /api/queues/failed", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Site, Queue, Action string
			IDs                 []string
			All                 bool
		}
		if err := readJSON(r, &req); err != nil || req.Queue == "" || (len(req.IDs) == 0 && !req.All) {
			fail(w, http.StatusBadRequest, "site, queue and the jobs to act on are required")
			return
		}
		ids := req.IDs
		if req.All {
			ids = nil
		}
		t, _, err := d.siteQueueTarget(req.Site)
		if err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		switch req.Action {
		case "retry":
			s, _ := d.siteByHost(req.Site)
			out, err := d.retryFailed(s.Path, s.PHP, req.Queue, ids)
			if err != nil {
				fail(w, http.StatusBadRequest, "%v", err)
				return
			}
			writeJSON(w, struct{ Output string }{out})
		case "forget":
			n, err := d.forgetFailed(t, req.Queue, ids)
			if err != nil {
				fail(w, http.StatusBadRequest, "%v", err)
				return
			}
			writeJSON(w, struct{ Removed int64 }{n})
		default:
			fail(w, http.StatusBadRequest, "action must be retry or forget")
		}
	})
	mux.HandleFunc("POST /api/queues/clear", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			DB  int
			Key string
		}
		if err := readJSON(r, &req); err != nil || req.Key == "" {
			fail(w, http.StatusBadRequest, "key required")
			return
		}
		addr, ok := d.redisAddr()
		if !ok {
			fail(w, http.StatusConflict, "Redis is not running")
			return
		}
		n, err := redisq.Clear(addr, req.DB, req.Key)
		if err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		writeJSON(w, struct{ Removed int64 }{n})
	})
}

func describeDB(t dbTarget) string {
	switch t.Conn {
	case "":
		return ""
	case "sqlite":
		return "SQLite " + t.Database
	}
	return fmt.Sprintf("%s database %s on %s:%d", t.Conn, t.Database, t.Host, t.Port)
}
