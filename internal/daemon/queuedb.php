<?php
// laman: reads Laravel's database queue tables (jobs, failed_jobs) for the
// dashboard's Queues page. laman's daemon runs it with the default PHP and
// sends the sites on stdin as JSON. It only reads, except "forget", which
// deletes failed jobs the user chose on the dashboard.
//
// in:  {"action":"counts","sites":[site...]}
//      {"action":"jobs","site":site,"queue":"default","part":"waiting|delayed|reserved|failed","limit":20}
//      {"action":"forget","site":site,"queue":"default","ids":["12",...]}  (no ids: every failed job of the queue)
// site: {"site","conn","host","port","database","username","password","table","failed","jobs"}
// out: counts -> [{"site","queues":[{"name","waiting","delayed","reserved","failed"}],"error"}]
//      jobs   -> {"jobs":[{"id","uuid","name","attempts","pushed","due","started","failedAt","error"}],"error"}
//      forget -> {"removed":n,"error"}

error_reporting(0);
ini_set('display_errors', '0');

$in = json_decode(stream_get_contents(STDIN), true);
$now = time();

function connect(array $s, bool $write = false): PDO
{
    $opt = [PDO::ATTR_ERRMODE => PDO::ERRMODE_EXCEPTION, PDO::ATTR_TIMEOUT => 2];
    switch ($s['conn']) {
        case 'mysql':
        case 'mariadb':
            return new PDO("mysql:host={$s['host']};port={$s['port']};dbname={$s['database']};charset=utf8mb4",
                $s['username'], $s['password'], $opt);
        case 'pgsql':
            return new PDO("pgsql:host={$s['host']};port={$s['port']};dbname={$s['database']};connect_timeout=2",
                $s['username'], $s['password'], $opt);
        case 'sqlite':
            $flags = defined('Pdo\Sqlite::OPEN_READONLY') ? constant('Pdo\Sqlite::OPEN_READONLY') : PDO::SQLITE_OPEN_READONLY;
            $key = defined('Pdo\Sqlite::ATTR_OPEN_FLAGS') ? constant('Pdo\Sqlite::ATTR_OPEN_FLAGS') : PDO::SQLITE_ATTR_OPEN_FLAGS;
            return new PDO('sqlite:' . $s['database'], null, null, $write ? $opt : $opt + [$key => $flags]);
    }
    throw new Exception("{$s['conn']} databases are not supported");
}

// Table names are checked by laman ([A-Za-z0-9_]+) before they get here.
function table(array $s, string $name): string
{
    return in_array($s['conn'], ['mysql', 'mariadb'], true) ? "`$name`" : "\"$name\"";
}

function missingTable(Throwable $e): bool
{
    return (bool) preg_match('/42S02|42P01|no such table/i', $e->getMessage());
}

// why turns a PDO error into a short reason for the dashboard.
function why(Throwable $e, array $s): string
{
    $m = $e->getMessage();
    if (missingTable($e)) {
        return "no {$s['table']} table (php artisan migrate)";
    }
    if (preg_match('/\[1049\]|database ".*" does not exist/i', $m)) {
        return "its database doesn't exist on this computer yet";
    }
    if (preg_match('/\[1045\]|password authentication failed|role ".*" does not exist/i', $m)) {
        return "the database refused its username or password";
    }
    if (preg_match('/\[(2002|2003|2006)\]|could not connect|connection to server|Connection refused/i', $m)) {
        return "can't reach the database server";
    }
    if (preg_match('/could not find driver/i', $m)) {
        return "PHP's pdo_{$s['conn']} extension is off";
    }
    $m = preg_replace('/\s+/', ' ', $m);
    return strlen($m) > 160 ? substr($m, 0, 157) . '...' : $m;
}

function counts(array $s, int $now): array
{
    $out = ['site' => $s['site'], 'queues' => []];
    try {
        $db = connect($s);
    } catch (Throwable $e) {
        $out['error'] = why($e, $s);
        return $out;
    }
    $q = [];
    $row = function ($name) use (&$q) {
        return $q[$name] ?? ['name' => (string) $name, 'waiting' => 0, 'delayed' => 0, 'reserved' => 0, 'failed' => 0];
    };
    if ($s['jobs']) {
        try {
            $t = table($s, $s['table']);
            $rows = $db->query("SELECT queue,
                SUM(CASE WHEN reserved_at IS NULL AND available_at <= $now THEN 1 ELSE 0 END),
                SUM(CASE WHEN reserved_at IS NULL AND available_at > $now THEN 1 ELSE 0 END),
                SUM(CASE WHEN reserved_at IS NOT NULL THEN 1 ELSE 0 END)
                FROM $t GROUP BY queue")->fetchAll(PDO::FETCH_NUM);
            foreach ($rows as [$name, $w, $d, $r]) {
                $x = $row($name);
                [$x['waiting'], $x['delayed'], $x['reserved']] = [(int) $w, (int) $d, (int) $r];
                $q[$name] = $x;
            }
        } catch (Throwable $e) {
            $out['error'] = why($e, $s);
            return $out;
        }
    }
    try {
        $t = table($s, $s['failed']);
        foreach ($db->query("SELECT queue, COUNT(*) FROM $t GROUP BY queue")->fetchAll(PDO::FETCH_NUM) as [$name, $n]) {
            $x = $row($name);
            $x['failed'] = (int) $n;
            $q[$name] = $x;
        }
    } catch (Throwable $e) {
        // No failed_jobs table: nothing has failed that Laravel kept.
    }
    $out['queues'] = array_values($q);
    return $out;
}

function jobName($payload): string
{
    $p = json_decode((string) $payload, true);
    return is_array($p) ? (string) ($p['displayName'] ?? $p['job'] ?? '?') : '?';
}

function stamp($v): ?float
{
    if ($v === null || $v === '') {
        return null;
    }
    return is_numeric($v) ? (float) $v : (float) strtotime($v);
}

function jobs(array $s, string $queue, string $part, int $limit, int $now): array
{
    // failed_at is a plain date-time in the app's timezone.
    if (!@date_default_timezone_set((string) ($s['tz'] ?? 'UTC'))) {
        date_default_timezone_set('UTC');
    }
    try {
        $db = connect($s);
        if ($part === 'failed') {
            $t = table($s, $s['failed']);
            // SELECT *: older tables have no uuid column.
            $st = $db->prepare("SELECT * FROM $t WHERE queue = ? ORDER BY id DESC LIMIT $limit");
            $st->execute([$queue]);
            $list = [];
            foreach ($st->fetchAll(PDO::FETCH_ASSOC) as $j) {
                $first = trim(strtok((string) $j['exception'], "\n"));
                $list[] = ['id' => (string) $j['id'], 'uuid' => (string) ($j['uuid'] ?? ''), 'name' => jobName($j['payload']), 'attempts' => 0,
                    'failedAt' => stamp($j['failed_at']), 'error' => strlen($first) > 300 ? substr($first, 0, 297) . '...' : $first];
            }
            return ['jobs' => $list];
        }
        $t = table($s, $s['table']);
        $where = [
            'waiting' => "reserved_at IS NULL AND available_at <= $now ORDER BY id",
            'delayed' => "reserved_at IS NULL AND available_at > $now ORDER BY available_at",
            'reserved' => "reserved_at IS NOT NULL ORDER BY reserved_at",
        ][$part] ?? null;
        if ($where === null) {
            return ['error' => "unknown part $part"];
        }
        $st = $db->prepare("SELECT id, payload, attempts, created_at, available_at, reserved_at FROM $t WHERE queue = ? AND $where LIMIT $limit");
        $st->execute([$queue]);
        $list = [];
        foreach ($st->fetchAll(PDO::FETCH_ASSOC) as $j) {
            $list[] = ['id' => (string) $j['id'], 'name' => jobName($j['payload']), 'attempts' => (int) $j['attempts'],
                'pushed' => stamp($j['created_at']), 'due' => $part === 'delayed' ? stamp($j['available_at']) : null,
                'started' => $part === 'reserved' ? stamp($j['reserved_at']) : null];
        }
        return ['jobs' => $list];
    } catch (Throwable $e) {
        return ['error' => why($e, $s)];
    }
}

function forget(array $s, string $queue, ?array $ids): array
{
    try {
        $db = connect($s, true);
        $t = table($s, $s['failed']);
        $args = [$queue];
        $sql = "DELETE FROM $t WHERE queue = ?";
        if ($ids !== null) {
            $ids = array_values(array_filter(array_map('strval', $ids), 'ctype_digit'));
            if (!$ids) {
                return ['removed' => 0];
            }
            $sql .= ' AND id IN (' . implode(',', array_fill(0, count($ids), '?')) . ')';
            $args = array_merge($args, $ids);
        }
        $st = $db->prepare($sql);
        $st->execute($args);
        return ['removed' => $st->rowCount()];
    } catch (Throwable $e) {
        return ['error' => why($e, $s)];
    }
}

if (($in['action'] ?? '') === 'counts') {
    $out = [];
    foreach ($in['sites'] ?? [] as $s) {
        $out[] = counts($s, $now);
    }
} elseif (($in['action'] ?? '') === 'jobs') {
    $out = jobs($in['site'], (string) $in['queue'], (string) $in['part'], max(1, min(100, (int) ($in['limit'] ?? 20))), $now);
} elseif (($in['action'] ?? '') === 'forget') {
    $out = forget($in['site'], (string) $in['queue'], isset($in['ids']) ? (array) $in['ids'] : null);
} else {
    $out = ['error' => 'unknown action'];
}
echo json_encode($out, JSON_INVALID_UTF8_SUBSTITUTE);
