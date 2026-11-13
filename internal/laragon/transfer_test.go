//go:build windows

package laragon

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"laman/internal/config"
	"laman/internal/desktop"
	"laman/internal/install"
)

func TestSourceWrongPassword(t *testing.T) {
	denied := "ERROR 1045 (28000): Access denied for user 'root'@'localhost' (using password: NO)"
	if err := classify(denied); !errors.Is(err, ErrPassword) {
		t.Errorf("classify(1045) = %v, want ErrPassword", err)
	}
	other := "ERROR 2003 (HY000): Can't connect to MySQL server on '127.0.0.1:3307' (10061)"
	err := classify(other)
	if err == nil || errors.Is(err, ErrPassword) || !strings.Contains(err.Error(), other) {
		t.Errorf("classify(other) = %v, want the text passed through", err)
	}
	if err := classify(""); err != nil {
		t.Errorf("classify(\"\") = %v, want nil", err)
	}
}

func TestMapCollation(t *testing.T) {
	cases := map[string]string{
		"utf8mb4_0900_ai_ci":         "utf8mb4_uca1400_ai_ci",
		"utf8mb4_0900_bin":           "utf8mb4_bin",
		"utf8mb4_0900_ja_0900_as_cs": "utf8mb4_uca1400_ai_ci",
		"utf8mb4_unicode_ci":         "utf8mb4_unicode_ci",
		"latin1_swedish_ci":          "latin1_swedish_ci",
	}
	for in, want := range cases {
		if got := mapCollation(in); got != want {
			t.Errorf("mapCollation(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestServerArgs(t *testing.T) {
	l := &Laragon{MySQLVersion: "mysql-8.4.0-winx64", MySQLBin: `C:\l\bin\mysql\mysql-8.4.0-winx64\bin`, MySQLData: `C:\l\data\mysql-8.4`}
	got := strings.Join(serverArgs(l, 4321), " ")
	want := `--no-defaults --basedir=C:\l\bin\mysql\mysql-8.4.0-winx64 --datadir=C:\l\data\mysql-8.4 --port=4321 --bind-address=127.0.0.1 --skip-log-bin --console --mysqlx=OFF --no-monitor`
	if got != want {
		t.Errorf("8.4:\n got %s\nwant %s", got, want)
	}
	// MySQL 5.7 has neither X plugin switch nor monitor process.
	l.MySQLVersion = "mysql-5.7.33-winx64"
	if got := strings.Join(serverArgs(l, 4321), " "); strings.Contains(got, "mysqlx") || strings.Contains(got, "monitor") {
		t.Errorf("5.7: %s", got)
	}
}

func TestQuoteIdent(t *testing.T) {
	if got := quoteIdent("a`b"); got != "`a``b`" {
		t.Errorf("quoteIdent = %q", got)
	}
}

// --- integration: a throwaway MySQL data folder, laman's running MariaDB ---

// itTarget returns laman's MariaDB, skipping unless LAMAN_IT_LARAGON is set
// and that server answers.
func itTarget(t *testing.T) (*Laragon, Target) {
	t.Helper()
	root := os.Getenv("LAMAN_IT_LARAGON")
	if root == "" {
		t.Skip("set LAMAN_IT_LARAGON=<laragon root> to run")
	}
	l, err := Find(root)
	if err != nil || l.MySQLBin == "" {
		t.Skipf("no Laragon MySQL at %s: %v", root, err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Skipf("laman config: %v", err)
	}
	client := install.MariaDBBin(install.MariaDBInstallDir(config.Active("mariadb", cfg.MariaDB)), "mariadb", "mysql")
	if client == "" {
		t.Skip("laman has no MariaDB client")
	}
	tg := Target{Client: client, Port: cfg.MySQLPort}
	if _, err := tg.query("SELECT 1"); err != nil {
		t.Skipf("laman's MariaDB does not answer: %v", err)
	}
	return l, tg
}

// itSource starts Laragon's mysqld on a fresh data folder in a temp dir;
// Laragon's real data folder is never used.
func itSource(t *testing.T, real *Laragon) *Source {
	t.Helper()
	return itStart(t, itData(t, real), "")
}

// itData is a copy of real whose MySQLData is a freshly initialised temp
// folder (root, no password).
func itData(t *testing.T, real *Laragon) *Laragon {
	t.Helper()
	data := filepath.Join(t.TempDir(), "data")
	init := exec.Command(filepath.Join(real.MySQLBin, "mysqld.exe"), "--no-defaults", "--initialize-insecure",
		"--basedir="+filepath.Dir(real.MySQLBin), "--datadir="+data)
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("initialise temp data folder: %v\n%s", err, out)
	}
	l := *real
	l.MySQLData = data
	return &l
}

func itStart(t *testing.T, l *Laragon, password string) *Source {
	t.Helper()
	s, err := StartSource(l, password)
	if err != nil {
		t.Fatalf("StartSource: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
	return s
}

func TestSourcePasswordLive(t *testing.T) {
	real, _ := itTarget(t)
	l := itData(t, real)
	const pw = `p"a's\s x`
	s := itStart(t, l, "")
	if _, err := s.query("ALTER USER 'root'@'localhost' IDENTIFIED BY " + quoteString(pw) + ";"); err != nil {
		t.Fatal(err)
	}
	// The options file still holds no password: shutdown is refused, so Stop kills.
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := StartSource(l, ""); !errors.Is(err, ErrPassword) {
		t.Fatalf("StartSource without the password = %v, want ErrPassword", err)
	}
	s = itStart(t, l, pw)
	if got, err := s.query("SELECT CURRENT_USER()"); err != nil || !strings.HasPrefix(got, "root@") {
		t.Fatalf("query with the password: %q %v", got, err)
	}
}

func dropTarget(t *testing.T, tg Target, db string) {
	t.Helper()
	if !strings.HasPrefix(db, "lamantest_") {
		t.Fatalf("refusing to drop %s", db)
	}
	if _, err := tg.query("DROP DATABASE IF EXISTS " + quoteIdent(db)); err != nil {
		t.Errorf("drop %s in laman: %v", db, err)
	}
}

const roundTripNote = "see DEFINER=`root`@`localhost` utf8mb4_0900_ai_ci /*!80016 x */ it's \"quoted\""

func TestCopyRoundTrip(t *testing.T) {
	l, tg := itTarget(t)
	s := itSource(t, l)
	const db = "lamantest_import"
	dropTarget(t, tg, db)
	t.Cleanup(func() { dropTarget(t, tg, db) })

	setup := "CREATE DATABASE " + db + " DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;\n" +
		"USE " + db + ";\n" +
		"CREATE TABLE notes (id INT AUTO_INCREMENT PRIMARY KEY, body TEXT COLLATE utf8mb4_0900_ai_ci, meta JSON, n INT DEFAULT 0) " +
		"ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;\n" +
		"INSERT INTO notes (body, meta) VALUES ('" + strings.ReplaceAll(strings.ReplaceAll(roundTripNote, `\`, `\\`), "'", "''") + "', '{\"a\": [1, 2], \"b\": \"x\"}');\n" +
		"INSERT INTO notes (body, meta) VALUES ('second', NULL);\n" +
		"CREATE VIEW note_view AS SELECT id, body FROM notes;\n" +
		"DELIMITER ;;\n" +
		"CREATE PROCEDURE note_count(OUT c INT) BEGIN SELECT COUNT(*) INTO c FROM notes; END;;\n" +
		"CREATE TRIGGER note_bump BEFORE UPDATE ON notes FOR EACH ROW BEGIN SET NEW.n = OLD.n + 1; END;;\n" +
		"CREATE EVENT note_sweep ON SCHEDULE EVERY 1 DAY DISABLE DO DELETE FROM notes WHERE id < 0;;\n" +
		"DELIMITER ;\n"
	if _, err := s.query(setup); err != nil {
		t.Fatalf("create source data: %v", err)
	}

	var log strings.Builder
	if err := Copy(s, tg, db, &log); err != nil {
		t.Fatalf("Copy: %v\nlog:\n%s", err, log.String())
	}

	check := func(sql, want string) {
		t.Helper()
		got, err := tg.query(sql)
		if err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		if strings.TrimSpace(got) != want {
			t.Errorf("%s\n got %q\nwant %q", sql, strings.TrimSpace(got), want)
		}
	}
	check("SELECT COUNT(*) FROM "+db+".notes", "2")
	check("SELECT HEX(body) FROM "+db+".notes WHERE id = 1", strings.ToUpper(hexOf(roundTripNote)))
	check("SELECT JSON_VALUE(meta, '$.b') FROM "+db+".notes WHERE id = 1", "x")
	check("SELECT COUNT(*) FROM information_schema.VIEWS WHERE TABLE_SCHEMA = '"+db+"' AND TABLE_NAME = 'note_view'", "1")
	check("SELECT COUNT(*) FROM "+db+".note_view", "2")
	check("SELECT COUNT(*) FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = '"+db+"' AND ROUTINE_NAME = 'note_count'", "1")
	check("SELECT COUNT(*) FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA = '"+db+"' AND TRIGGER_NAME = 'note_bump'", "1")
	check("SELECT COUNT(*) FROM information_schema.EVENTS WHERE EVENT_SCHEMA = '"+db+"' AND EVENT_NAME = 'note_sweep'", "1")
	check("SELECT DEFAULT_COLLATION_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = '"+db+"'", "utf8mb4_uca1400_ai_ci")
}

func hexOf(s string) string {
	const digits = "0123456789abcdef"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		b.WriteByte(digits[s[i]>>4])
		b.WriteByte(digits[s[i]&15])
	}
	return b.String()
}

func TestCopyFailureDropsHalfLoaded(t *testing.T) {
	l, tg := itTarget(t)
	s := itSource(t, l)
	const db = "lamantest_missing"
	dropTarget(t, tg, db)
	t.Cleanup(func() { dropTarget(t, tg, db) })

	var log strings.Builder
	if err := Copy(s, tg, db, &log); err == nil {
		t.Fatal("Copy of a missing database succeeded")
	}
	assertNoTarget(t, tg, db)

	// A failure after the database was created in laman: mysqldump stops at
	// a view whose table is gone.
	const broken = "lamantest_broken"
	dropTarget(t, tg, broken)
	t.Cleanup(func() { dropTarget(t, tg, broken) })
	if _, err := s.query("CREATE DATABASE " + broken + "; USE " + broken + ";\n" +
		"CREATE TABLE a (x INT); INSERT INTO a VALUES (1); CREATE TABLE gone (x INT);\n" +
		"CREATE VIEW v AS SELECT x FROM gone; DROP TABLE gone;"); err != nil {
		t.Fatal(err)
	}
	log.Reset()
	if err := Copy(s, tg, broken, &log); err == nil {
		t.Fatal("Copy of a database with a broken view succeeded")
	} else {
		t.Logf("Copy error: %v", err)
	}
	assertNoTarget(t, tg, broken)
}

func assertNoTarget(t *testing.T, tg Target, db string) {
	t.Helper()
	got, err := tg.query("SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = '" + db + "'")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(got) != "0" {
		t.Errorf("laman still has %s", db)
	}
}

func TestCopyNeverReplacesExisting(t *testing.T) {
	l, tg := itTarget(t)
	s := itSource(t, l)
	const db = "lamantest_exists"
	dropTarget(t, tg, db)
	t.Cleanup(func() { dropTarget(t, tg, db) })
	if _, err := s.query("CREATE DATABASE " + db + "; CREATE TABLE " + db + ".a (x INT);"); err != nil {
		t.Fatal(err)
	}
	if _, err := tg.query("CREATE DATABASE " + db + "; CREATE TABLE " + db + ".keep (x INT);"); err != nil {
		t.Fatal(err)
	}
	if err := Copy(s, tg, db, nil); err == nil {
		t.Fatal("Copy over an existing laman database succeeded")
	}
	got, err := tg.query("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = '" + db + "' AND TABLE_NAME = 'keep'")
	if err != nil || strings.TrimSpace(got) != "1" {
		t.Errorf("existing laman database was touched: %q %v", got, err)
	}
}

func TestSourceStops(t *testing.T) {
	l, _ := itTarget(t)
	if p := desktop.ProcessesUnder(l.MySQLBin); len(p) > 0 {
		t.Skipf("something already runs from %s: %v", l.MySQLBin, p)
	}
	s := itSource(t, l)
	if len(desktop.ProcessesUnder(l.MySQLBin)) == 0 {
		t.Fatal("no mysqld running after StartSource")
	}
	cnf := s.cnf
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	if p := desktop.ProcessesUnder(l.MySQLBin); len(p) > 0 {
		t.Errorf("still running after Stop: %v", p)
	}
	if _, err := os.Stat(cnf); !os.IsNotExist(err) {
		t.Errorf("credentials file %s still exists", cnf)
	}
}
