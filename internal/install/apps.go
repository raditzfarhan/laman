package install

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"laman/internal/config"
)

// Apps are web tools laman installs next to its servers and serves at
// <name>.<tld>. Each recipe below downloads one, prepares it for laman's
// databases, and says which PHP versions it runs on. The installed app
// lives in config.AppsDir()/<name>; data an app keeps there (dbzuper's
// accounts and saved connections) survives `laman app update`.

type AppSpec struct {
	Name     string `json:"name"`
	Title    string `json:"title"`
	Category string `json:"category"` // "database" apps can sit behind db.<tld>
	About    string `json:"about"`
	Source   string `json:"source"`
	// PHP is the supported range, e.g. ">=8.4" or ">=7.2,<8.4".
	PHP     string                                         `json:"php"`
	latest  func() (string, error)                         // newest version on offer
	install func(dir, version string, c *AppCtx) error     // into an empty dir
	keep    []string                                       // paths carried over on update
	done    func(dir string, c *AppCtx, fresh bool) string // a note to show after install
}

// AppCtx is what a recipe needs from laman.
type AppCtx struct {
	PHP     string // php.exe the app runs on
	Host    string // e.g. dbzuper.test
	URL     string // e.g. https://dbzuper.test
	MySQL   int
	Postgre int
	Redis   int
	// Which of laman's servers exist, for apps that pre-configure them.
	HasMariaDB, HasPostgres bool
	// Who the app's account is for (git's user.name / user.email).
	UserName, UserEmail string
	// From builds from a local source folder instead of a download.
	From string
	Old  string // previous install when updating, else ""
	Out  func(format string, a ...any)
}

var AppCatalog = []*AppSpec{dbzuperApp, adminerApp, phpMyAdminApp, phpRedisAdminApp}

func AppByName(name string) *AppSpec {
	for _, a := range AppCatalog {
		if a.Name == name {
			return a
		}
	}
	return nil
}

func AppDir(name string) string { return filepath.Join(config.AppsDir(), name) }

// LatestApp reports the newest version a recipe can install.
func LatestApp(spec *AppSpec) (string, error) { return spec.latest() }

// InstallApp installs or updates an app into AppsDir and returns the
// version installed and a note for the user. The new copy is built in a
// side folder and swapped in only when complete, so a failed update leaves
// the working app alone.
func InstallApp(spec *AppSpec, version string, c *AppCtx) (note string, err error) {
	dir := AppDir(spec.Name)
	staging := dir + ".new"
	os.RemoveAll(staging)
	if exists(dir) {
		c.Old = dir
	}
	if err := spec.install(staging, version, c); err != nil {
		os.RemoveAll(staging)
		return "", err
	}
	fresh := c.Old == ""
	if !fresh {
		for _, k := range spec.keep {
			if err := carry(filepath.Join(dir, k), filepath.Join(staging, k)); err != nil {
				os.RemoveAll(staging)
				return "", fmt.Errorf("keep %s: %w", k, err)
			}
		}
		old := dir + ".old"
		os.RemoveAll(old)
		if err := os.Rename(dir, old); err != nil {
			os.RemoveAll(staging)
			return "", fmt.Errorf("%w (is a file in %s open?)", err, dir)
		}
		if err := os.Rename(staging, dir); err != nil {
			os.Rename(old, dir) // put the working copy back
			return "", err
		}
		os.RemoveAll(old)
	} else if err := os.Rename(staging, dir); err != nil {
		return "", err
	}
	if spec.done != nil {
		note = spec.done(dir, c, fresh)
	}
	return note, nil
}

// carry moves a file or folder from the old install into the new one,
// replacing what the new one shipped.
func carry(from, to string) error {
	if !exists(from) {
		return nil
	}
	os.RemoveAll(to)
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	return os.Rename(from, to)
}

// RemoveApp deletes an app and everything it stored.
func RemoveApp(name string) error {
	os.Remove(ServerEnvPath(name))
	dir := AppDir(name)
	if !exists(dir) {
		return nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("%w (stop laman first if its PHP is still serving files there)", err)
	}
	return nil
}

// ServerEnvPath holds an app's server variables: values laman's web server
// hands the app's PHP on every request. They come from laman, never from
// the request, so a visitor cannot send one. The file sits beside the app
// folder, outside anything served, and survives updates.
func ServerEnvPath(name string) string { return filepath.Join(config.AppsDir(), name+".server-env") }

// ServerEnv reads an app's server variables (KEY=value lines); nil if none.
func ServerEnv(name string) map[string]string {
	b, err := os.ReadFile(ServerEnvPath(name))
	if err != nil {
		return nil
	}
	env := map[string]string{}
	for _, line := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && serverEnvKey.MatchString(k) && v != "" && !strings.ContainsAny(v, "\"\\ \t{}") {
			env[k] = v
		}
	}
	return env
}

var serverEnvKey = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// setServerEnv replaces an app's server variables.
func setServerEnv(name string, env map[string]string) error {
	var b strings.Builder
	b.WriteString("# Passed by laman's web server to this app's PHP. Keep private.\n")
	for k, v := range env {
		b.WriteString(k + "=" + v + "\n")
	}
	return os.WriteFile(ServerEnvPath(name), []byte(b.String()), 0o600)
}

// PHPFits reports whether a PHP version ("8.4.27-1") is in a range like
// ">=8.4" or ">=7.2,<8.4".
func PHPFits(version, rng string) bool {
	v := config.Line(strings.SplitN(version, "-", 2)[0])
	for _, part := range strings.Split(rng, ",") {
		part = strings.TrimSpace(part)
		switch {
		case part == "":
		case strings.HasPrefix(part, ">="):
			if config.CompareVersions(v, strings.TrimPrefix(part, ">=")) < 0 {
				return false
			}
		case strings.HasPrefix(part, "<"):
			if config.CompareVersions(v, strings.TrimPrefix(part, "<")) >= 0 {
				return false
			}
		}
	}
	return true
}

// run executes a command in dir with laman's PHP, Node and npm first on
// PATH (build tools such as Laravel Wayfinder call `php` themselves).
func run(c *AppCtx, dir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	path := filepath.Dir(c.PHP)
	if node := NodeDir(config.Latest("node")); node != "" {
		path += string(os.PathListSeparator) + node + string(os.PathListSeparator) + NpmPrefix()
	}
	cmd.Env = append(os.Environ(), "PATH="+path+string(os.PathListSeparator)+os.Getenv("PATH"),
		"COMPOSER_NO_INTERACTION=1", "CI=1", "NO_COLOR=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		tail := strings.TrimSpace(string(out))
		if len(tail) > 1500 {
			tail = "…" + tail[len(tail)-1500:]
		}
		return fmt.Errorf("%s %s: %w\n%s", filepath.Base(name), strings.Join(args, " "), err, tail)
	}
	return nil
}

// SetEnv sets KEY=value lines in a .env file, adding missing ones.
func SetEnv(path string, values map[string]string) error { return setEnv(path, values) }

// setEnv sets KEY=value lines in a .env file, adding missing ones.
func setEnv(path string, values map[string]string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	for k, v := range values {
		line := regexp.MustCompile(`(?m)^#?\s*` + regexp.QuoteMeta(k) + `=.*$`)
		if line.MatchString(s) {
			s = line.ReplaceAllLiteralString(s, k+"="+v)
		} else {
			s = strings.TrimRight(s, "\n") + "\n" + k + "=" + v + "\n"
		}
	}
	return os.WriteFile(path, []byte(s), 0o644)
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func githubLatest(repo string) (string, error) {
	var rel struct {
		Tag string `json:"tag_name"`
	}
	if err := getJSON("https://api.github.com/repos/"+repo+"/releases/latest", &rel); err != nil {
		return "", err
	}
	v := strings.TrimPrefix(rel.Tag, "v")
	if !versionTag.MatchString(v) {
		return "", fmt.Errorf("%s: unexpected release tag %q", repo, rel.Tag)
	}
	return v, nil
}

// ---- dbzuper -------------------------------------------------------------

var dbzuperApp = &AppSpec{
	Name: "dbzuper", Title: "DBZuper", Category: "database",
	About:  "Visual database workspace: explore schemas, query and browse MariaDB, PostgreSQL and SQLite.",
	Source: "github.com/dbzuper/dbzuper",
	PHP:    ">=8.4",
	latest: func() (string, error) { return githubLatest("dbzuper/dbzuper") },
	// Its own database (accounts, saved connections, history) and uploads.
	keep: []string{".env", "database/database.sqlite", "storage/app"},
	install: func(dir, version string, c *AppCtx) error {
		if c.From != "" {
			// A local checkout (DBZuper development): its source, not its
			// dependencies, build output, settings or data.
			c.Out("Copying DBZuper from %s\n", c.From)
			if err := copySource(c.From, dir, []string{".git", "vendor", "node_modules", ".env", "public/build", "public/hot",
				"database/database.sqlite", "storage", "bootstrap/cache"}); err != nil {
				return err
			}
			for _, d := range []string{"storage/app/private", "storage/framework/cache/data", "storage/framework/sessions",
				"storage/framework/views", "storage/logs", "bootstrap/cache"} {
				if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(d)), 0o755); err != nil {
					return err
				}
			}
		} else {
			// GitHub gives tag archives no fixed checksum; this relies on HTTPS.
			c.Out("Downloading DBZuper %s\n", version)
			zip, err := fetchAs("https://github.com/dbzuper/dbzuper/archive/refs/tags/v"+version+".zip",
				"dbzuper-"+version+".zip", "")
			if err != nil {
				return err
			}
			if err := unpack(zip, dir); err != nil {
				return err
			}
		}
		composer := ComposerPhar()
		if composer == "" {
			if _, err := Composer(); err != nil {
				return err
			}
			composer = ComposerPhar()
		}
		c.Out("Installing PHP packages\n")
		if err := run(c, dir, c.PHP, composer, "install", "--no-dev", "--optimize-autoloader", "--no-progress"); err != nil {
			return err
		}
		env := filepath.Join(dir, ".env")
		if c.Old != "" && exists(filepath.Join(c.Old, ".env")) {
			if err := copyInto(filepath.Join(c.Old, ".env"), env); err != nil {
				return err
			}
		} else if err := copyInto(filepath.Join(dir, ".env.example"), env); err != nil {
			return err
		}
		if err := setEnv(env, map[string]string{
			"APP_URL": c.URL, "APP_ENV": "production", "APP_DEBUG": "false", "LOG_LEVEL": "warning",
			// A private install on this computer: any 8+ character password.
			"DBZUPER_PASSWORD_POLICY": "basic",
		}); err != nil {
			return err
		}
		if c.Old != "" && exists(filepath.Join(c.Old, "database", "database.sqlite")) {
			if err := copyInto(filepath.Join(c.Old, "database", "database.sqlite"), filepath.Join(dir, "database", "database.sqlite")); err != nil {
				return err
			}
		} else {
			os.WriteFile(filepath.Join(dir, "database", "database.sqlite"), nil, 0o644)
			if err := run(c, dir, c.PHP, "artisan", "key:generate", "--force"); err != nil {
				return err
			}
		}
		if err := run(c, dir, c.PHP, "artisan", "migrate", "--force"); err != nil {
			return err
		}
		node := NodeDir(config.Latest("node"))
		if node == "" {
			c.Out("Installing Node (needed once, to build DBZuper's interface)\n")
			if _, err := Node(""); err != nil {
				return err
			}
			node = NodeDir(config.Latest("node"))
		}
		npm := filepath.Join(node, "node_modules", "npm", "bin", "npm-cli.js")
		c.Out("Building the interface (a minute or two)\n")
		if err := run(c, dir, filepath.Join(node, "node.exe"), npm, "ci", "--no-audit", "--no-fund", "--loglevel=error"); err != nil {
			return err
		}
		if err := run(c, dir, filepath.Join(node, "node.exe"), npm, "run", "build"); err != nil {
			return err
		}
		os.RemoveAll(filepath.Join(dir, "node_modules")) // only needed to build: ~300 MB
		os.Remove(filepath.Join(dir, "public", "hot"))
		return nil
	},
	done: dbzuperDone,
}

// dbzuperDone makes DBZuper ready to use. A DBZuper that supports it (0.3+)
// gets the administrator account, sign-in without a login screen from this
// computer, and connections to laman's servers; an older one shows its
// setup code instead.
func dbzuperDone(dir string, c *AppCtx, fresh bool) string {
	artisan := func(args ...string) (string, error) {
		cmd := exec.Command(c.PHP, append([]string{"artisan"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	conf, _ := os.ReadFile(filepath.Join(dir, "config", "dbzuper.php"))
	list, _ := artisan("list", "--raw")
	if !strings.Contains(string(conf), "auto_login_hash") || !strings.Contains(list, "dbzuper:connection") {
		if !fresh {
			return "Your accounts, connections and history were kept."
		}
		out, _ := artisan("dbzuper:setup-code")
		code := regexp.MustCompile(`[A-Z0-9]{4,}(?:-[A-Z0-9]{3,})+`).FindString(out)
		if code == "" {
			code = out
		}
		return fmt.Sprintf("Open %s and enter the setup code %s to create your account (any 8+ character password).\n"+
			"Then add connections: MariaDB 127.0.0.1:%d user root, PostgreSQL 127.0.0.1:%d user postgres, both with no password.",
			c.URL, code, c.MySQL, c.Postgre)
	}

	var notes []string
	// The administrator: refused (and fine) when one already exists. With
	// sign-in from this computer the password is never typed, so it is
	// random; `dbzuper:reset-password` sets a known one if ever needed.
	name, email := c.UserName, c.UserEmail
	if name == "" {
		name = "laman"
	}
	if email == "" || !strings.Contains(email, "@") {
		email = "you@laman.test"
	}
	if out, err := artisan("dbzuper:install", "--name="+name, "--email="+email, "--password="+randomHex(16)); err == nil {
		notes = append(notes, "Created your DBZuper account ("+email+").")
	} else if !strings.Contains(out, "already installed") {
		return "DBZuper is installed, but laman could not create its account: " + out
	}
	// Sign-in from this computer takes two keys: the setting with the key's
	// hash in .env, and the key itself, which only laman's web server hands
	// PHP (a server variable; visitors cannot send one). A copy of this .env
	// anywhere else signs no one in. A fresh key on every install or update.
	key := randomHex(32)
	sum := sha256.Sum256([]byte(key))
	if err := setServerEnv("dbzuper", map[string]string{"DBZUPER_AUTO_LOGIN_KEY": key}); err != nil {
		return "DBZuper is installed, but laman could not turn on sign-in from this computer: " + err.Error()
	}
	if err := setEnv(filepath.Join(dir, ".env"), map[string]string{
		"DBZUPER_AUTO_LOGIN": "true", "DBZUPER_AUTO_LOGIN_HASH": hex.EncodeToString(sum[:]),
	}); err != nil {
		return "DBZuper is installed, but laman could not turn on sign-in from this computer: " + err.Error()
	}
	artisan("config:clear")
	for _, conn := range []struct {
		on                      bool
		name, driver, port, usr string
	}{
		{c.HasMariaDB, "laman MariaDB", "mariadb", fmt.Sprint(c.MySQL), "root"},
		{c.HasPostgres, "laman PostgreSQL", "pgsql", fmt.Sprint(c.Postgre), "postgres"},
	} {
		if !conn.on {
			continue
		}
		if out, err := artisan("dbzuper:connection", conn.name, "--driver="+conn.driver, "--host=127.0.0.1",
			"--port="+conn.port, "--username="+conn.usr, "--if-missing"); err != nil {
			notes = append(notes, "Could not add "+conn.name+": "+out)
		} else if strings.Contains(out, "Added") {
			notes = append(notes, "Added the "+conn.name+" connection.")
		}
	}
	notes = append(notes, "Open "+c.URL+": you are signed in already, no password needed on this computer.")
	if !fresh {
		notes = append([]string{"Your accounts, connections and history were kept."}, notes...)
	}
	return strings.Join(notes, "\n")
}

// copySource copies a source tree, leaving out the given relative paths
// (slash-separated) and anything under them.
func copySource(src, dest string, skip []string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		slash := filepath.ToSlash(rel)
		for _, s := range skip {
			if slash == s || strings.HasPrefix(slash, s+"/") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		target := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return copyFile(path, target)
	})
}

// ---- Adminer -------------------------------------------------------------

var adminerAsset = regexp.MustCompile(`^adminer-([0-9.]+)-en\.php$`)

var adminerApp = &AppSpec{
	Name: "adminer", Title: "Adminer", Category: "database",
	About:  "One-file database manager for MariaDB and PostgreSQL. Signs you in to laman's servers without a password.",
	Source: "github.com/vrana/adminer",
	PHP:    ">=7.4",
	latest: func() (string, error) { return githubLatest("vrana/adminer") },
	install: func(dir, version string, c *AppCtx) error {
		var rel struct {
			Assets []struct {
				Name   string `json:"name"`
				URL    string `json:"browser_download_url"`
				Digest string `json:"digest"`
			} `json:"assets"`
		}
		if err := getJSON("https://api.github.com/repos/vrana/adminer/releases/tags/v"+version, &rel); err != nil {
			return err
		}
		for _, a := range rel.Assets {
			if m := adminerAsset.FindStringSubmatch(a.Name); m == nil || m[1] != version {
				continue
			}
			sum := strings.TrimPrefix(a.Digest, "sha256:")
			if !shaHex.MatchString(sum) {
				return fmt.Errorf("Adminer %s: GitHub lists no SHA-256 for %s", version, a.Name)
			}
			c.Out("Downloading Adminer %s\n", version)
			file, err := fetch(a.URL, sum)
			if err != nil {
				// GitHub's release storage has outages; adminer.org serves the
				// same file, still checked against GitHub's SHA-256.
				c.Out("GitHub did not serve it (%v); trying adminer.org\n", err)
				file, err = fetch("https://www.adminer.org/static/download/"+version+"/"+a.Name, sum)
				if err != nil {
					return err
				}
			}
			if err := copyInto(file, filepath.Join(dir, "adminer.php")); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dir, "index.php"), []byte(adminerIndex), 0o644)
		}
		return fmt.Errorf("Adminer %s: no English single-file build in the release", version)
	},
}

// adminerIndex lets Adminer sign in to laman's servers, whose admin users
// have no password, and only from this computer. Adminer refuses empty
// passwords by default.
const adminerIndex = `<?php
// laman: Adminer for the database servers on this computer.
function adminer_object() {
    class LamanAdminer extends Adminer\Adminer {
        function name() { return 'Adminer · laman'; }
        function login($login, $password) {
            // A blank password is fine for servers on this computer only;
            // anything else signs in the normal way, with a password.
            if ($password !== '') {
                return true;
            }
            $server = Adminer\SERVER;
            return $server === '' || preg_match('~^(127\.0\.0\.1|localhost|\[::1\])(:\d+)?$~', $server) === 1;
        }
    }
    return new LamanAdminer;
}
include __DIR__ . '/adminer.php';
`

// ---- phpRedisAdmin -------------------------------------------------------

var phpRedisAdminApp = &AppSpec{
	Name: "phpredisadmin", Title: "phpRedisAdmin", Category: "redis",
	About:  "Browse and edit Redis keys, see server info. Connected to laman's Redis; served at redis.<tld> too.",
	Source: "github.com/ErikDubbelboer/phpRedisAdmin",
	PHP:    ">=7.4",
	latest: func() (string, error) { return githubLatest("ErikDubbelboer/phpRedisAdmin") },
	install: func(dir, version string, c *AppCtx) error {
		// GitHub gives tag archives no fixed checksum; this relies on HTTPS.
		c.Out("Downloading phpRedisAdmin %s\n", version)
		zip, err := fetchAs("https://github.com/ErikDubbelboer/phpRedisAdmin/archive/refs/tags/v"+version+".zip",
			"phpredisadmin-"+version+".zip", "")
		if err != nil {
			return err
		}
		if err := unpack(zip, dir); err != nil {
			return err
		}
		composer := ComposerPhar()
		if composer == "" {
			if _, err := Composer(); err != nil {
				return err
			}
			composer = ComposerPhar()
		}
		c.Out("Installing PHP packages\n")
		if err := run(c, dir, c.PHP, composer, "install", "--no-dev", "--optimize-autoloader", "--no-progress"); err != nil {
			return err
		}
		sample, err := os.ReadFile(filepath.Join(dir, "includes", "config.sample.inc.php"))
		if err != nil {
			return err
		}
		conf, err := phpRedisAdminConfig(string(sample), c.Redis)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, "includes", "config.inc.php"), []byte(conf), 0o644)
	},
	done: func(dir string, c *AppCtx, fresh bool) string {
		alias := strings.Replace(c.URL, "//"+c.Host, "//redis."+strings.SplitN(c.Host, ".", 2)[1], 1)
		return "Open " + alias + " (or " + c.URL + "): it is connected to laman's Redis."
	},
}

// phpRedisAdminConfig points the sample config's first server at laman's
// Redis. It fails rather than guess if the sample's layout changed.
func phpRedisAdminConfig(sample string, port int) (string, error) {
	name := regexp.MustCompile(`'name'\s*=>\s*'local server'`)
	portRe := regexp.MustCompile(`'port'\s*=>\s*6379,`)
	noAuth := regexp.MustCompile(`'allow_no_auth'\s*=>\s*false,`)
	if !name.MatchString(sample) || !portRe.MatchString(sample) || !noAuth.MatchString(sample) {
		return "", fmt.Errorf("phpRedisAdmin: its sample config changed; laman cannot set it up")
	}
	conf := name.ReplaceAllLiteralString(sample, `'name'   => 'laman Redis'`)
	// No login: laman serves it on 127.0.0.1 only and never shares apps,
	// the isolated local install this setting is for (its forms keep CSRF
	// tokens).
	conf = noAuth.ReplaceAllLiteralString(conf, `'allow_no_auth' => true,`)
	loc := portRe.FindStringIndex(conf) // the first server only
	conf = conf[:loc[0]] + fmt.Sprintf("'port'   => %d,", port) + conf[loc[1]:]
	return "<?php\n// Written by laman: the first server is laman's Redis.\n?>" + conf, nil
}

// ---- phpMyAdmin ----------------------------------------------------------

var phpMyAdminApp = &AppSpec{
	Name: "phpmyadmin", Title: "phpMyAdmin", Category: "database",
	About:  "The classic MariaDB/MySQL manager, signed in as root to laman's MariaDB.",
	Source: "phpmyadmin.net",
	PHP:    ">=7.2,<8.4", // what phpMyAdmin 5.2 supports; updated from version.json
	install: func(dir, version string, c *AppCtx) error {
		base := "https://files.phpmyadmin.net/phpMyAdmin/" + version + "/phpMyAdmin-" + version + "-all-languages.zip"
		sum, err := getText(base + ".sha256")
		if err != nil {
			return err
		}
		if sum = strings.Fields(sum + " ")[0]; !shaHex.MatchString(sum) {
			return fmt.Errorf("phpMyAdmin: unexpected checksum %q", sum)
		}
		c.Out("Downloading phpMyAdmin %s\n", version)
		zip, err := fetch(base, sum)
		if err != nil {
			return err
		}
		if err := unpack(zip, dir); err != nil {
			return err
		}
		os.MkdirAll(filepath.Join(dir, "tmp"), 0o755)
		conf := `<?php
// Written by laman: signed in as root to laman's MariaDB on this computer.
$cfg['blowfish_secret'] = sodium_hex2bin('` + randomHex(32) + `');
$cfg['Servers'][1]['auth_type'] = 'config';
$cfg['Servers'][1]['host'] = '127.0.0.1';
$cfg['Servers'][1]['port'] = '` + strconv.Itoa(c.MySQL) + `';
$cfg['Servers'][1]['user'] = 'root';
$cfg['Servers'][1]['password'] = '';
$cfg['Servers'][1]['AllowNoPassword'] = true;
$cfg['TempDir'] = __DIR__ . '/tmp';
$cfg['VersionCheck'] = false;
`
		return os.WriteFile(filepath.Join(dir, "config.inc.php"), []byte(conf), 0o644)
	},
}

// The recipe refers to itself (to follow phpMyAdmin's supported PHP
// range), so it is wired up here rather than in its literal.
func init() { phpMyAdminApp.latest = phpMyAdminLatest }

func phpMyAdminLatest() (string, error) {
	var v struct {
		Version  string `json:"version"`
		Releases []struct {
			Version string `json:"version"`
			PHP     string `json:"php_versions"`
		} `json:"releases"`
	}
	if err := getJSON("https://www.phpmyadmin.net/home_page/version.json", &v); err != nil {
		return "", err
	}
	for _, r := range v.Releases {
		if r.Version == v.Version && r.PHP != "" {
			phpMyAdminApp.PHP = r.PHP // follow its published support
		}
	}
	if !versionTag.MatchString(v.Version) {
		return "", fmt.Errorf("phpMyAdmin: unexpected version %q", v.Version)
	}
	return v.Version, nil
}
