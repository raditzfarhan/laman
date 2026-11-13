package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"laman/internal/config"
	"laman/internal/laragon"
)

func TestImportArgs(t *testing.T) {
	cases := []struct {
		args []string
		want importOpts
		err  string
	}{
		{args: []string{"laragon"}, want: importOpts{From: laragon.DefaultRoot}},
		{args: []string{"laragon", "--from", `D:\lg`, "--dry-run"}, want: importOpts{From: `D:\lg`, DryRun: true}},
		{args: []string{"laragon", "--from=D:\\lg", "--yes"}, want: importOpts{From: `D:\lg`, Yes: true}},
		{args: []string{"laragon", "--db", "acme,shop", "--yes"}, want: importOpts{From: laragon.DefaultRoot, Yes: true, DBs: []string{"acme", "shop"}}},
		{args: []string{"laragon", "--db=acme, shop,"}, want: importOpts{From: laragon.DefaultRoot, DBs: []string{"acme", "shop"}}},
		{args: []string{"laragon", "--no-db"}, want: importOpts{From: laragon.DefaultRoot, NoDB: true}},
		{args: []string{"laragon", "--db", "acme", "--no-db"}, err: "either"},
		{args: []string{"laragon", "--no-db", "--db=acme"}, err: "either"},
		{args: []string{"laragon", "--db"}, err: "--db"},
		{args: []string{"laragon", "--db", ""}, err: "--db"},
		{args: []string{"laragon", "--from"}, err: "--from"},
		{args: []string{"laragon", "--bogus"}, err: "--bogus"},
		{args: []string{}, err: "usage"},
		{args: []string{"xampp"}, err: "usage"},
	}
	for _, c := range cases {
		got, err := parseImportArgs(c.args)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%q: err = %v, want one mentioning %q", c.args, err, c.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.args, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: got %+v, want %+v", c.args, got, c.want)
		}
	}
}

func testPlan() laragon.Plan {
	return laragon.Plan{
		Root:    `C:\laragon`,
		Folders: []laragon.PlanFolder{{Dir: `C:\laragon\www`}},
		Links:   []laragon.PlanLink{{Name: "shop", Dir: `D:\code\shop`}, {Name: "blog", Dir: `D:\code\blog`, Taken: true}},
		Renamed: []laragon.Rename{{From: "acme.local", To: "acme.test"}},
		ByHand:  []laragon.VHost{{File: `C:\laragon\etc\apache2\sites-enabled\acme.conf`, Host: "acme.local", Extra: true}},
		Clashes: []string{`Two project folders hold "api".`},
		DBs: []laragon.PlanDB{
			{Name: "acme", Size: 12 << 20},
			{Name: "acme_backup", Size: 3 << 20, Copy: true},
			{Name: "shop", Size: 2048, Exists: true},
			{Name: "blog", Size: 0},
		},
		PHP: []laragon.SiteFit{{Site: "acme", Fit: config.PHPFit{Needs: "^8.4", From: "composer.json (require.php)", RunsOn: "8.3.10", Use: "8.4.1"}}},
	}
}

func TestDefaultDBSelection(t *testing.T) {
	got := defaultDBSelection(testPlan())
	if want := []string{"acme", "blog"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := defaultDBSelection(laragon.Plan{}); len(got) != 0 {
		t.Fatalf("empty plan: got %q", got)
	}
}

func TestChooseDBs(t *testing.T) {
	p := testPlan()
	if got, err := chooseDBs(p, importOpts{NoDB: true}); err != nil || len(got) != 0 {
		t.Errorf("--no-db: %q, %v", got, err)
	}
	if got, err := chooseDBs(p, importOpts{}); err != nil || !reflect.DeepEqual(got, []string{"acme", "blog"}) {
		t.Errorf("default: %q, %v", got, err)
	}
	// A copy may be picked by name; names match the plan's spelling.
	if got, err := chooseDBs(p, importOpts{DBs: []string{"ACME_backup"}}); err != nil || !reflect.DeepEqual(got, []string{"acme_backup"}) {
		t.Errorf("--db acme_backup: %q, %v", got, err)
	}
	if _, err := chooseDBs(p, importOpts{DBs: []string{"shop"}}); err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("--db shop (exists in laman): err = %v", err)
	}
	if _, err := chooseDBs(p, importOpts{DBs: []string{"acme", "nope"}}); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("--db nope: err = %v", err)
	}
	if got, err := chooseDBs(p, importOpts{DBs: []string{"acme", "acme"}}); err != nil || !reflect.DeepEqual(got, []string{"acme"}) {
		t.Errorf("--db acme,acme: %q, %v", got, err)
	}
}

func TestPrintPlan(t *testing.T) {
	p := testPlan()
	p.Blocked = []string{"httpd.exe", "mysqld.exe"}
	var b bytes.Buffer
	err := printPlan(&b, p, defaultDBSelection(p))
	if err == nil {
		t.Fatal("blocked plan: want an error")
	}
	if !strings.Contains(b.String(), "Quit Laragon (or click Stop All) first: httpd.exe, mysqld.exe") {
		t.Fatalf("blocked plan output:\n%s", b.String())
	}

	p.Blocked = nil
	b.Reset()
	if err := printPlan(&b, p, []string{"acme"}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		`C:\laragon\www`,
		"link shop", `D:\code\shop`,
		`blog`, // taken link is reported
		"acme.local", "acme.test",
		`acme.conf`,
		`Two project folders hold "api".`,
		"[x] acme (12 MB)",
		"[ ] acme_backup (3 MB)",
		"shop (2 KB)", "already in laman",
		"[ ] blog",
		"laman isolate 8.4.1 acme",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plan output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Quit Laragon") {
		t.Errorf("unblocked plan mentions the blocker:\n%s", out)
	}
}

func TestPHPFitCommand(t *testing.T) {
	cases := []struct {
		fit  config.PHPFit
		want string
	}{
		{config.PHPFit{Use: "8.4.1"}, "laman isolate 8.4.1 acme"},
		{config.PHPFit{Install: "8.2"}, "laman install php 8.2, then laman isolate 8.2 acme"},
		{config.PHPFit{Needs: "^5.6"}, ""},
	}
	for _, c := range cases {
		if got := phpFitCommand(laragon.SiteFit{Site: "acme", Fit: c.fit}); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.fit, got, c.want)
		}
	}
}

// Ruling E: ErrPassword asks for the password once and retries once with it.
func TestOpenSourcePromptsOnce(t *testing.T) {
	var tried []string
	prompts := 0
	start := func(pw string) (string, error) {
		tried = append(tried, pw)
		if pw != "s3cret" {
			return "", laragon.ErrPassword
		}
		return "up", nil
	}
	prompt := func() (string, error) { prompts++; return "s3cret", nil }
	got, err := openSource(start, "", prompt)
	if err != nil || got != "up" {
		t.Fatalf("got %q, %v", got, err)
	}
	if prompts != 1 || !reflect.DeepEqual(tried, []string{"", "s3cret"}) {
		t.Fatalf("prompts = %d, tried %q", prompts, tried)
	}

	// A wrong answer is not asked for again.
	tried, prompts = nil, 0
	wrong := func() (string, error) { prompts++; return "nope", nil }
	if _, err := openSource(start, "", wrong); !errors.Is(err, laragon.ErrPassword) {
		t.Fatalf("wrong password: err = %v", err)
	}
	if prompts != 1 || len(tried) != 2 {
		t.Fatalf("wrong password: prompts = %d, tried %q", prompts, tried)
	}

	// No prompt available (no terminal, or LAMAN_LARAGON_PASSWORD set).
	tried, prompts = nil, 0
	if _, err := openSource(start, "env-pw", nil); !errors.Is(err, laragon.ErrPassword) {
		t.Fatalf("no prompt: err = %v", err)
	}
	if !reflect.DeepEqual(tried, []string{"env-pw"}) {
		t.Fatalf("no prompt: tried %q", tried)
	}

	// Other errors never prompt.
	prompts = 0
	boom := func(string) (string, error) { return "", errors.New("boom") }
	if _, err := openSource(boom, "", prompt); err == nil || prompts != 0 {
		t.Fatalf("other error: err = %v, prompts = %d", err, prompts)
	}
}

func TestApplyProjects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LAMAN_HOME", filepath.Join(home, ".laman"))
	www := filepath.Join(home, "laragon", "www")
	shop := filepath.Join(home, "code", "shop")
	for _, d := range []string{www, shop} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{SitesDir: filepath.Join(home, "Sites"), TLD: "test", Links: map[string]string{"api": `D:\mine\api`}}
	p := laragon.Plan{
		Folders: []laragon.PlanFolder{{Dir: www}, {Dir: filepath.Join(home, "Sites"), Already: true}, {Dir: filepath.Join(home, "gone")}},
		Links: []laragon.PlanLink{
			{Name: "shop", Dir: shop},
			{Name: "blog", Dir: `D:\code\blog`, Taken: true},
			{Name: "api", Dir: `D:\code\api`}, // laman got one meanwhile: never overwritten
			{Name: "lost", Dir: filepath.Join(home, "code", "lost")},
		},
	}
	var res importResult
	var log bytes.Buffer
	if changed := applyProjects(cfg, p, &res, &log); !changed {
		t.Fatal("want changed")
	}
	if !reflect.DeepEqual(res.Folders, []string{www}) {
		t.Errorf("Folders = %q", res.Folders)
	}
	if !reflect.DeepEqual(res.Links, []string{"shop"}) {
		t.Errorf("Links = %q", res.Links)
	}
	if !reflect.DeepEqual(cfg.MoreSitesDirs, []string{www}) {
		t.Errorf("MoreSitesDirs = %q", cfg.MoreSitesDirs)
	}
	if cfg.Links["shop"] != shop || cfg.Links["api"] != `D:\mine\api` || cfg.Links["blog"] != "" {
		t.Errorf("Links = %v", cfg.Links)
	}
	if res.Failed["folder:"+filepath.Join(home, "gone")] == "" || res.Failed["link:lost"] == "" || len(res.Failed) != 2 {
		t.Errorf("Failed = %v", res.Failed)
	}
}

// A change saved (by the dashboard) between plan and apply survives the
// import's save.
func TestApplyAndSaveKeepsNewerConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LAMAN_HOME", filepath.Join(home, ".laman"))
	www := filepath.Join(home, "laragon", "www")
	if err := os.MkdirAll(www, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.SitesDir = filepath.Join(home, "Sites")
	cfg.ControlPort = 1 // no daemon to notify
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(); err != nil { // what the plan was built from
		t.Fatal(err)
	}
	p := laragon.Plan{Folders: []laragon.PlanFolder{{Dir: www}}}

	// Meanwhile the dashboard links a site and picks a theme.
	later, _ := config.Load()
	later.Links["mine"] = `D:\mine`
	later.Theme = "dark"
	if err := later.Save(); err != nil {
		t.Fatal(err)
	}

	res := importResult{Failed: map[string]string{}}
	if _, err := applyAndSave(p, &res, io.Discard); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Links["mine"] != `D:\mine` || got.Theme != "dark" {
		t.Errorf("dashboard change lost: links %v, theme %q", got.Links, got.Theme)
	}
	if len(got.MoreSitesDirs) != 1 || got.MoreSitesDirs[0] != www {
		t.Errorf("MoreSitesDirs = %q", got.MoreSitesDirs)
	}
	if len(res.Failed) != 0 || !reflect.DeepEqual(res.Folders, []string{www}) {
		t.Errorf("res = %+v", res)
	}
}

// A failed save is a reported failure, not a fatal error, and nothing is
// claimed as added.
func TestApplyAndSaveReportsSaveFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LAMAN_HOME", filepath.Join(home, ".laman"))
	www := filepath.Join(home, "laragon", "www")
	if err := os.MkdirAll(www, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.SitesDir = filepath.Join(home, "Sites")
	cfg.ControlPort = 1
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	// A folder where the temp file goes makes Save fail.
	if err := os.MkdirAll(filepath.Join(config.Path()+".tmp", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	res := importResult{Failed: map[string]string{}}
	if _, err := applyAndSave(laragon.Plan{Folders: []laragon.PlanFolder{{Dir: www}}}, &res, io.Discard); err != nil {
		t.Fatal(err)
	}
	if res.Failed["config:"+config.Path()] == "" || len(res.Folders) != 0 {
		t.Errorf("res = %+v", res)
	}
}

func TestImportOutcome(t *testing.T) {
	if err := importOutcome(importResult{}, false); err != nil {
		t.Errorf("clean: %v", err)
	}
	pw := importResult{NeedsPassword: true, Failed: map[string]string{"db:acme": laragon.ErrPassword.Error(), "folder:x": "gone"}}
	err := importOutcome(pw, false)
	if !errors.Is(err, laragon.ErrPassword) || err.Error() != "Laragon's MySQL asked for a password" {
		t.Errorf("password: %v", err)
	}
	if err := importOutcome(pw, true); err == nil || errors.Is(err, laragon.ErrPassword) {
		t.Errorf("cancelled: %v", err)
	}
	if err := importOutcome(importResult{Failed: map[string]string{"db:acme": "boom"}}, false); err == nil {
		t.Error("failed item: want an error")
	}
}

func TestSourceFailed(t *testing.T) {
	res := importResult{Failed: map[string]string{}}
	sourceFailed(&res, []string{"acme", "shop"}, laragon.ErrPassword, io.Discard)
	if !res.NeedsPassword || res.Failed["db:acme"] == "" || res.Failed["db:shop"] == "" {
		t.Errorf("password: %+v", res)
	}
	res = importResult{Failed: map[string]string{}}
	sourceFailed(&res, []string{"acme"}, errors.New("no MySQL"), io.Discard)
	if res.NeedsPassword || res.Failed["db:acme"] != "no MySQL" {
		t.Errorf("other: %+v", res)
	}
}

func TestImportResultJSON(t *testing.T) {
	b, err := json.Marshal(importResult{NeedsPassword: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"needsPassword":true`, `"folders"`, `"links"`, `"copied"`, `"skipped"`,
		`"failed"`, `"renamed"`, `"byHand"`, `"php"`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("JSON lacks %s: %s", k, b)
		}
	}
	// Nested: renamed and php entries are lower camelCase too.
	b, _ = json.Marshal(importResult{Renamed: []laragon.Rename{{From: "a.test", To: "b.test"}},
		PHP: []laragon.SiteFit{{Site: "acme", Fit: config.PHPFit{Needs: "^7.4", RunsOn: "8.5.1"}}}})
	for _, k := range []string{`{"from":"a.test","to":"b.test"}`, `"site":"acme"`, `"fit":{"needs":"^7.4","from":"","runsOn":"8.5.1","use":"","install":""}`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("JSON lacks %s: %s", k, b)
		}
	}
}

func TestTakePasswordUnsetsEnv(t *testing.T) {
	t.Setenv(passwordEnv, "s3cret")
	var o importOpts
	takePassword(&o)
	if o.Password != "s3cret" || !o.HasPassword {
		t.Errorf("opts = %+v", o)
	}
	if _, ok := os.LookupEnv(passwordEnv); ok {
		t.Error("the password is still in the environment")
	}
	o = importOpts{}
	takePassword(&o)
	if o.HasPassword {
		t.Error("HasPassword without the variable")
	}
}

// A run with nothing left to import still stops Home offering it.
func TestRunImportNothingToImportSetsNotice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LAMAN_HOME", filepath.Join(home, ".laman"))
	root := filepath.Join(home, "laragon")
	www := filepath.Join(root, "www")
	if err := os.MkdirAll(www, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "laragon.exe"), nil, 0o644)
	cfg := config.Default()
	cfg.SitesDir = filepath.Join(home, "Sites")
	cfg.MoreSitesDirs = []string{www}
	cfg.MySQLPort = 1 // laman's MariaDB doesn't answer; Laragon has no databases anyway
	cfg.ControlPort = 1
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runImport(importOpts{From: root, Yes: true}, &out); err != nil {
		t.Fatalf("runImport: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "Nothing to import.") {
		t.Fatalf("output:\n%s", out.String())
	}
	got, err := config.Load()
	if err != nil || got.LaragonNotice != "imported" {
		t.Errorf("notice = %q, %v", got.LaragonNotice, err)
	}
}

func TestPrintPlanAndSummaryNotes(t *testing.T) {
	p := testPlan()
	p.Notes = []string{"acme may be incomplete from an interrupted import — drop it in laman and import again."}
	var b bytes.Buffer
	if err := printPlan(&b, p, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "Note: acme may be incomplete") {
		t.Errorf("plan output lacks the note:\n%s", b.String())
	}
	b.Reset()
	printSummary(&b, importResult{Notes: p.Notes})
	if !strings.Contains(b.String(), "Note: acme may be incomplete") {
		t.Errorf("summary lacks the note:\n%s", b.String())
	}
}

// A database an earlier run was copying when it was killed is reported
// (never dropped) and the record is cleared by a real run, not a dry run.
func TestRunImportReportsInterruptedCopy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LAMAN_HOME", filepath.Join(home, ".laman"))
	root := filepath.Join(home, "laragon")
	www := filepath.Join(root, "www")
	os.MkdirAll(www, 0o755)
	os.WriteFile(filepath.Join(root, "laragon.exe"), nil, 0o644)
	cfg := config.Default()
	cfg.SitesDir = filepath.Join(home, "Sites")
	cfg.MoreSitesDirs = []string{www}
	cfg.ControlPort = 1
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	old := listLamanDBs
	listLamanDBs = func(*config.Config) ([]string, error) { return []string{"mysql", "acme"}, nil }
	t.Cleanup(func() { listLamanDBs = old })
	if err := laragon.MarkCopying("acme"); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(config.LogDir(), "import-laragon.copying")

	var out bytes.Buffer
	if err := runImport(importOpts{From: root, DryRun: true}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "acme may be incomplete from an interrupted import") {
		t.Errorf("dry run lacks the note:\n%s", out.String())
	}
	if _, err := os.Stat(record); err != nil {
		t.Errorf("dry run removed the record: %v", err)
	}
	out.Reset()
	if err := runImport(importOpts{From: root, Yes: true}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "acme may be incomplete from an interrupted import") {
		t.Errorf("run lacks the note:\n%s", out.String())
	}
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Errorf("record kept after a run: %v", err)
	}
}
