package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"laman/internal/config"
	"laman/internal/install"
	"laman/internal/laragon"
)

// importOpts are the flags of `laman import laragon`. DBs is non-nil when
// --db named the databases to copy.
type importOpts struct {
	From        string
	DryRun, Yes bool
	NoDB        bool
	DBs         []string
	// Password is Laragon's MySQL root password from LAMAN_LARAGON_PASSWORD;
	// HasPassword when that was set (the CLI then never prompts).
	Password    string
	HasPassword bool
}

// importResult is what an import did; it is written to
// logs/import-laragon.json for the dashboard. Failed maps "db:<name>",
// "folder:<path>", "link:<name>" or "config:<path>" to the reason.
// NeedsPassword: Laragon's MySQL refused the password (none could be
// asked for), so no database was copied.
type importResult struct {
	Folders       []string          `json:"folders"`
	Links         []string          `json:"links"`
	Copied        []string          `json:"copied"`
	Skipped       []string          `json:"skipped"`
	Failed        map[string]string `json:"failed"`
	Renamed       []laragon.Rename  `json:"renamed"`
	ByHand        []string          `json:"byHand"`
	PHP           []laragon.SiteFit `json:"php"`
	Notes         []string          `json:"notes"`
	NeedsPassword bool              `json:"needsPassword"`
}

// Keys of importResult.Failed.
func failDB(name string) string     { return "db:" + name }
func failFolder(dir string) string  { return "folder:" + dir }
func failLink(name string) string   { return "link:" + name }
func failConfig(path string) string { return "config:" + path }

const (
	importLogName  = "import-laragon.log"
	importJSONName = "import-laragon.json"
	// passwordEnv carries Laragon's MySQL root password (the dashboard sets
	// it); when set, the CLI never prompts.
	passwordEnv = "LAMAN_LARAGON_PASSWORD"
)

func parseImportArgs(args []string) (importOpts, error) {
	const use = "usage: laman import laragon [--from <dir>] [--dry-run] [--yes] [--db a,b | --no-db]"
	if len(args) == 0 || args[0] != "laragon" {
		return importOpts{}, errors.New(use)
	}
	o := importOpts{From: laragon.DefaultRoot}
	rest := args[1:]
	value := func(i *int, flag, arg string) (string, error) {
		if v, ok := strings.CutPrefix(arg, flag+"="); ok {
			return v, nil
		}
		if *i+1 >= len(rest) {
			return "", fmt.Errorf("%s needs a value", flag)
		}
		*i++
		return rest[*i], nil
	}
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case a == "--dry-run":
			o.DryRun = true
		case a == "--yes" || a == "-y":
			o.Yes = true
		case a == "--no-db":
			o.NoDB = true
		case a == "--from" || strings.HasPrefix(a, "--from="):
			v, err := value(&i, "--from", a)
			if err != nil {
				return o, err
			}
			if strings.TrimSpace(v) == "" {
				return o, errors.New("--from needs a folder")
			}
			o.From = v
		case a == "--db" || strings.HasPrefix(a, "--db="):
			v, err := value(&i, "--db", a)
			if err != nil {
				return o, err
			}
			o.DBs = []string{}
			for _, n := range strings.Split(v, ",") {
				if n = strings.TrimSpace(n); n != "" {
					o.DBs = append(o.DBs, n)
				}
			}
			if len(o.DBs) == 0 {
				return o, errors.New("--db needs database names, e.g. --db acme,shop")
			}
		default:
			return o, fmt.Errorf("unknown option %s; %s", a, use)
		}
	}
	if o.NoDB && o.DBs != nil {
		return o, errors.New("use either --db or --no-db, not both")
	}
	return o, nil
}

// defaultDBSelection is the databases copied when --db isn't given: all
// that laman doesn't have yet, except ones that look like backups.
func defaultDBSelection(p laragon.Plan) []string {
	var out []string
	for _, d := range p.DBs {
		if !d.Exists && !d.Copy {
			out = append(out, d.Name)
		}
	}
	return out
}

// chooseDBs applies --no-db / --db to the plan. --db names must be Laragon
// databases laman doesn't have yet.
func chooseDBs(p laragon.Plan, o importOpts) ([]string, error) {
	if o.NoDB {
		return nil, nil
	}
	if o.DBs == nil {
		return defaultDBSelection(p), nil
	}
	var out []string
	seen := map[string]bool{}
	for _, n := range o.DBs {
		var hit *laragon.PlanDB
		for i := range p.DBs {
			if strings.EqualFold(p.DBs[i].Name, n) {
				hit = &p.DBs[i]
			}
		}
		switch {
		case hit == nil:
			return nil, fmt.Errorf("Laragon has no database %s", n)
		case hit.Exists:
			return nil, fmt.Errorf("laman already has a database %s; it is never replaced", hit.Name)
		case !seen[hit.Name]:
			seen[hit.Name] = true
			out = append(out, hit.Name)
		}
	}
	return out, nil
}

// errBlocked: Laragon's servers are running, so nothing was changed.
var errBlocked = errors.New("Laragon is running; nothing was changed")

// printPlan writes the plan; selected are the databases that would be
// copied. It returns errBlocked when Laragon's servers are running.
func printPlan(w io.Writer, p laragon.Plan, selected []string) error {
	if len(p.Blocked) > 0 {
		fmt.Fprintf(w, "Quit Laragon (or click Stop All) first: %s\n\n", strings.Join(p.Blocked, ", "))
	}
	fmt.Fprintf(w, "Import from Laragon at %s\n", p.Root)

	fmt.Fprintln(w, "\nProject folders (every folder inside is a site):")
	for _, f := range p.Folders {
		if f.Already {
			fmt.Fprintf(w, "  %s  (already in laman)\n", f.Dir)
		} else {
			fmt.Fprintf(w, "  add %s\n", f.Dir)
		}
	}
	if len(p.Links) > 0 {
		fmt.Fprintln(w, "\nSites outside www:")
		for _, l := range p.Links {
			if l.Taken {
				fmt.Fprintf(w, "  skip %s (%s): laman already serves a site called %s; link it by hand under another name\n", l.Name, l.Dir, l.Name)
			} else {
				fmt.Fprintf(w, "  link %s -> %s\n", l.Name, l.Dir)
			}
		}
	}
	if len(p.Renamed) > 0 {
		fmt.Fprintln(w, "\nAddresses that change:")
		for _, r := range p.Renamed {
			fmt.Fprintf(w, "  %s -> %s\n", r.From, r.To)
		}
	}
	if len(p.ByHand) > 0 {
		fmt.Fprintln(w, "\nCheck by hand (settings laman can't carry over):")
		for _, v := range p.ByHand {
			fmt.Fprintf(w, "  %s\n", byHandLine(v))
		}
	}
	if len(p.Clashes) > 0 {
		fmt.Fprintln(w, "\nName clashes:")
		for _, c := range p.Clashes {
			fmt.Fprintf(w, "  %s\n", c)
		}
	}
	fmt.Fprintln(w, "\nDatabases (Laragon's are read, not changed):")
	if len(p.DBs) == 0 {
		fmt.Fprintln(w, "  none")
	}
	pick := map[string]bool{}
	for _, n := range selected {
		pick[n] = true
	}
	for _, d := range p.DBs {
		switch {
		case d.Exists:
			fmt.Fprintf(w, "      %s (%s)  already in laman, skipped\n", d.Name, sizeLabel(d.Size))
		case pick[d.Name]:
			fmt.Fprintf(w, "  [x] %s (%s)\n", d.Name, sizeLabel(d.Size))
		case d.Copy:
			fmt.Fprintf(w, "  [ ] %s (%s)  looks like a copy\n", d.Name, sizeLabel(d.Size))
		default:
			fmt.Fprintf(w, "  [ ] %s (%s)\n", d.Name, sizeLabel(d.Size))
		}
	}
	for _, n := range p.Notes {
		fmt.Fprintf(w, "  Note: %s\n", n)
	}
	if len(p.PHP) > 0 {
		fmt.Fprintln(w, "\nPHP (suggestions; nothing is changed):")
		for _, f := range p.PHP {
			fmt.Fprintf(w, "  %s\n", phpFitLine(f))
		}
	}
	if len(p.Blocked) > 0 {
		return errBlocked
	}
	return nil
}

func byHandLine(v laragon.VHost) string {
	return fmt.Sprintf("%s  (%s)", v.Host, v.File)
}

func phpFitLine(f laragon.SiteFit) string {
	s := fmt.Sprintf("%s: %s asks for PHP %s; it would run on %s.", f.Site, f.Fit.From, f.Fit.Needs, f.Fit.RunsOn)
	if c := phpFitCommand(f); c != "" {
		return s + " Run: " + c
	}
	return s + " No installed PHP fits; install one that does (laman install php <version>)."
}

// phpFitCommand is the command that gives a site a PHP that fits it.
func phpFitCommand(f laragon.SiteFit) string {
	switch {
	case f.Fit.Use != "":
		return fmt.Sprintf("laman isolate %s %s", f.Fit.Use, f.Site)
	case f.Fit.Install != "":
		return fmt.Sprintf("laman install php %s, then laman isolate %s %s", f.Fit.Install, f.Fit.Install, f.Site)
	}
	return ""
}

func sizeLabel(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%d MB", (n+1<<19)>>20)
	}
	return fmt.Sprintf("%d KB", (n+1023)>>10)
}

// openSource starts Laragon's MySQL with password. When it is refused and
// prompt is not nil, it asks once and tries once more with the answer.
func openSource[T any](start func(password string) (T, error), password string, prompt func() (string, error)) (T, error) {
	s, err := start(password)
	if !errors.Is(err, laragon.ErrPassword) || prompt == nil {
		return s, err
	}
	pw, perr := prompt()
	if perr != nil {
		return s, perr
	}
	return start(pw)
}

// applyProjects adds Laragon's www as a project folder and links the sites
// outside it. Failures are recorded in res; an existing link is never
// replaced. It reports whether cfg changed (it is not saved).
func applyProjects(cfg *config.Config, p laragon.Plan, res *importResult, log io.Writer) bool {
	if res.Failed == nil {
		res.Failed = map[string]string{}
	}
	changed := false
	for _, f := range p.Folders {
		if f.Already {
			continue
		}
		if _, err := cfg.AddSitesDir(f.Dir); err != nil {
			res.Failed[failFolder(f.Dir)] = err.Error()
			fmt.Fprintf(log, "Could not add the project folder %s: %v\n", f.Dir, err)
			continue
		}
		res.Folders = append(res.Folders, f.Dir)
		changed = true
		fmt.Fprintf(log, "Added the project folder %s\n", f.Dir)
	}
	for _, l := range p.Links {
		if l.Taken {
			continue
		}
		if _, ok := cfg.Links[l.Name]; ok {
			fmt.Fprintf(log, "Skipped the link %s: laman already has one by that name\n", l.Name)
			continue
		}
		if fi, err := os.Stat(l.Dir); err != nil || !fi.IsDir() {
			res.Failed[failLink(l.Name)] = fmt.Sprintf("%s is not a folder", l.Dir)
			fmt.Fprintf(log, "Could not link %s: %s is not a folder\n", l.Name, l.Dir)
			continue
		}
		if cfg.Links == nil {
			cfg.Links = map[string]string{}
		}
		cfg.Links[l.Name] = l.Dir
		res.Links = append(res.Links, l.Name)
		changed = true
		fmt.Fprintf(log, "Linked %s -> %s\n", l.Name, l.Dir)
	}
	return changed
}

// applyAndSave adds the plan's folders and links to the config as it is
// on disk now (the dashboard may have changed it since the plan was made),
// saves it and tells the daemon. A failed save is recorded in res and
// nothing is then reported as added. It returns the config it used.
func applyAndSave(p laragon.Plan, res *importResult, log io.Writer) (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if !applyProjects(cfg, p, res, log) {
		return cfg, nil
	}
	if err := cfg.Save(); err != nil {
		res.Failed[failConfig(config.Path())] = err.Error()
		fmt.Fprintf(log, "Could not save the config, so no folder or link was added: %v\n", err)
		res.Folders, res.Links = []string{}, []string{}
		return cfg, nil
	}
	if err := notifyDaemonErr(); err != nil {
		fmt.Fprintln(log, err)
	}
	return cfg, nil
}

// importOutcome is the command's error after an apply: cancelled, Laragon's
// MySQL wanting a password (laragon.ErrPassword itself, so the last line
// reads "laman: Laragon's MySQL asked for a password"), or failed items.
func importOutcome(res importResult, cancelled bool) error {
	switch {
	case cancelled:
		return errors.New("import cancelled")
	case res.NeedsPassword:
		return laragon.ErrPassword
	case len(res.Failed) > 0:
		return fmt.Errorf("%d of the import's steps failed (see above)", len(res.Failed))
	}
	return nil
}

// sourceFailed records that Laragon's MySQL could not be started: every
// selected database fails with err.
func sourceFailed(res *importResult, selected []string, err error, log io.Writer) {
	if errors.Is(err, laragon.ErrPassword) {
		res.NeedsPassword = true
	}
	for _, n := range selected {
		res.Failed[failDB(n)] = err.Error()
	}
	fmt.Fprintf(log, "Databases not copied: %v\n", err)
}

// lamanDatabases lists laman's MariaDB databases; an error means it does
// not answer.
func lamanDatabases(cfg *config.Config) ([]string, error) {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(cfg.MySQLPort), 2*time.Second)
	if err != nil {
		return nil, err
	}
	conn.Close()
	out, err := dbSQL(cfg, "mariadb", "SHOW DATABASES")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, n := range strings.Split(out, "\n") {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	return names, nil
}

// listLamanDBs is lamanDatabases; tests replace it.
var listLamanDBs = lamanDatabases

const mariaDBDown = "laman's MariaDB is not running; start laman (laman start) and turn MariaDB on (laman enable mariadb)"

// lenientWriter writes to every writer, ignoring their errors (stdout may
// be gone when the dashboard runs the import).
type lenientWriter []io.Writer

func (m lenientWriter) Write(p []byte) (int, error) {
	for _, w := range m {
		w.Write(p)
	}
	return len(p), nil
}

// takePassword reads Laragon's MySQL password from the environment and
// removes it there, so no child process (mysqld, the MariaDB client, the
// daemon notification) inherits it.
func takePassword(o *importOpts) {
	o.Password, o.HasPassword = os.LookupEnv(passwordEnv)
	os.Unsetenv(passwordEnv)
}

func cmdImport(args []string) error {
	o, err := parseImportArgs(args)
	if err != nil {
		return err
	}
	takePassword(&o) // before any child process starts
	if o.DryRun {
		return runImport(o, os.Stdout)
	}
	if err := os.MkdirAll(config.LogDir(), 0o755); err != nil {
		return err
	}
	logf, err := os.Create(filepath.Join(config.LogDir(), importLogName))
	if err != nil {
		return err
	}
	defer logf.Close()
	os.Remove(filepath.Join(config.LogDir(), importJSONName))
	err = runImport(o, lenientWriter{os.Stdout, logf})
	if err != nil {
		fmt.Fprintln(logf, "laman:", err)
	}
	return err
}

// runImport is `laman import laragon`; progress goes to out.
func runImport(o importOpts, out io.Writer) error {
	l, err := laragon.Find(o.From)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if l.MariaDB && o.DBs != nil {
		return laragon.ErrMariaDB
	}
	lamanDBs, dbErr := listLamanDBs(cfg)
	p := laragon.BuildPlan(l, cfg, lamanDBs, l.RunningServers())
	if dbErr == nil {
		laragon.NoteInterrupted(&p, lamanDBs)
		if !o.DryRun {
			laragon.ClearCopying() // reported once; laman's databases were checked
		}
	}
	selected, err := chooseDBs(p, o)
	if err != nil {
		return err
	}
	if err := printPlan(out, p, selected); err != nil {
		return err
	}
	if dbErr != nil && len(selected) > 0 {
		fmt.Fprintln(out, "\nlaman's MariaDB is not answering, so databases it already has can't be told apart yet.")
	}
	if o.DryRun {
		fmt.Fprintln(out, "\nDry run: nothing was changed.")
		return nil
	}
	todo := len(selected) > 0
	for _, f := range p.Folders {
		todo = todo || !f.Already
	}
	for _, k := range p.Links {
		todo = todo || !k.Taken
	}
	if !todo {
		fmt.Fprintln(out, "\nNothing to import.")
		// Home stops offering an import that has nothing left to do.
		if err := setNoticeImported(); err != nil {
			fmt.Fprintf(out, "Could not save the config: %v\n", err)
		}
		return nil
	}
	if !o.Yes {
		fmt.Print("\nImport now? [y/N] ")
		answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			fmt.Fprintln(out, "Nothing changed.")
			return nil
		}
	}
	if len(selected) > 0 && dbErr != nil {
		return errors.New(mariaDBDown)
	}

	res := importResult{Folders: []string{}, Links: []string{}, Copied: []string{}, Skipped: []string{},
		Failed: map[string]string{}, Renamed: append([]laragon.Rename{}, p.Renamed...), ByHand: []string{},
		PHP: append([]laragon.SiteFit{}, p.PHP...), Notes: append([]string{}, p.Notes...)}
	for _, d := range p.DBs {
		if d.Exists {
			res.Skipped = append(res.Skipped, d.Name)
		}
	}
	for _, v := range p.ByHand {
		res.ByHand = append(res.ByHand, byHandLine(v))
	}
	fmt.Fprintln(out)
	cfg, err = applyAndSave(p, &res, out)
	if err != nil {
		return err
	}
	cancelled := copyDatabases(l, cfg, p, selected, o, &res, out)
	if !cancelled {
		if err := setNoticeImported(); err != nil {
			res.Failed[failConfig(config.Path())] = err.Error()
			fmt.Fprintf(out, "Could not save the config: %v\n", err)
		}
	}
	if err := writeImportResult(res); err != nil {
		fmt.Fprintf(out, "Could not write the import result: %v\n", err)
	}
	printSummary(out, res)
	return importOutcome(res, cancelled)
}

// setNoticeImported records that the import ran, so Home stops offering
// it. It reloads the config: the dashboard may have changed it meanwhile.
func setNoticeImported() error {
	c, err := config.Load()
	if err == nil {
		c.LaragonNotice = "imported"
		err = c.Save()
	}
	return err
}

// copyDatabases copies selected from Laragon's MySQL into laman's MariaDB.
// Laragon's mysqld is stopped on every path, Ctrl+C included. It reports
// whether the user cancelled.
func copyDatabases(l *laragon.Laragon, cfg *config.Config, p laragon.Plan, selected []string, o importOpts, res *importResult, out io.Writer) (cancelled bool) {
	if len(selected) == 0 {
		return false
	}
	failAll := func(reason string) { sourceFailed(res, selected, errors.New(reason), out) }
	if running := l.RunningServers(); len(running) > 0 {
		failAll("Laragon was started again (" + strings.Join(running, ", ") + "); quit it and import the databases again")
		return false
	}
	client := install.MariaDBBin(install.MariaDBInstallDir(config.Active("mariadb", cfg.MariaDB)), "mariadb", "mysql")
	if client == "" {
		failAll("the MariaDB client is missing")
		return false
	}
	target := laragon.Target{Client: client, Port: cfg.MySQLPort}

	// Ctrl+C: stop Laragon's mysqld cleanly (the copy running then fails
	// and drops what it created) instead of dying with it up.
	var mu sync.Mutex
	var src *laragon.Source
	interrupted := false
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	done := make(chan struct{})
	defer func() { signal.Stop(sig); close(done) }()
	go func() {
		for {
			select {
			case <-sig:
				mu.Lock()
				first := !interrupted
				interrupted = true
				s := src
				mu.Unlock()
				if first {
					fmt.Fprintln(out, "Stopping Laragon's MySQL safely…")
				}
				if s != nil {
					s.Stop()
				}
			case <-done:
				return
			}
		}
	}()
	isInterrupted := func() bool { mu.Lock(); defer mu.Unlock(); return interrupted }

	password := o.Password
	var prompt func() (string, error)
	if !o.HasPassword && term.IsTerminal(int(os.Stdin.Fd())) {
		prompt = func() (string, error) {
			fmt.Print("Laragon's MySQL asks for root's password: ")
			b, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Println()
			if err == nil && isInterrupted() {
				err = errors.New("cancelled")
			}
			return string(b), err
		}
	}
	fmt.Fprintln(out, "Starting Laragon's MySQL (its data is read, not changed)…")
	s, err := openSource(func(pw string) (*laragon.Source, error) { return laragon.StartSource(l, pw) }, password, prompt)
	if err != nil {
		if isInterrupted() {
			failAll("cancelled")
			return true
		}
		sourceFailed(res, selected, err, out)
		return false
	}
	mu.Lock()
	src = s
	mu.Unlock()
	defer func() {
		fmt.Fprintln(out, "Stopping Laragon's MySQL…")
		if err := s.Stop(); err != nil {
			fmt.Fprintf(out, "Stopping Laragon's MySQL: %v\n", err)
		}
	}()

	size := map[string]int64{}
	for _, d := range p.DBs {
		size[d.Name] = d.Size
	}
	for _, n := range selected {
		if isInterrupted() {
			res.Failed[failDB(n)] = "cancelled"
			continue
		}
		fmt.Fprintf(out, "Copying %s (%s)…\n", n, sizeLabel(size[n]))
		// Should laman kill this run mid-copy, the next plan says n may be incomplete.
		if err := laragon.MarkCopying(n); err != nil {
			fmt.Fprintf(out, "  could not record the copy in progress: %v\n", err)
		}
		err := laragon.Copy(s, target, n, out)
		laragon.ClearCopying()
		if err != nil {
			if isInterrupted() {
				res.Failed[failDB(n)] = "cancelled"
			} else {
				res.Failed[failDB(n)] = err.Error()
			}
			fmt.Fprintf(out, "  failed: %s\n", res.Failed[failDB(n)])
			continue
		}
		res.Copied = append(res.Copied, n)
		fmt.Fprintln(out, "  done")
	}
	return isInterrupted()
}

func writeImportResult(res importResult) error {
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(config.LogDir(), importJSONName)
	if err := os.WriteFile(path+".tmp", append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

func printSummary(w io.Writer, res importResult) {
	fmt.Fprintln(w, "\nSummary:")
	list := func(label string, items []string) {
		if len(items) > 0 {
			fmt.Fprintf(w, "  %s: %s\n", label, strings.Join(items, ", "))
		}
	}
	list("Project folders added", res.Folders)
	list("Linked", res.Links)
	list("Databases copied", res.Copied)
	list("Databases skipped (already in laman)", res.Skipped)
	if len(res.Failed) > 0 {
		fmt.Fprintln(w, "  Failed:")
		for _, k := range sortedKeys(res.Failed) {
			fmt.Fprintf(w, "    %s: %s\n", k, res.Failed[k])
		}
	}
	if len(res.Renamed) > 0 {
		fmt.Fprintln(w, "  New addresses:")
		for _, r := range res.Renamed {
			fmt.Fprintf(w, "    %s is now %s\n", r.From, r.To)
		}
	}
	if len(res.ByHand) > 0 {
		fmt.Fprintln(w, "  Check by hand:")
		for _, b := range res.ByHand {
			fmt.Fprintf(w, "    %s\n", b)
		}
	}
	for _, n := range res.Notes {
		fmt.Fprintf(w, "  Note: %s\n", n)
	}
	if len(res.PHP) > 0 {
		fmt.Fprintln(w, "  PHP:")
		for _, f := range res.PHP {
			fmt.Fprintf(w, "    %s\n", phpFitLine(f))
		}
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
