package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// queuePlan describes how a site's queue worker should run.
type queuePlan struct {
	Args   []string // artisan arguments after "artisan"
	Note   string
	Inline bool   // jobs run inline, so there is nothing to start
	Needs  string // "redis", "mariadb", "postgres" or ""
}

// composerRequires reports whether the project depends on pkg, either in
// composer.json or installed under vendor.
func composerRequires(dir, pkg string) bool {
	if b, err := os.ReadFile(filepath.Join(dir, "composer.json")); err == nil {
		var c struct {
			Require    map[string]json.RawMessage `json:"require"`
			RequireDev map[string]json.RawMessage `json:"require-dev"`
		}
		if json.Unmarshal(b, &c) == nil {
			if _, ok := c.Require[pkg]; ok {
				return true
			}
			if _, ok := c.RequireDev[pkg]; ok {
				return true
			}
		}
	}
	return exists(filepath.Join(dir, "vendor", filepath.FromSlash(pkg)))
}

// localHost reports whether a host value points at this machine's services.
func localHost(h string) bool {
	h = strings.TrimSpace(h)
	return h == "" || h == "127.0.0.1" || h == "localhost"
}

// planQueue picks the worker command and the local service it needs from the
// site's .env values. horizonQueues are the queue names a Horizon config
// listens on, if any.
func planQueue(dir string, vals map[string]string, horizonQueues []string) queuePlan {
	conn := strings.TrimSpace(vals["QUEUE_CONNECTION"])
	if conn == "" {
		conn = "database"
	}
	if conn == "sync" || conn == "null" {
		return queuePlan{Inline: true, Note: "Jobs run inline (QUEUE_CONNECTION=" + conn + ")"}
	}
	p := queuePlan{Args: []string{"queue:listen", "--tries=1", "--timeout=0"}}
	switch conn {
	case "redis":
		if localHost(vals["REDIS_HOST"]) {
			p.Needs = "redis"
		}
	case "database":
		switch strings.TrimSpace(vals["DB_CONNECTION"]) {
		case "mysql", "mariadb":
			p.Needs = "mariadb"
		case "pgsql":
			p.Needs = "postgres"
		}
	}
	if composerRequires(dir, "laravel/horizon") {
		p.Note = "Horizon doesn't run on Windows — using queue:listen"
		if len(horizonQueues) > 0 {
			p.Args = append(p.Args, "--queue="+strings.Join(horizonQueues, ","))
		}
	}
	return p
}

// reverbArgs are the artisan arguments that start Reverb on a local port.
func reverbArgs(port int) []string {
	return []string{"reverb:start", "--host=127.0.0.1", fmt.Sprintf("--port=%d", port), "--hostname=127.0.0.1"}
}

const horizonQueuesPHP = `require 'vendor/autoload.php';
$app = require 'bootstrap/app.php';
$app->make(Illuminate\Contracts\Console\Kernel::class)->bootstrap();
$q = [];
foreach ([config('horizon.defaults', []), config('horizon.environments.local', [])] as $sups) {
    foreach ($sups as $s) {
        if (!is_array($s)) continue;
        $v = $s['queue'] ?? [];
        foreach ((array) $v as $n) $q[$n] = true;
    }
}
echo json_encode(array_keys($q));`

// horizonQueues returns the unique queue names Horizon's supervisors listen
// on, or nil on any error.
func horizonQueues(phpExe, dir string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, phpExe, "-r", horizonQueuesPHP)
	cmd.Dir = dir
	noWindow(cmd)
	var out bytes.Buffer
	cmd.Stdout = &out
	if cmd.Run() != nil {
		return nil
	}
	var names []string
	if json.Unmarshal(bytes.TrimSpace(out.Bytes()), &names) != nil {
		return nil
	}
	return names
}
