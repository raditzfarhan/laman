// Package config holds laman's on-disk configuration and directory layout.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"laman/internal/sites"
)

type Config struct {
	// SitesDir is the main project folder; MoreSitesDirs are further ones.
	// Every folder inside any of them is served as a site (see SitesDirs).
	SitesDir      string   `json:"sitesDir"`
	MoreSitesDirs []string `json:"moreSitesDirs,omitempty"`
	// FolderPrefix names a project folder's sites <prefix>-<folder>, by
	// folder path (acme -> acme-api.test).
	FolderPrefix map[string]string `json:"folderPrefix,omitempty"`
	TLD          string            `json:"tld"`
	HTTPPort     int               `json:"httpPort"`
	HTTPSPort    int               `json:"httpsPort"` // 0 disables HTTPS
	PHP          string            `json:"php"`
	Node         string            `json:"node,omitempty"` // default Node.js version
	// MariaDB, Redis and Postgres hold the active line ("12.3", "8.10",
	// "17"); the newest installed patch of that line runs. Each line keeps
	// its own data.
	MariaDB      string            `json:"mariadb,omitempty"`
	Redis        string            `json:"redis,omitempty"`
	Postgres     string            `json:"postgres,omitempty"`
	PHPWorkers   int               `json:"phpWorkers"`
	PHPBasePort  int               `json:"phpBasePort"`
	MySQLPort    int               `json:"mysqlPort"`
	RedisPort    int               `json:"redisPort"`
	PostgresPort int               `json:"postgresPort"`
	SMTPPort     int               `json:"smtpPort"`   // Mailpit catches mail sent here
	MailUIPort   int               `json:"mailUiPort"` // Mailpit's web inbox
	ControlPort  int               `json:"controlPort"`
	Links        map[string]string `json:"links"`
	// Isolated pins a site (by name) to an installed PHP version.
	Isolated map[string]string `json:"isolated"`
	// Apps are web tools laman installed into AppsDir (dbzuper, adminer,
	// phpmyadmin), by name -> version; each is served at <name>.<tld>.
	// DBClient picks the database app behind db.<tld>.
	Apps     map[string]string `json:"apps,omitempty"`
	DBClient string            `json:"dbClient,omitempty"`
	// Services turns add-on servers (mariadb, postgres, redis, mailpit) on
	// or off. Missing means on, so a new install starts everything.
	Services map[string]bool `json:"services,omitempty"`
	// Share: `laman share` on the user's own domain (Cloudflare named
	// tunnel). Empty ShareDomain means random trycloudflare.com links.
	// SharePattern names a shared site's address, "{site}" by default
	// (blog -> blog.<ShareDomain>); ShareTunnel is the tunnel's ID.
	ShareDomain  string `json:"shareDomain,omitempty"`
	SharePattern string `json:"sharePattern,omitempty"`
	ShareTunnel  string `json:"shareTunnel,omitempty"`
	// Theme is the dashboard's look: "light", "dark", or "" to follow
	// the system. The localhost page uses it too.
	Theme string `json:"theme,omitempty"`
	// Editor opens a site's folder from the dashboard: an editor ID
	// ("vscode", "cursor", ...) or "" for the first one installed.
	Editor string `json:"editor,omitempty"`
	// Proxies: `laman proxy <name> <port>` sites, name -> "127.0.0.1:3000".
	Proxies map[string]string `json:"proxies,omitempty"`
	// DevAuto: Node sites (by name) whose dev server starts with laman.
	DevAuto map[string]bool `json:"devAuto,omitempty"`
	// NoUpdateCheck stops the daily look for a newer laman release.
	NoUpdateCheck bool `json:"noUpdateCheck,omitempty"`
}

// Discover lists every site laman serves: project folders, links, apps
// and proxies, with any name clashes between project folders.
func (c *Config) Discover() ([]sites.Site, []sites.Clash) {
	list, clashes := sites.DiscoverAll(c.Folders(), c.TLD, c.ServedLinks())
	return sites.WithProxies(list, c.Proxies, c.TLD), clashes
}

// SetProxy makes <name>.<tld> forward to target: a port ("3000") or
// host:port on this computer. An empty target removes the proxy. It
// returns the name as used. It does not save the config.
func (c *Config) SetProxy(name, target string) (string, error) {
	name = sites.Slug(name)
	if name == "" {
		return "", errors.New("give the site a name: letters, digits and dashes")
	}
	if target == "" {
		if _, ok := c.Proxies[name]; !ok {
			return "", fmt.Errorf("no proxy called %s", name)
		}
		delete(c.Proxies, name)
		return name, nil
	}
	addr, err := ProxyTarget(target)
	if err != nil {
		return "", err
	}
	if c.Proxies == nil {
		c.Proxies = map[string]string{}
	}
	c.Proxies[name] = addr
	return name, nil
}

// ProxyTarget turns "3000", ":3000", "localhost:3000" or
// "http://127.0.0.1:3000" into host:port. Only this computer is allowed.
func ProxyTarget(t string) (string, error) {
	t = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(t), "http://"), "https://"), "/")
	host, port := "127.0.0.1", t
	if i := strings.LastIndex(t, ":"); i >= 0 {
		host, port = t[:i], t[i+1:]
	}
	switch strings.ToLower(host) {
	case "", "127.0.0.1", "localhost":
		host = "127.0.0.1"
	case "[::1]":
	default:
		return "", fmt.Errorf("%s: laman only forwards to ports on this computer (localhost)", host)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("%q is not a port", port)
	}
	return host + ":" + strconv.Itoa(n), nil
}

// SitesDirs is every project folder, the main one first. Earlier folders
// win when two hold a project of the same name.
func (c *Config) SitesDirs() []string {
	out := []string{c.SitesDir}
	for _, d := range c.MoreSitesDirs {
		if !containsDir(out, d) {
			out = append(out, d)
		}
	}
	return out
}

// Folders is SitesDirs with each folder's name prefix.
func (c *Config) Folders() []sites.Folder {
	var out []sites.Folder
	for _, d := range c.SitesDirs() {
		out = append(out, sites.Folder{Dir: d, Prefix: c.prefixFor(d)})
	}
	return out
}

func (c *Config) prefixFor(dir string) string {
	for k, v := range c.FolderPrefix {
		if sameDir(k, dir) {
			return v
		}
	}
	return ""
}

func (c *Config) setPrefix(dir, prefix string) {
	for k := range c.FolderPrefix {
		if sameDir(k, dir) {
			delete(c.FolderPrefix, k)
		}
	}
	if prefix != "" {
		if c.FolderPrefix == nil {
			c.FolderPrefix = map[string]string{}
		}
		c.FolderPrefix[dir] = prefix
	}
	if len(c.FolderPrefix) == 0 {
		c.FolderPrefix = nil
	}
}

// SetFolderPrefix puts prefix in front of the names of a project folder's
// sites; "" names them after their folders alone. It returns the folder and
// the prefix as used (a valid DNS label). It does not save the config.
func (c *Config) SetFolderPrefix(dir, prefix string) (string, string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}
	var found string
	for _, d := range c.SitesDirs() {
		if sameDir(d, abs) {
			found = d
		}
	}
	if found == "" {
		return "", "", fmt.Errorf("%s is not a project folder", abs)
	}
	p := sites.Slug(prefix)
	if strings.TrimSpace(prefix) != "" && p == "" {
		return "", "", fmt.Errorf("%q has no letters or digits to use", prefix)
	}
	if len(p) > 40 {
		return "", "", errors.New("keep the prefix under 40 characters")
	}
	c.setPrefix(found, p)
	return found, p, nil
}

func sameDir(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

func containsDir(list []string, d string) bool {
	for _, x := range list {
		if sameDir(x, d) {
			return true
		}
	}
	return false
}

// AddSitesDir adds a project folder (an existing directory). It does not
// save the config.
func (c *Config) AddSitesDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	fi, err := os.Stat(abs)
	if err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%s is not a folder", abs)
	}
	if containsDir(c.SitesDirs(), abs) {
		return "", fmt.Errorf("%s is already a project folder", abs)
	}
	home := filepath.Clean(Home())
	if sameDir(abs, home) || strings.HasPrefix(strings.ToLower(abs), strings.ToLower(home)+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is inside laman's own folder", abs)
	}
	if len(abs) <= 3 { // C:\ and the like
		return "", fmt.Errorf("a whole drive (%s) cannot be a project folder", abs)
	}
	// A group inside another project folder (www\acme inside www)
	// gets its name as a prefix, so its generic names (api, docs) do not
	// clash with the outer folder's.
	for _, d := range c.SitesDirs() {
		if strings.HasPrefix(strings.ToLower(abs), strings.ToLower(filepath.Clean(d))+string(filepath.Separator)) {
			c.setPrefix(abs, sites.Slug(filepath.Base(abs)))
			break
		}
	}
	c.MoreSitesDirs = append(c.MoreSitesDirs, abs)
	return abs, nil
}

// RemoveSitesDir stops serving a project folder (nothing on disk changes).
// Removing the main folder makes the next one the main folder; the last
// folder cannot be removed. It does not save the config.
func (c *Config) RemoveSitesDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	all := c.SitesDirs()
	if !containsDir(all, abs) {
		return "", fmt.Errorf("%s is not a project folder", abs)
	}
	if len(all) == 1 {
		return "", errors.New("laman needs at least one project folder; add another one first")
	}
	var rest []string
	for _, d := range all {
		if !sameDir(d, abs) {
			rest = append(rest, d)
		}
	}
	c.setPrefix(abs, "")
	c.SitesDir, c.MoreSitesDirs = rest[0], rest[1:]
	if len(c.MoreSitesDirs) == 0 {
		c.MoreSitesDirs = nil
	}
	for _, d := range all {
		if sameDir(d, abs) {
			return d, nil
		}
	}
	return abs, nil
}

// ShareDir holds laman's own Cloudflare sign-in and tunnel credentials,
// apart from the user's ~/.cloudflared.
func ShareDir() string { return filepath.Join(Home(), "etc", "cloudflared") }

// ShareCert is the Cloudflare sign-in certificate `cloudflared tunnel
// login` writes when run with HOME set to ShareDir.
func ShareCert() string { return filepath.Join(ShareDir(), ".cloudflared", "cert.pem") }

// ShareCredentials is the named tunnel's credentials file.
func ShareCredentials() string { return filepath.Join(ShareDir(), "tunnel.json") }

// ShareHost is the public host name for a site label ("blog") on the
// user's domain, or "" when no domain is set up.
func (c *Config) ShareHost(label string) string {
	if c.ShareDomain == "" {
		return ""
	}
	p := c.SharePattern
	if p == "" {
		p = "{site}"
	}
	return strings.ReplaceAll(p, "{site}", label) + "." + c.ShareDomain
}

// Home is laman's root directory: %LAMAN_HOME%, or ~/.laman.
func Home() string {
	if h := os.Getenv("LAMAN_HOME"); h != "" {
		return h
	}
	u, _ := os.UserHomeDir()
	return filepath.Join(u, ".laman")
}

func Path() string              { return filepath.Join(Home(), "config.json") }
func BinDir(kind string) string { return filepath.Join(Home(), "bin", kind) }
func DataDir() string           { return filepath.Join(Home(), "data") }
func LogDir() string            { return filepath.Join(Home(), "logs") }
func CacheDir() string          { return filepath.Join(Home(), "cache") }
func EtcDir() string            { return filepath.Join(Home(), "etc") }
func CaddyDir() string          { return filepath.Join(EtcDir(), "caddy") }
func CADir() string             { return filepath.Join(EtcDir(), "ca") }
func CertDir() string           { return filepath.Join(EtcDir(), "certs") }

func Default() *Config {
	u, _ := os.UserHomeDir()
	return &Config{
		SitesDir:     filepath.Join(u, "Sites"),
		TLD:          "test",
		HTTPPort:     80,
		HTTPSPort:    443,
		PHPWorkers:   4,
		PHPBasePort:  9100,
		MySQLPort:    3306,
		RedisPort:    6379,
		PostgresPort: 5432,
		SMTPPort:     1025,
		MailUIPort:   8025,
		ControlPort:  7171,
		Links:        map[string]string{},
		Isolated:     map[string]string{},
	}
}

func Load() (*Config, error) {
	c := Default()
	b, err := os.ReadFile(Path())
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", Path(), err)
	}
	if c.Isolated == nil {
		c.Isolated = map[string]string{}
	}
	if c.Links == nil {
		c.Links = map[string]string{}
	}
	if c.PHPWorkers < 1 {
		c.PHPWorkers = 1
	}
	return c, nil
}

func (c *Config) Save() error {
	if err := os.MkdirAll(Home(), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := Path() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, Path())
}

// Installed lists installed versions of a component, oldest first.
func Installed(kind string) []string {
	entries, err := os.ReadDir(BinDir(kind))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasSuffix(e.Name(), ".tmp") {
			out = append(out, e.Name())
		}
	}
	sort.Slice(out, func(i, j int) bool { return CompareVersions(out[i], out[j]) < 0 })
	return out
}

// Latest returns the newest installed version of kind, or "".
func Latest(kind string) string {
	v := Installed(kind)
	if len(v) == 0 {
		return ""
	}
	return v[len(v)-1]
}

// Match finds the newest installed version equal to want or starting with
// want followed by a separator ("8.4" matches "8.4.27-1" but not "8.40.0").
func Match(kind, want string) string {
	v := Installed(kind)
	for i := len(v) - 1; i >= 0; i-- {
		if v[i] == want || strings.HasPrefix(v[i], want+".") || strings.HasPrefix(v[i], want+"-") {
			return v[i]
		}
	}
	return ""
}

// CompareVersions compares dotted versions numerically, segment by segment.
func CompareVersions(a, b string) int {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func versionParts(v string) []int {
	var out []int
	for _, f := range strings.FieldsFunc(v, func(r rune) bool { return !unicode.IsDigit(r) }) {
		n, _ := strconv.Atoi(f)
		out = append(out, n)
	}
	return out
}

// DefaultPHP is the configured PHP version, or the newest installed one if
// the configured version is missing.
func (c *Config) DefaultPHP() string {
	if c.PHP != "" && Match("php", c.PHP) == c.PHP {
		return c.PHP
	}
	return Latest("php")
}

// NodeLink is a directory junction to the default Node version. It sits on
// the user's PATH, so plain `node`, `npm` and `npx` use the default.
// AppsDir holds the web apps laman installs.
func AppsDir() string { return filepath.Join(Home(), "apps") }

// DBClients are the database apps, in order of preference for db.<tld>.
var DBClients = []string{"dbzuper", "adminer", "phpmyadmin"}

// DefaultDBClient is the installed database app behind db.<tld>: the one
// chosen with DBClient, else the first installed in DBClients order.
func (c *Config) DefaultDBClient() string {
	if _, ok := c.Apps[c.DBClient]; ok && c.DBClient != "" {
		return c.DBClient
	}
	for _, name := range DBClients {
		if _, ok := c.Apps[name]; ok {
			return name
		}
	}
	return ""
}

// RedisClients are the Redis apps, in order of preference for redis.<tld>.
var RedisClients = []string{"phpredisadmin"}

// DefaultRedisClient is the installed Redis app behind redis.<tld>, or "".
func (c *Config) DefaultRedisClient() string {
	for _, name := range RedisClients {
		if _, ok := c.Apps[name]; ok {
			return name
		}
	}
	return ""
}

// ServedLinks is every folder served outside SitesDir: the user's links,
// the installed apps, db.<tld> for the default database app and
// redis.<tld> for the Redis app.
func (c *Config) ServedLinks() map[string]string {
	out := map[string]string{}
	for name, p := range c.Links {
		out[name] = p
	}
	for name := range c.Apps {
		out[name] = filepath.Join(AppsDir(), name)
	}
	if db := c.DefaultDBClient(); db != "" {
		out["db"] = filepath.Join(AppsDir(), db)
	}
	if r := c.DefaultRedisClient(); r != "" {
		out["redis"] = filepath.Join(AppsDir(), r)
	}
	return out
}

// AppFor names the app a served site belongs to ("db" belongs to the
// default database app), or "".
func (c *Config) AppFor(site string) string {
	switch site {
	case "db":
		return c.DefaultDBClient()
	case "redis":
		return c.DefaultRedisClient()
	}
	if _, ok := c.Apps[site]; ok {
		return site
	}
	return ""
}

// Enabled reports whether an add-on server should run with laman.
func (c *Config) Enabled(kind string) bool {
	on, set := c.Services[kind]
	return !set || on
}

// SetEnabled records whether an add-on server runs with laman.
func (c *Config) SetEnabled(kind string, on bool) {
	if c.Services == nil {
		c.Services = map[string]bool{}
	}
	c.Services[kind] = on
}

func NodeLink() string { return filepath.Join(Home(), "node") }

// NodeFor picks the Node version for a folder: the nearest .nvmrc or
// .node-version above it; else, when package.json's engines.node rules out
// the default, the newest installed version it allows; else the default.
// from names the file used; a version an .nvmrc asks for may not be
// installed.
func (c *Config) NodeFor(dir string) (version, from string) {
	def := c.DefaultNode()
	start := dir
	for ; ; dir = filepath.Dir(dir) {
		for _, name := range []string{".nvmrc", ".node-version"} {
			b, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			want := NodeSpec(string(b))
			if want == "" {
				return def, "" // "lts/*", "node" and friends: use the default
			}
			if v := Match("node", want); v != "" {
				return v, filepath.Join(dir, name)
			}
			return want, filepath.Join(dir, name)
		}
		if parent := filepath.Dir(dir); parent == dir {
			break
		}
	}
	rng, pkg := NodeEngines(start)
	if in, _ := InRange(def, rng); in || def == "" {
		return def, ""
	}
	if v := newestInRange("node", rng); v != "" {
		return v, pkg
	}
	return def, ""
}

// NodeMismatch explains when version v is outside the folder's
// package.json engines.node, else "".
func NodeMismatch(dir, v string) string {
	rng, pkg := NodeEngines(dir)
	if in, _ := InRange(v, rng); in || v == "" {
		return ""
	}
	return fmt.Sprintf("%s asks for Node %s and no installed version fits, so it runs on %s. "+
		"Install one with `laman install node \"%s\"`", pkg, rng, v, rng)
}

func newestInRange(kind, rng string) string {
	v := Installed(kind)
	for i := len(v) - 1; i >= 0; i-- {
		if in, ok := InRange(v[i], rng); in && ok {
			return v[i]
		}
	}
	return ""
}

// DefaultNode is the configured Node version, or the newest installed one.

// NodeSpec reads a version file's first line: "v20.11.0" -> "20.11.0",
// "20" -> "20". Aliases (lts/*, node, stable) return "".
func NodeSpec(s string) string {
	line := strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	line = strings.TrimPrefix(strings.TrimSpace(line), "v")
	if line == "" || strings.ContainsAny(line, "/*") || line == "node" || line == "stable" || line == "lts" {
		return ""
	}
	return line
}

func (c *Config) DefaultNode() string {
	if c.Node != "" && Match("node", c.Node) == c.Node {
		return c.Node
	}
	return Latest("node")
}

// Line is a version's "major.minor": "12.3.3" -> "12.3".
func Line(v string) string {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return v
	}
	return parts[0] + "." + parts[1]
}

// LineOf is the release line of a version for kind: PostgreSQL (10+)
// numbers majors as one part ("17.6" is line "17"); MariaDB and Redis use
// two ("12.3.3" is line "12.3").
func LineOf(kind, v string) string {
	if kind == "postgres" {
		return strings.SplitN(v, ".", 2)[0]
	}
	return Line(v)
}

// Active returns the installed version of kind ("mariadb", "redis") to run:
// the newest patch of the pinned line, or the newest install when nothing
// is pinned or the pinned line is gone.
func Active(kind, line string) string {
	if line != "" {
		if v := Match(kind, line); v != "" {
			return v
		}
	}
	return Latest(kind)
}

// PHPFor returns the PHP version a site runs on. note is non-empty when the
// site is pinned to a version that is no longer installed.
func (c *Config) PHPFor(site string) (version, note string) {
	if want, ok := c.Isolated[site]; ok {
		if Match("php", want) == want {
			return want, ""
		}
		return c.DefaultPHP(), fmt.Sprintf("%s is pinned to PHP %s, which is not installed; using %s", site, want, c.DefaultPHP())
	}
	return c.DefaultPHP(), ""
}
