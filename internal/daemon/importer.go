package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"laman/internal/config"
	"laman/internal/install"
	"laman/internal/laragon"
)

// The dashboard's "Import from Laragon" runs `laman import laragon` as the
// background job, so it does exactly what the command does. The plan shown
// before the run is built here with the same laragon.BuildPlan the command
// uses.

// importPlan is laragon.Plan plus whether laman's MariaDB answered. When it
// did not, DBs[].Exists could not be told and is false.
type importPlan struct {
	laragon.Plan
	MariaDBOff bool `json:"mariadbOff"`
}

// Seams for tests.
var (
	lamanDatabases = lamanDBNames
	runImportJob   = func(d *daemon, args, env []string) error {
		return d.runJobEnv("Importing from Laragon", args, env, func() { d.refresh() })
	}
)

// lamanDBNames lists the databases of laman's MariaDB through its client;
// an error means it does not answer.
func lamanDBNames(cfg *config.Config) ([]string, error) {
	cli := install.MariaDBBin(install.MariaDBInstallDir(config.Active("mariadb", cfg.MariaDB)), "mariadb", "mysql")
	if cli == "" {
		return nil, errors.New("the MariaDB client is missing")
	}
	cmd := exec.Command(cli, "-h", "127.0.0.1", "-P", fmt.Sprint(cfg.MySQLPort), "-u", "root", "-N", "-B", "-e", "SHOW DATABASES")
	noWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, n := range strings.Split(string(out), "\n") {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	return names, nil
}

// safeFrom accepts an absolute path that cannot be mistaken for a flag.
// (safeArg only allows version numbers, so it cannot be used for paths.)
func safeFrom(s string) bool {
	return s != "" && !strings.HasPrefix(s, "-") && filepath.IsAbs(s) && !strings.ContainsAny(s, "\x00\r\n")
}

// importArgs builds the `laman import laragon` arguments. Every name in dbs
// must be a database of the plan that laman does not have yet. The daemon
// always passes --yes: there is no one to answer a prompt.
func importArgs(from string, dbs []string, plan laragon.Plan) ([]string, error) {
	if !safeFrom(from) {
		return nil, fmt.Errorf("%q is not a folder path", from)
	}
	ok := map[string]bool{}
	for _, db := range plan.DBs {
		ok[db.Name] = !db.Exists
	}
	for _, n := range dbs {
		if !ok[n] {
			return nil, fmt.Errorf("%q is not a database the import can copy", n)
		}
	}
	args := []string{"import", "laragon", "--from", from, "--yes"}
	if len(dbs) == 0 {
		return append(args, "--no-db"), nil
	}
	return append(args, "--db", strings.Join(dbs, ",")), nil
}

// buildImportPlan finds Laragon at from ("" = the default) and works out
// the plan against laman's current config and databases.
func buildImportPlan(from string) (importPlan, string, error) {
	if from == "" {
		from = laragon.DefaultRoot
	}
	if !safeFrom(from) {
		return importPlan{}, "", fmt.Errorf("%q is not a folder path", from)
	}
	l, err := laragon.Find(filepath.Clean(from))
	if err != nil {
		return importPlan{}, "", err
	}
	cfg, err := config.Load()
	if err != nil {
		return importPlan{}, "", err
	}
	names, derr := lamanDatabases(cfg)
	if derr != nil {
		names = nil
	}
	plan := laragon.BuildPlan(l, cfg, names, l.RunningServers())
	return importPlan{Plan: plan, MariaDBOff: derr != nil}, l.Root, nil
}

// lastFileLines is the last n non-empty lines of the file, or none.
func lastFileLines(path string, n int) []string {
	lines := []string{}
	b, err := os.ReadFile(path)
	if err != nil {
		return lines
	}
	for _, l := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

func (d *daemon) importRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/import/laragon", func(w http.ResponseWriter, r *http.Request) {
		plan, _, err := buildImportPlan(r.URL.Query().Get("from"))
		if err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		writeJSON(w, plan)
	})

	mux.HandleFunc("POST /api/import/laragon", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			From     string   `json:"from"`
			DBs      []string `json:"dbs"`
			Password string   `json:"password"`
		}
		if err := readJSON(r, &req); err != nil {
			fail(w, http.StatusBadRequest, "bad request: %v", err)
			return
		}
		plan, root, err := buildImportPlan(req.From)
		if err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		args, err := importArgs(root, req.DBs, plan.Plan)
		if err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		if len(plan.Blocked) > 0 {
			fail(w, http.StatusConflict, "Quit Laragon (or click Stop All) first: %s", strings.Join(plan.Blocked, ", "))
			return
		}
		if len(req.DBs) > 0 && plan.MariaDBOff {
			fail(w, http.StatusConflict, "laman's MariaDB is not running; start it to copy databases")
			return
		}
		// The password travels in the environment only: never in the
		// arguments, which the job log prints.
		var env []string
		if req.Password != "" {
			env = []string{"LAMAN_LARAGON_PASSWORD=" + req.Password}
		}
		if err := runImportJob(d, args, env); err != nil {
			fail(w, http.StatusConflict, "%v", err)
			return
		}
		writeJSON(w, d.installJob.state())
	})

	// The command's progress and the result of its last run. The result is
	// from the previous run until the new one has written its own.
	mux.HandleFunc("GET /api/import/laragon/log", func(w http.ResponseWriter, r *http.Request) {
		out := struct {
			Lines  []string        `json:"lines"`
			Result json.RawMessage `json:"result"`
		}{Lines: lastFileLines(filepath.Join(config.LogDir(), "import-laragon.log"), 50), Result: json.RawMessage("null")}
		if b, err := os.ReadFile(filepath.Join(config.LogDir(), "import-laragon.json")); err == nil && json.Valid(b) {
			out.Result = json.RawMessage(b)
		}
		writeJSON(w, out)
	})

	mux.HandleFunc("POST /api/import/laragon/dismiss", func(w http.ResponseWriter, r *http.Request) {
		cfg, err := config.Load()
		if err == nil {
			cfg.LaragonNotice = "dismissed"
			err = cfg.Save()
		}
		if err != nil {
			fail(w, http.StatusInternalServerError, "%v", err)
			return
		}
		writeJSON(w, struct{ OK bool }{true})
	})
}
