// Package redisq reads Laravel queues straight from Redis, for the
// dashboard's Queues view. Laravel keeps a queue as a list
// <prefix>queues:<name> of waiting jobs (JSON payloads), plus sorted sets
// <prefix>queues:<name>:delayed and :reserved scored by a Unix time.
package redisq

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Conn is a minimal Redis (RESP2) client: enough for the commands below.
type Conn struct {
	c net.Conn
	r *bufio.Reader
}

func Dial(addr string) (*Conn, error) {
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return nil, err
	}
	c.SetDeadline(time.Now().Add(10 * time.Second))
	return &Conn{c: c, r: bufio.NewReader(c)}, nil
}

func (c *Conn) Close() error { return c.c.Close() }

// Do sends a command and returns its reply: string, int64, nil, or []any.
func (c *Conn) Do(args ...string) (any, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	if _, err := c.c.Write([]byte(b.String())); err != nil {
		return nil, err
	}
	return c.read()
}

func (c *Conn) read() (any, error) {
	line, err := c.r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSuffix(line, "\r\n")
	if line == "" {
		return nil, errors.New("redis: empty reply")
	}
	switch line[0] {
	case '+':
		return line[1:], nil
	case '-':
		return nil, errors.New("redis: " + line[1:])
	case ':':
		return strconv.ParseInt(line[1:], 10, 64)
	case '$':
		n, err := strconv.Atoi(line[1:])
		if err != nil || n < 0 {
			return nil, err
		}
		buf := make([]byte, n+2)
		if _, err := readFull(c.r, buf); err != nil {
			return nil, err
		}
		return string(buf[:n]), nil
	case '*':
		n, err := strconv.Atoi(line[1:])
		if err != nil || n < 0 {
			return nil, err
		}
		out := make([]any, n)
		for i := range out {
			if out[i], err = c.read(); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("redis: unexpected reply %q", line)
}

func readFull(r *bufio.Reader, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		m, err := r.Read(buf[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func (c *Conn) Int(args ...string) int64 {
	v, _ := c.Do(args...)
	n, _ := v.(int64)
	return n
}

func (c *Conn) Strings(args ...string) ([]string, error) {
	v, err := c.Do(args...)
	if err != nil {
		return nil, err
	}
	arr, _ := v.([]any)
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out, nil
}

// Queue is one Laravel queue with its job counts.
type Queue struct {
	DB       int    `json:"db"`
	Prefix   string `json:"prefix"` // e.g. "blog_database_"
	Name     string `json:"name"`   // e.g. "default"
	Key      string `json:"key"`    // the waiting list: <prefix>queues:<name>
	Waiting  int64  `json:"waiting"`
	Delayed  int64  `json:"delayed"`
	Reserved int64  `json:"reserved"` // being worked on now
}

// SplitKey takes a key apart: "blog_database_queues:emails:delayed" is
// prefix "blog_database_", queue "emails", part "delayed".
func SplitKey(key string) (prefix, name, part string, ok bool) {
	i := strings.LastIndex(key, "queues:")
	if i < 0 {
		return "", "", "", false
	}
	prefix, rest := key[:i], key[i+len("queues:"):]
	for _, p := range []string{"delayed", "reserved", "notify"} {
		if strings.HasSuffix(rest, ":"+p) {
			rest, part = strings.TrimSuffix(rest, ":"+p), p
			break
		}
	}
	if rest == "" {
		return "", "", "", false
	}
	return prefix, rest, part, true
}

// Queues lists every Laravel queue in the given databases.
func Queues(addr string, dbs []int) ([]Queue, error) {
	c, err := Dial(addr)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	var out []Queue
	for _, db := range dbs {
		if _, err := c.Do("SELECT", strconv.Itoa(db)); err != nil {
			return nil, err
		}
		found := map[string]bool{}
		cursor := "0"
		for {
			v, err := c.Do("SCAN", cursor, "MATCH", "*queues:*", "COUNT", "1000")
			if err != nil {
				return nil, err
			}
			arr, _ := v.([]any)
			if len(arr) != 2 {
				break
			}
			cursor, _ = arr[0].(string)
			keys, _ := arr[1].([]any)
			for _, k := range keys {
				key, _ := k.(string)
				if prefix, name, _, ok := SplitKey(key); ok {
					found[prefix+"queues:"+name] = true
				}
			}
			if cursor == "0" {
				break
			}
		}
		for key := range found {
			prefix, name, _, _ := SplitKey(key)
			out = append(out, Queue{
				DB: db, Prefix: prefix, Name: name, Key: key,
				Waiting:  c.Int("LLEN", key),
				Delayed:  c.Int("ZCARD", key+":delayed"),
				Reserved: c.Int("ZCARD", key+":reserved"),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Prefix != out[j].Prefix {
			return out[i].Prefix < out[j].Prefix
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Job is a queued job as the dashboard shows it.
type Job struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"` // the job class
	Attempts int     `json:"attempts"`
	Pushed   float64 `json:"pushed,omitempty"` // Unix time, when Laravel records it
	Due      float64 `json:"due,omitempty"`    // delayed: when it runs; reserved: when it times out
}

// Jobs returns up to limit jobs of a queue part: "waiting" (next first),
// "delayed" (soonest first) or "reserved".
func Jobs(addr string, db int, key, part string, limit int) ([]Job, error) {
	if _, name, p, ok := SplitKey(key); !ok || p != "" || name == "" {
		return nil, fmt.Errorf("not a queue: %q", key)
	}
	c, err := Dial(addr)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if _, err := c.Do("SELECT", strconv.Itoa(db)); err != nil {
		return nil, err
	}
	end := strconv.Itoa(limit - 1)
	var raw []string
	switch part {
	case "waiting":
		raw, err = c.Strings("LRANGE", key, "0", end)
	case "delayed", "reserved":
		raw, err = c.Strings("ZRANGE", key+":"+part, "0", end, "WITHSCORES")
	default:
		return nil, fmt.Errorf("unknown part %q", part)
	}
	if err != nil {
		return nil, err
	}
	var out []Job
	step := 1
	if part != "waiting" {
		step = 2
	}
	for i := 0; i+step-1 < len(raw); i += step {
		j := parseJob(raw[i])
		if step == 2 {
			j.Due, _ = strconv.ParseFloat(raw[i+1], 64)
		}
		out = append(out, j)
	}
	return out, nil
}

func parseJob(payload string) Job {
	var p struct {
		UUID        string  `json:"uuid"`
		ID          any     `json:"id"`
		DisplayName string  `json:"displayName"`
		Job         string  `json:"job"`
		Attempts    int     `json:"attempts"`
		PushedAt    flexNum `json:"pushedAt"`
		CreatedAt   flexNum `json:"createdAt"`
	}
	if json.Unmarshal([]byte(payload), &p) != nil {
		return Job{Name: "(not a Laravel job)"}
	}
	j := Job{ID: p.UUID, Name: p.DisplayName, Attempts: p.Attempts, Pushed: float64(p.PushedAt)}
	if j.ID == "" && p.ID != nil {
		j.ID = fmt.Sprint(p.ID)
	}
	if j.Name == "" {
		j.Name = p.Job
	}
	if j.Pushed == 0 {
		j.Pushed = float64(p.CreatedAt)
	}
	return j
}

// flexNum reads a number Laravel may write as a number or a string.
type flexNum float64

func (f *flexNum) UnmarshalJSON(b []byte) error {
	v, err := strconv.ParseFloat(strings.Trim(string(b), `"`), 64)
	if err == nil {
		*f = flexNum(v)
	}
	return nil
}

// Clear deletes a queue's waiting and delayed jobs. Jobs being worked on
// (reserved) are left for their workers.
func Clear(addr string, db int, key string) (int64, error) {
	if _, name, p, ok := SplitKey(key); !ok || p != "" || name == "" {
		return 0, fmt.Errorf("not a queue: %q", key)
	}
	c, err := Dial(addr)
	if err != nil {
		return 0, err
	}
	defer c.Close()
	if _, err := c.Do("SELECT", strconv.Itoa(db)); err != nil {
		return 0, err
	}
	n := c.Int("LLEN", key) + c.Int("ZCARD", key+":delayed")
	if _, err := c.Do("DEL", key, key+":delayed", key+":notify"); err != nil {
		return 0, err
	}
	return n, nil
}
