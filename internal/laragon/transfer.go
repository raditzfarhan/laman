//go:build windows

package laragon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// ErrPassword means Laragon's MySQL refused the root password given.
var ErrPassword = errors.New("Laragon's MySQL asked for a password")

// Source is Laragon's own mysqld, started on a spare 127.0.0.1 port so its
// databases can be dumped. Always Stop it.
type Source struct {
	bin    string // Laragon's MySQL bin folder
	cnfDir string // private temp folder holding cnf
	cnf    string // client options file with the credentials
	cmd    *exec.Cmd
	exited chan struct{}
	out    *tailBuffer // mysqld's console output

	stopOnce sync.Once
	stopErr  error
}

// Target is laman's MariaDB: its client program and port (user root, no
// password).
type Target struct {
	Client string
	Port   int
}

const (
	startTimeout = 60 * time.Second
	// shutdownWait is how long a requested shutdown may take before mysqld
	// counts as unresponsive and is killed.
	shutdownWait = 120 * time.Second
	adminTimeout = 20 * time.Second
	killWait     = 20 * time.Second
)

// How Stop ended mysqld.
const (
	stopExited = "exited"     // it had already stopped
	stopEvent  = "event"      // the MYSQLShutdown<pid> event (no credentials needed)
	stopAdmin  = "mysqladmin" // mysqladmin shutdown with the options file
	stopKill   = "kill"       // unresponsive: killed
)

// onStop, when set (tests), is told how each Source stopped and what mysqld
// printed.
var onStop func(path, output string)

// StartSource runs Laragon's mysqld on its data folder, bound to a free
// 127.0.0.1 port, and waits until root can log in with password. It
// returns ErrPassword when the password is refused.
func StartSource(l *Laragon, password string) (*Source, error) {
	if l.MySQLBin == "" || l.MySQLData == "" {
		return nil, errors.New("Laragon has no MySQL set up")
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "laman-laragon-")
	if err != nil {
		return nil, err
	}
	s := &Source{bin: l.MySQLBin, cnfDir: dir, cnf: filepath.Join(dir, "client.cnf"), exited: make(chan struct{}), out: &tailBuffer{}}
	if err := os.WriteFile(s.cnf, clientOptions(port, password), 0o600); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	s.cmd = exec.Command(filepath.Join(l.MySQLBin, "mysqld.exe"), serverArgs(l, port, dir)...)
	s.cmd.Stdout, s.cmd.Stderr = s.out, s.out
	// Should a stray child still hold the output pipe, don't let Wait hang.
	s.cmd.WaitDelay = 5 * time.Second
	hide(s.cmd)
	if err := s.cmd.Start(); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("start Laragon's mysqld: %w", err)
	}
	go func() { s.cmd.Wait(); close(s.exited) }()

	deadline := time.Now().Add(startTimeout)
	for {
		_, err := s.query("SELECT 1")
		switch {
		case err == nil:
			return s, nil
		case errors.Is(err, ErrPassword):
			return nil, s.stopWith(ErrPassword)
		case !errors.Is(err, errNotUp):
			return nil, s.stopWith(fmt.Errorf("log in to Laragon's MySQL: %w", err))
		}
		select {
		case <-s.exited:
			return nil, s.stopWith(fmt.Errorf("Laragon's mysqld stopped while starting:\n%s", s.out.String()))
		case <-time.After(500 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return nil, s.stopWith(fmt.Errorf("Laragon's mysqld did not accept connections within %s: %v\n%s", startTimeout, err, s.out.String()))
		}
	}
}

// stopWith stops the source after a failed start and returns err, joined
// with Stop's own error when that fails too (err itself otherwise, so
// err == ErrPassword still holds).
func (s *Source) stopWith(err error) error {
	if serr := s.Stop(); serr != nil {
		return errors.Join(err, serr)
	}
	return err
}

var mysqlVersion = regexp.MustCompile(`^mysql-(\d+)\.(\d+)\.(\d+)`)

// serverArgs are mysqld's options; privateDir holds the pid file. The user's
// events don't run and replication doesn't start, and as little as possible
// is written into Laragon's data folder. MySQL 8+ also gets --mysqlx=OFF
// and --no-monitor: on Windows it otherwise runs as a monitor process whose
// child is the real server, and killing the monitor leaves the server up.
func serverArgs(l *Laragon, port int, privateDir string) []string {
	args := []string{
		"--no-defaults",
		"--basedir=" + filepath.Dir(l.MySQLBin),
		"--datadir=" + l.MySQLData,
		"--port=" + strconv.Itoa(port),
		"--bind-address=127.0.0.1",
		"--skip-log-bin",
		"--console", // log to stderr, not a .err file in Laragon's data folder
		"--pid-file=" + filepath.Join(privateDir, "mysqld.pid"),
		"--event-scheduler=DISABLED",
		"--innodb-buffer-pool-dump-at-shutdown=OFF",
		"--innodb-buffer-pool-load-at-startup=OFF",
	}
	v := [3]int{8, 4, 0} // unknown folder name: assume a current MySQL
	if m := mysqlVersion.FindStringSubmatch(strings.ToLower(l.MySQLVersion)); m != nil {
		for i := range v {
			v[i], _ = strconv.Atoi(m[i+1])
		}
	}
	if v[0] >= 8 {
		args = append(args, "--mysqlx=OFF", "--no-monitor")
	}
	// skip-replica-start arrived in 8.0.26.
	if v[0] > 8 || v[0] == 8 && (v[1] > 0 || v[2] >= 26) {
		args = append(args, "--skip-replica-start")
	} else {
		args = append(args, "--skip-slave-start")
	}
	return args
}

// Stop shuts mysqld down cleanly and deletes the credentials file. It
// first signals mysqld's MYSQLShutdown<pid> event, which needs no password,
// and falls back to mysqladmin shutdown. mysqld gets 120 seconds to finish;
// only an unresponsive one is killed. Calling it again is a no-op.
func (s *Source) Stop() error {
	s.stopOnce.Do(func() {
		path := s.stop()
		os.RemoveAll(s.cnfDir)
		if onStop != nil {
			onStop(path, s.out.String())
		}
	})
	return s.stopErr
}

func (s *Source) stop() string {
	select {
	case <-s.exited:
		return stopExited
	default:
	}
	path := stopEvent
	err := requestShutdown(s.cmd.Process.Pid)
	if err != nil {
		path = stopAdmin
		ctx, cancel := context.WithTimeout(context.Background(), adminTimeout)
		err = s.tool(ctx, "mysqladmin.exe", "shutdown").Run()
		cancel()
	}
	if err == nil {
		select {
		case <-s.exited:
			return path
		case <-time.After(shutdownWait):
		}
	}
	// Unresponsive: kill the whole tree, in case mysqld started a child.
	tk := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(s.cmd.Process.Pid))
	hide(tk)
	tk.Run()
	kerr := s.cmd.Process.Kill() // fails harmlessly when taskkill got it
	select {
	case <-s.exited:
	case <-time.After(killWait):
		s.stopErr = fmt.Errorf("Laragon's mysqld (pid %d) did not exit: %v", s.cmd.Process.Pid, kerr)
	}
	return stopKill
}

// requestShutdown signals the named event mysqld on Windows creates for
// itself (MYSQLShutdown<pid>, case-sensitive; with --no-monitor the pid is
// the process we started); setting it makes the server shut down cleanly.
func requestShutdown(pid int) error {
	name, err := windows.UTF16PtrFromString("MYSQLShutdown" + strconv.Itoa(pid))
	if err != nil {
		return err
	}
	h, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, name)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.SetEvent(h)
}

// tool builds a Laragon MySQL client command that reads its credentials
// from the temporary options file (which must come first).
func (s *Source) tool(ctx context.Context, exe string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, filepath.Join(s.bin, exe), append([]string{"--defaults-extra-file=" + s.cnf}, args...)...)
	hide(cmd)
	return cmd
}

// query runs sql (any number of statements) on the source and returns the
// tab-separated result rows.
func (s *Source) query(sql string) (string, error) {
	cmd := s.tool(context.Background(), "mysql.exe", "-N", "-B", "--connect-timeout=10", "--default-character-set=utf8mb4")
	return runSQL(cmd, sql)
}

func (t Target) command(args ...string) *exec.Cmd {
	cmd := exec.Command(t.Client, append([]string{"--no-defaults", "--protocol=tcp", "-h", "127.0.0.1",
		"-P", strconv.Itoa(t.Port), "-u", "root", "--default-character-set=utf8mb4"}, args...)...)
	hide(cmd)
	return cmd
}

// query runs sql on laman's MariaDB and returns the tab-separated rows.
func (t Target) query(sql string) (string, error) {
	return runSQL(t.command("-N", "-B"), sql)
}

func runSQL(cmd *exec.Cmd, sql string) (string, error) {
	var stdout, stderr strings.Builder
	cmd.Stdin = strings.NewReader(sql)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if cerr := classify(stderr.String()); cerr != nil {
			return stdout.String(), cerr
		}
		return stdout.String(), err
	}
	return stdout.String(), nil
}

// errNotUp marks a client error that only means the server isn't
// accepting connections yet.
var errNotUp = errors.New("not accepting connections yet")

var errorCode = regexp.MustCompile(`ERROR (\d+)`)

// classify turns a MySQL client's error text into an error: ErrPassword
// for an access-denied login; errNotUp (with the text) for can't-connect
// (2002, 2003) and lost-during-handshake (2013); the text itself otherwise
// (nil when empty).
func classify(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if m := errorCode.FindStringSubmatch(text); m != nil {
		switch m[1] {
		case "1045":
			return ErrPassword
		case "2002", "2003", "2013":
			return fmt.Errorf("%w: %s", errNotUp, text)
		}
	}
	return errors.New(text)
}

var identRe = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// Copy creates db in laman's MariaDB with the source's default character
// set (collation mapped like the dump filter does), then loads
// mysqldump | Filter | mariadb into it. An existing laman database is never
// touched; a database Copy created is dropped again when anything fails.
// Tool messages go to log (nil discards them).
func Copy(s *Source, t Target, db string, log io.Writer) (err error) {
	if log == nil {
		log = io.Discard
	}
	log = &syncWriter{w: log}
	out, err := s.query("SELECT DEFAULT_CHARACTER_SET_NAME, DEFAULT_COLLATION_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = " + quoteString(db))
	if err != nil {
		return fmt.Errorf("read %s on Laragon's MySQL: %w", db, err)
	}
	row := strings.Split(strings.TrimRight(out, "\r\n"), "\t")
	if len(row) != 2 || !identRe.MatchString(row[0]) || !identRe.MatchString(row[1]) {
		return fmt.Errorf("Laragon's MySQL has no database %s", db)
	}
	charset, collation := row[0], mapCollation(row[1])
	if _, err := t.query(fmt.Sprintf("CREATE DATABASE %s CHARACTER SET %s COLLATE %s", quoteIdent(db), charset, collation)); err != nil {
		return fmt.Errorf("create %s in laman: %w", db, err)
	}
	defer func() {
		if err == nil {
			return
		}
		if _, derr := t.query("DROP DATABASE IF EXISTS " + quoteIdent(db)); derr != nil {
			fmt.Fprintf(log, "could not drop the half-loaded %s in laman: %v\n", db, derr)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var dumpErr, loadErr tailBuffer
	dump := s.tool(ctx, "mysqldump.exe",
		"--single-transaction", "--routines", "--triggers", "--events", "--hex-blob",
		"--default-character-set=utf8mb4", "--no-tablespaces", "--set-gtid-purged=OFF",
		"--", db)
	dump.Stderr = io.MultiWriter(log, &dumpErr)
	load := t.command("--max-allowed-packet=1G")
	load.Stdout = log
	load.Stderr = io.MultiWriter(log, &loadErr)

	src, err := dump.StdoutPipe()
	if err != nil {
		return err
	}
	dst, err := load.StdinPipe()
	if err != nil {
		return err
	}
	if err := load.Start(); err != nil {
		return fmt.Errorf("start laman's MariaDB client: %w", err)
	}
	if err := dump.Start(); err != nil {
		dst.Close()
		load.Wait()
		return fmt.Errorf("start Laragon's mysqldump: %w", err)
	}
	_, ferr := io.WriteString(dst, "USE "+quoteIdent(db)+";\n")
	if ferr == nil {
		ferr = Filter(dst, src)
	}
	dst.Close()
	if ferr != nil {
		cancel() // the loader stopped reading; don't leave mysqldump blocked on its output
	}
	derr := dump.Wait()
	lerr := load.Wait()
	switch {
	case lerr != nil:
		return fmt.Errorf("loading %s into laman's MariaDB: %v: %s", db, lerr, lastLine(loadErr.String()))
	case derr != nil:
		return fmt.Errorf("dumping %s from Laragon's MySQL: %v: %s", db, derr, lastLine(dumpErr.String()))
	case ferr != nil:
		return fmt.Errorf("copying %s: %w", db, ferr)
	}
	return nil
}

// clientOptions is the [client] options file for Laragon's MySQL tools.
func clientOptions(port int, password string) []byte {
	var b strings.Builder
	b.WriteString("[client]\nuser=root\nhost=127.0.0.1\nprotocol=tcp\nport=" + strconv.Itoa(port) + "\nget-server-public-key\n")
	if password != "" {
		// Option-file values may be quoted; these escapes are decoded.
		esc := strings.NewReplacer(`\`, `\\`, "\n", `\n`, "\r", `\r`, "\t", `\t`).Replace(password)
		b.WriteString(`password="` + esc + "\"\n")
	}
	return []byte(b.String())
}

// freePort finds a free TCP port on 127.0.0.1 (never 3306).
func freePort() (int, error) {
	for range 5 {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return 0, err
		}
		port := ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		if port != 3306 {
			return port, nil
		}
	}
	return 0, errors.New("no free port on 127.0.0.1")
}

func quoteIdent(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }

func quoteString(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, "'", "''").Replace(s) + "'"
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[i+1:])
	}
	return s
}

var getConsoleWindow = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow")

// hide keeps a child from opening a console window when laman has none
// (the dashboard and tray); in a terminal the child shares laman's console,
// as main's noConsole does.
func hide(cmd *exec.Cmd) {
	if h, _, _ := getConsoleWindow.Call(); h != 0 {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

// tailBuffer keeps the last 16 KB written to it; safe for concurrent use.
type tailBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if over := len(t.b) - 16<<10; over > 0 {
		t.b = append(t.b[:0], t.b[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}

// syncWriter serialises writes from the two tools' output copiers.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
