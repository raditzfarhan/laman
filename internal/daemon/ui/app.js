"use strict";
// laman dashboard. Every request carries X-Laman-Client, which other web
// pages cannot send, so the API stays closed to cross-site requests.

const $ = (id) => document.getElementById(id);
let status = null, vers = null, logName = "php";
// Each section redraws only when its own data changes. Redrawing replaces
// buttons, and a click that lands while its button is replaced is lost;
// service uptimes change every poll, so they must not redraw the rest.
let drawn = {};
const changed = (name, data) => {
  const key = JSON.stringify(data);
  if (drawn[name] === key) return false;
  drawn[name] = key;
  return true;
};
const avail = {};       // kind -> release lines the source offers (fetched once)
let pending = null;     // upgrade waiting for its second click: {kind, line, until}
let exts = null;        // GET /api/ext for the chosen PHP version
let apps = null;        // GET /api/apps
let removing = null;    // app waiting for its second "Remove" click: {name, until}
let extPHP = "";        // chosen PHP version ("" = the default)
let opening = {};       // site host -> true while its tunnel opens
let copied = "";        // host whose share link was just copied
let shareOpts = "";     // host whose "▾" share options are open
const devOpen = new Set(); // Node sites whose dev server output is shown
const devOut = {};        // host -> latest output text
let devBusy = {};         // host -> true while a start/stop request runs
let devAsk = "";          // site named by ?start= (from a site's "start it" page), waiting for a click
let flashSite = "";       // site named by ?site=, highlighted once

async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: { "X-Laman-Client": "1", ...(body ? { "Content-Type": "application/json" } : {}) },
    body: body ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  if (!res.ok) throw new Error(text.trim() || res.statusText);
  return text && res.headers.get("Content-Type")?.includes("json") ? JSON.parse(text) : text;
}

function el(tag, props = {}, ...children) {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (k === "class") n.className = v;
    else if (k.startsWith("on")) n.addEventListener(k.slice(2), v);
    else if (v !== undefined && v !== false) n.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children.flat(Infinity)) if (c != null) n.append(c.nodeType ? c : document.createTextNode(String(c)));
  return n;
}

const short = (v) => (v || "").replace(/-\d+$/, "");
// tidy shortens the home folder to ~ (paths may use / or \).
const tidy = (p) => {
  const norm = (x) => x.replace(/\//g, "\\");
  const path = norm(p || ""), home = norm(status.userHome || "");
  return home && path.toLowerCase().startsWith(home.toLowerCase()) ? "~" + path.slice(home.length) : path;
};

// shortPath keeps the start and the last folders of a long path, where
// the name that matters is: ~\…\Sites\myproject.
const shortPath = (p) => {
  const parts = p.split("\\");
  return parts.length > 5 ? [parts[0], "…", ...parts.slice(-3)].join("\\") : p;
};

function siteURL(host) {
  const https = status.httpsPort > 0 && status.httpsTrusted;
  const port = https ? status.httpsPort : status.httpPort;
  const def = https ? 443 : 80;
  return `${https ? "https" : "http"}://${host}${port === def ? "" : ":" + port}/`;
}

// Redraws replace elements; if the focused one goes, focus moves to the
// element at the same place in the new markup, so keyboard users keep theirs.
function focusPath(node) {
  const path = [];
  while (node && !node.id && node.parentElement) {
    path.unshift([...node.parentElement.children].indexOf(node));
    node = node.parentElement;
  }
  return node?.id ? { root: node.id, path } : null;
}
function render(force) {
  if (!status) return;
  // Leave the tables alone while the user has a dropdown open.
  if (document.activeElement?.tagName === "SELECT") return;
  const had = document.activeElement;
  const where = had && had !== document.body && $("main").contains(had) ? focusPath(had) : null;
  draw(force);
  if (where && !had.isConnected) {
    let n = $(where.root);
    for (const i of where.path) n = n?.children[i];
    if (n && n.tabIndex >= 0) n.focus({ preventScroll: true });
  }
}

function draw(force) {
  if (force) drawn = {};

  const problems = status.services.filter((s) => s.state !== "running");
  const httpsNag = status.httpsNote || (status.httpsPort > 0 && !status.httpsTrusted);
  const notesCount = (status.hostsStale ? 1 : 0) + (status.notes || []).length + (httpsNag ? 1 : 0);
  const warn = problems.length || notesCount;
  $("dot").className = "dot " + (warn ? "warn" : "ok");
  $("state").textContent = problems.length ? `${problems[0].name} is ${problems[0].state}` : warn ? "running, see notes" : "running";
  if (changed("home", [status.services.map((s) => [s.name, s.state]), status.off, status.php, status.node, status.mariadb, status.postgres,
    status.redis, status.mailpit, status.sites.map((s) => [s.name, s.app]), problems.length, notesCount, status.sitesDir, status.sitesDirs])) renderHome(problems, notesCount);

  $("mail").hidden = !status.mailUrl;
  if (status.mailUrl) $("mail").href = status.mailUrl;

  // Standing notes (hosts, HTTPS, port clashes) live on Home; messages
  // from actions show wherever you are.
  const onHome = currentView() === "home";
  // The hosts job runs `laman hosts`, which raises the Windows admin prompt.
  const job = vers?.job || {};
  const hostsJob = /hosts file/.test(job.message || "") || (job.error && /hosts|elevation|admin/i.test(job.error));
  const up = status.update || {};
  if (changed("ver", [up, checkingUpdate])) {
    const v = $("ver");
    v.hidden = !up.current;
    const dev = up.current === "dev";
    v.textContent = dev ? "dev build" : up.available ? `v${up.current} → ${up.available}` : `v${up.current}`;
    v.className = "ver" + (up.available ? " new" : "");
    v.disabled = dev || checkingUpdate;
    v.title = dev ? "A local build: it doesn't look for updates"
      : up.available ? `laman ${up.available} is available. Click to check again.`
      : up.error ? `Last update check failed: ${up.error}. Click to try again.`
      : up.checked ? `Up to date (checked ${ago(up.checked)}). Click to check again.` : "Click to check for updates";
    v.onclick = checkUpdate;
  }

  const notesData = [onHome, status.hostsStale, status.notes, status.httpsNote, status.httpsPort, status.httpsTrusted, hostsJob && job, up.available, up.running];
  if (changed("notes", notesData)) {
    const notes = [];
    if (onHome && up.available) {
      notes.push(up.running
        ? [`Updating laman to ${up.available}… laman restarts, and this page reconnects by itself. Progress is in Logs > Upgrade.`]
        : [`laman ${up.available} is available (you have ${up.current}). `,
          el("button", { class: "inline", onclick: () => act(async () => {
            await api("POST", "/api/update");
            alertNote(`Updating to ${up.available}. laman restarts; the page reconnects by itself.`, "note info");
          }) }, "Update now"),
          " ", el("a", { href: up.url, target: "_blank", rel: "noopener" }, "What's new")]);
    }
    if (onHome) {
      if (status.hostsStale) {
        const waiting = hostsJob && job.running;
        notes.push(["Some sites are missing from the Windows hosts file, so their addresses do not open yet. ",
          el("button", { class: "inline", disabled: waiting || job.running,
            onclick: () => act(() => api("POST", "/api/hosts/sync")) },
            waiting ? "Approve the Windows prompt…" : "Update hosts file"),
          hostsJob && job.error ? el("span", { class: "why" }, ` ${job.error}`) : null]);
      }
      for (const n of status.notes || []) notes.push([n]);
      if (status.httpsNote) notes.push([status.httpsNote]);
      else if (status.httpsPort > 0 && !status.httpsTrusted) notes.push(["HTTPS works, but browsers will warn until you run ", el("code", {}, "laman trust"), "."]);
    }
    $("notes").replaceChildren(...notes.map((parts) => el("div", { class: "note" }, parts)));
  }

  const installed = vers?.versions.find((v) => v.kind === "php")?.installed || [];
  const ownSites = status.sites.filter((s) => !s.app);
  const shares = status.shares || [];
  $("sitefind").hidden = ownSites.length < 8;
  // Dev server uptimes and output change every poll; they are updated in
  // place below so the rows (and their buttons) stay put.
  const devShape = (status.dev || []).map(({ uptime, ...rest }) => rest);
  if (changed("sites", [ownSites, status.php, installed, status.httpsPort, status.httpsTrusted, status.httpPort, shares, opening, copied, shareOpts,
    status.shareDomain, shareCfg?.pattern, status.folders, siteQuery(), [...folded], devShape, status.devAuto, [...devOpen], devBusy, devAsk, flashSite])) renderSites(ownSites, installed, shares);
  for (const pre of document.querySelectorAll("[data-devout]")) {
    const text = devOut[pre.dataset.devout] ?? "Loading…";
    if (pre.textContent !== text) {
      const atEnd = pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 8;
      pre.textContent = text;
      if (atEnd) pre.scrollTop = pre.scrollHeight;
    }
  }
  if (!(naming && document.activeElement?.classList.contains("fprefix")) && changed("folders", [status.folders, status.sites.map((s) => [s.path, s.app, s.linked, s.folder]), unfolding, picking, naming])) renderFolders();
  $("sites").hidden = ownSites.length === 0;
  $("nosites").hidden = ownSites.length > 0;

  // Uptimes change every poll; redrawing for them would replace the
  // switches under the cursor, so they are updated in place instead.
  const shape = status.services.map(({ uptime, ...rest }) => rest);
  if (changed("services", [shape, status.off, [...expanded]])) renderServices();
  for (const s of status.services) {
    const cell = document.querySelector(`[data-up="${s.name}"]`);
    if (cell && s.state === "running") cell.textContent = s.uptime || "";
  }

  if (vers && changed("versions", [vers, avail, pending])) renderVersions();
  if (apps && changed("apps", [apps, removing, status.httpsPort, status.httpsTrusted, status.httpPort])) renderApps();
  if (shareCfg && changed("sharing", [shareCfg, shareEditing])) renderSharing();
  if (queues && changed("queues", [queues, [...openQueues], queueJobs, clearing, [...othersOpen], $("qfind").value])) renderQueues();
  if (exts && changed("exts", [exts, $("extfilter").value, vers?.versions.find((v) => v.kind === "php")?.installed])) renderExts();
}

// Job messages ("Installed PHP 8.4") stay 10 seconds after the job ends;
// errors stay until the next job.
const jobSeen = {};
// A finished job's message shows only if this page saw it running or the
// user just did something, so an old "Updated the hosts file" from hours
// ago doesn't greet every visit.
const jobRan = {};
let lastAct = 0;
function jobText(key, job) {
  if (job.error) return job.error;
  if (job.running) { jobRan[key] = true; delete jobSeen[key]; return job.message || ""; }
  if (!job.message) return "";
  if (jobSeen[key]?.msg !== job.message) {
    const fresh = jobRan[key] || Date.now() - lastAct < 60000;
    jobSeen[key] = { msg: job.message, at: fresh ? Date.now() : 0 };
    delete jobRan[key];
    if (fresh) setTimeout(() => render(true), 10100);
  }
  return Date.now() - jobSeen[key].at < 10000 ? job.message : "";
}

// Sites, grouped by the project folder they are in (links last). With one
// group there are no headers; a group can be folded away, and the search
// box narrows every group at once.
let folded = new Set(); // folder paths folded away (kept in this browser)
let foldChosen = false;  // false until the user folds or opens a group
try {
  const saved = localStorage.getItem("laman.folded");
  if (saved) { folded = new Set(JSON.parse(saved)); foldChosen = true; }
} catch { /* private window */ }
const siteQuery = () => ($("sitefind")?.value || "").trim().toLowerCase();

function renderSites(ownSites, installed, shares) {
  const q = siteQuery();
  const match = (s) => !q || s.host.toLowerCase().includes(q) || s.path.toLowerCase().includes(q);
  const groups = (status.folders || []).map((f) => ({ key: f.dir, title: f.dir, prefix: f.prefix, list: [] }));
  const linked = { key: "", title: "Linked", list: [] };
  // Folders with nothing a browser can open go last, folded unless opened
  // (for this group, being in `folded` means open).
  const none = { key: NONE, title: "Not websites", list: [] };
  for (const s of ownSites) {
    if (s.kind === "none") { none.list.push(s); continue; }
    const g = groups.find((x) => s.folder && normDir(x.key) === normDir(s.folder)) || linked;
    g.list.push(s);
  }
  if (linked.list.length) groups.push(linked);
  if (none.list.length) groups.push(none);
  const used = groups.filter((g) => g.list.length);
  const flat = used.length < 2;
  // A long list starts with every group but the first folded, so Home
  // opens as an overview; Find still searches all of them.
  if (!foldChosen && !flat && ownSites.length > 30) {
    for (const g of used.slice(1)) if (g.key !== NONE) folded.add(g.key);
    foldChosen = true;
  }
  const out = [];
  for (const g of used) {
    const shown = g.list.filter(match);
    if (q && !shown.length) continue;
    const isFolded = !q && !flat && (g.key === NONE ? !folded.has(g.key) : folded.has(g.key));
    if (!flat) {
      out.push(el("button", { class: "grp", "aria-expanded": isFolded ? "false" : "true",
        title: isFolded ? "Show these sites" : "Hide these sites",
        onclick: () => {
          if (q) return;
          folded.has(g.key) ? folded.delete(g.key) : folded.add(g.key);
          foldChosen = true;
          try { localStorage.setItem("laman.folded", JSON.stringify([...folded])); } catch { /* private window */ }
          render(true);
        } },
        el("span", { class: "chev", "aria-hidden": "true" }, "▸"),
        el("span", { class: "gname" }, g.key === NONE ? g.title : g.key ? g.title.split(/[\\/]/).filter(Boolean).pop() : "Linked"),
        g.key === NONE ? el("span", { class: "gpath" }, "no index.php, index.html or dev script: infra, docs, notes")
          : g.key ? el("span", { class: "gpath mono" }, shortPath(tidy(g.title))) : el("span", { class: "gpath" }, "folders served with laman link"),
        el("span", { class: "gcount" }, q ? `${shown.length} of ${g.list.length}` : `${g.list.length} site${g.list.length === 1 ? "" : "s"}`)));
    }
    if (!isFolded) out.push(...shown.map((s) => siteRow(s, installed, shares)));
  }
  if (q && !out.length) out.push(el("div", { class: "empty-state" }, `No site matches “${q}”.`));
  $("sites").replaceChildren(...out);
}

const NONE = "\u0000none"; // the "Not websites" group's key

function siteRow(s, installed, shares) {
    if (s.kind === "node") return nodeRow(s, shares);
    if (s.kind === "proxy" || s.kind === "group" || s.kind === "none") return plainRow(s);
    const sel = el("select", { "aria-label": `PHP version for ${s.name}`, onchange: (e) => setSitePHP(s.name, e.target.value) },
      el("option", { value: "", selected: !s.isolated }, `PHP ${short(status.php)} (default)`),
      installed.map((v) => el("option", { value: v, selected: s.isolated && s.php === v }, `PHP ${short(v)}`)));
    const url = siteURL(s.host);
    const sh = shares.find((x) => x.host === s.host);
    return el("div", { class: "site" },
      el("div", { class: "who" },
        el("a", { class: "host", href: url, target: "_blank", rel: "noopener" }, s.host),
        s.linked ? el("span", { class: "tag" }, "linked") : null,
        el("span", { class: "path", title: s.root }, shortPath(tidy(s.path)))),
      sel,
      el("span", { class: "sharegrp" },
        el("button", { class: "sharebtn", disabled: !!sh || opening[s.host],
          title: status.shareDomain ? `Share at ${shareAddress(s.host)}` : "Get a public https link anyone can open (Cloudflare)",
          onclick: () => shareSite(s.host) }, opening[s.host] ? "Sharing…" : sh ? "Shared" : "Share"),
        status.shareDomain && !sh && !opening[s.host]
          ? el("button", { class: "sharebtn more", "aria-label": `More ways to share ${s.host}`, "aria-expanded": shareOpts === s.host ? "true" : "false",
            onclick: () => { shareOpts = shareOpts === s.host ? "" : s.host; render(true); } }, "▾")
          : null),
      el("a", { class: "open", href: url, target: "_blank", rel: "noopener", "aria-label": `Open ${s.host}` }, "Open ↗"),
      shareBlock(s, sh));
}

// What sits under a row while it is shared or being shared.
function shareBlock(s, sh) {
  return sh ? el("div", { class: "shared" },
        el("span", { class: "live" }, "Public"),
        el("a", { href: sh.url, target: "_blank", rel: "noopener" }, sh.url.replace(/^https:\/\//, "")),
        el("span", { class: "acts" },
          el("button", { onclick: () => copyLink(s.host, sh.url) }, copied === s.host ? "Copied" : "Copy link"),
          el("button", { onclick: () => act(() => api("POST", "/api/share/stop", { site: s.host })) }, "Stop sharing")),
        el("span", { class: "why" }, "Anyone with the link can open this site until you stop sharing or laman stops. " + (sh.own ? "The address stays the same next time." : "The link changes each time.")))
      : opening[s.host] ? el("div", { class: "shared" }, el("span", { class: "why" }, status.shareDomain
        ? `Sharing at ${shareAddress(s.host)}…` : "Opening a Cloudflare tunnel… this takes up to half a minute (longer the first time, to download cloudflared)."))
      : shareOpts === s.host && status.shareDomain ? shareOptions(s)
      : null;
}

function shareGroup(s, sh, disabled) {
  return el("span", { class: "sharegrp" },
    el("button", { class: "sharebtn", disabled: disabled || !!sh || opening[s.host],
      title: disabled ? "Start the dev server first" : status.shareDomain ? `Share at ${shareAddress(s.host)}` : "Get a public https link anyone can open (Cloudflare)",
      onclick: () => shareSite(s.host) }, opening[s.host] ? "Sharing…" : sh ? "Shared" : "Share"),
    status.shareDomain && !sh && !opening[s.host] && !disabled
      ? el("button", { class: "sharebtn more", "aria-label": `More ways to share ${s.host}`, "aria-expanded": shareOpts === s.host ? "true" : "false",
        onclick: () => { shareOpts = shareOpts === s.host ? "" : s.host; render(true); } }, "▾")
      : null);
}

const devOf = (host) => (status.dev || []).find((d) => d.host === host) || { state: "stopped" };

async function devAction(host, path) {
  devBusy = { ...devBusy, [host]: true };
  render();
  try { await api("POST", path, { site: host }); } catch (e) { alertNote(e.message); }
  const { [host]: _, ...rest } = devBusy;
  devBusy = rest;
  if (devAsk && status.sites.find((s) => s.name === devAsk)?.host === host) devAsk = "";
  await refresh();
}

// A Node app: its dev server's state, Start/Stop, its output, and
// "start with laman".
function nodeRow(s, shares) {
  const d = devOf(s.host);
  const busy = devBusy[s.host];
  const up = d.state === "running" || d.state === "starting";
  const sh = shares.find((x) => x.host === s.host);
  const url = siteURL(s.host);
  const open = devOpen.has(s.host);
  const label = { running: `running · :${d.port}`, starting: "starting…", crashed: "crashed", stopped: "stopped" }[d.state] || d.state;
  const asked = devAsk === s.name;
  return el("div", { class: "site node" + (asked || flashSite === s.name ? " flash" : ""), id: `site-${s.name}` },
    el("div", { class: "who" },
      el("a", { class: "host", href: url, target: "_blank", rel: "noopener" }, s.host),
      el("span", { class: "tag" }, s.framework || "Node"),
      el("span", { class: "path", title: s.path }, shortPath(tidy(s.path)))),
    el("span", { class: "devctl" },
      el("span", { class: `devstate ${d.state}`, title: d.state === "running" ? `${s.devCmd}, up ${d.uptime || ""}` : s.devCmd }, label),
      el("button", { class: up ? "" : "primary", disabled: !!busy,
        title: up ? "Stop the dev server and everything it started" : `Run ${s.devCmd} in ${s.path}`,
        onclick: () => devAction(s.host, up ? "/api/dev/stop" : "/api/dev/start") }, busy ? "…" : up ? "Stop" : "Start"),
      el("button", { class: "linkish", "aria-expanded": open ? "true" : "false",
        onclick: () => { open ? devOpen.delete(s.host) : devOpen.add(s.host); loadDevOut(); render(true); } }, open ? "Hide output" : "Output")),
    shareGroup(s, sh, d.state !== "running"),
    el("a", { class: "open", href: url, target: "_blank", rel: "noopener", "aria-label": `Open ${s.host}` }, "Open ↗"),
    asked && !up ? el("div", { class: "shared" },
      el("span", {}, `Start ${s.host}'s dev server? It runs `, el("code", {}, s.devCmd), "."),
      el("span", { class: "acts" },
        el("button", { class: "primary", onclick: () => devAction(s.host, "/api/dev/start") }, "Start"),
        el("button", { onclick: () => { devAsk = ""; render(true); } }, "Not now"))) : null,
    open ? el("div", { class: "devout" },
      el("pre", { "data-devout": s.host, tabindex: "0", "aria-label": `${s.host} dev server output` }),
      el("div", { class: "devfoot" },
        el("label", { class: "devauto" },
          el("input", { type: "checkbox", checked: !!(status.devAuto || {})[s.name],
            onchange: (e) => act(() => api("POST", "/api/dev/auto", { site: s.host, on: e.target.checked })) }),
          "Start with laman"),
        el("span", { class: "fine" }, el("code", {}, s.devCmd), d.port ? ` on port ${d.port}` : ""))) : null,
    shareBlock(s, sh));
}

// Proxies, folders of projects, and folders that are not websites.
function plainRow(s) {
  const url = siteURL(s.host);
  let tag, middle, sub = null;
  if (s.kind === "proxy") {
    tag = `proxy → ${s.proxy}`;
    middle = el("button", { title: `${s.host} stops forwarding; nothing else changes`,
      onclick: () => act(() => api("POST", "/api/proxy", { name: s.name, target: "" })) }, "Remove");
  } else if (s.kind === "group") {
    tag = "projects inside";
    middle = el("button", { class: flashSite === s.name ? "primary" : "",
      title: `Serve each folder inside ${s.path} as ${s.name}-<folder>.test`,
      onclick: () => act(async () => { await api("POST", "/api/folders/add", { path: s.path }); flashSite = ""; }) }, "Serve the projects inside");
    sub = el("span", { class: "path" }, "Each folder inside gets its own address, such as ", el("b", {}, `${s.name}-app.test`), ".");
  } else {
    tag = "not a website";
    middle = el("span");
  }
  return el("div", { class: "site" + (flashSite === s.name ? " flash" : ""), id: `site-${s.name}` },
    el("div", { class: "who" },
      el("a", { class: "host", href: url, target: "_blank", rel: "noopener" }, s.host),
      el("span", { class: "tag" }, tag),
      s.path ? el("span", { class: "path", title: s.path }, shortPath(tidy(s.path))) : null,
      sub),
    middle, el("span"),
    s.kind === "none" ? el("span") : el("a", { class: "open", href: url, target: "_blank", rel: "noopener", "aria-label": `Open ${s.host}` }, "Open ↗"));
}

async function loadDevOut() {
  for (const host of devOpen) {
    try { devOut[host] = String(await api("GET", `/api/dev/log?site=${encodeURIComponent(host)}`)).replace(/^\s*\n|\s+$/g, ""); } catch { /* next poll */ }
  }
  render();
}


// Home: the prompt line, the stack rack, and counts in the rail.
const isWorker = (name) => /^php\d/.test(name);
function renderHome(problems, notesCount) {
  const running = status.services.filter((s) => s.state === "running").length;
  $("headline").textContent = problems.length ? `laman: ${problems[0].name} is ${problems[0].state}` : "laman is running";
  const n = status.sites.filter((s) => !s.app).length;
  const dbSite = status.sites.find((s) => s.name === "db" && s.app);
  $("dblink").hidden = !dbSite;
  if (dbSite) { $("dblink").href = siteURL(dbSite.host); $("dblink").title = `${dbSite.host}: your database app`; }
  const redisSite = status.sites.find((s) => s.name === "redis" && s.app);
  $("redislink").hidden = !redisSite || !status.redis;
  if (redisSite) { $("redislink").href = siteURL(redisSite.host); $("redislink").title = `${redisSite.host}: browse Redis keys`; }
  $("summary").replaceChildren(
    el("b", {}, `${n} site${n === 1 ? "" : "s"}`), " on ", el("span", { class: "mono" }, ".test"),
    ` · ${running} process${running === 1 ? "" : "es"} running`,
    notesCount ? ` · ${notesCount} note${notesCount === 1 ? "" : "s"} above` : "");
  const dirs = status.sitesDirs || [status.sitesDir];
  $("sitesdirlabel").textContent = dirs.length > 1 ? "Project folders:" : "Sites folder:";
  $("sitesdir").textContent = dirs.length > 1 ? String(dirs.length) : status.sitesDir ? shortPath(tidy(status.sitesDir)) : "";
  $("nav-folders").textContent = String(dirs.length);
  $("opensites").title = dirs.length > 1 ? "Show your project folders" : status.sitesDir ? `Open ${tidy(status.sitesDir)} in Explorer` : "";
  $("sitecount").textContent = n ? String(n) : "";
  $("nav-sites").textContent = n ? String(n) : "";
  const pools = new Set(status.services.filter((s) => isWorker(s.name)).map((s) => s.name.split("-")[0]));
  $("nav-services").textContent = String(status.services.filter((s) => !isWorker(s.name)).length + pools.size);

  const svc = (name) => status.services.find((s) => s.name === name);
  const line = short(status.php).split(".").slice(0, 2).join(".");
  const php = status.services.filter((s) => s.name.startsWith(`php${line}-`));
  const stateOf = (name) => ((status.off || []).includes(name) ? "off" : svc(name)?.state || "none");
  const item = (label, version, port, state, view) => el("li", { class: state === "off" ? "is-off" : "" },
    el("a", { href: "#" + view },
      el("span", { class: "k" }, el("span", { class: "dot " + (state === "running" ? "ok" : state === "off" || state === "none" ? "" : "warn") }), label),
      el("span", { class: "v" }, version),
      el("span", { class: "p" }, port)));
  const items = [
    item("Web", status.webServer || "Caddy", status.httpsPort ? `:${status.httpPort} · :${status.httpsPort}` : `:${status.httpPort}`, svc("caddy")?.state || "none", "services"),
    item("PHP", short(status.php), php.length ? `${php.filter((w) => w.state === "running").length} of ${php.length} workers` : "idle",
      php.every((w) => w.state === "running") ? "running" : "warn", "versions"),
  ];
  if (status.node) items.push(item("Node", status.node, "default", "running", "versions"));
  const db = (label, kind, version, port) => {
    const st = stateOf(kind);
    if (st === "none" && !version) return;
    items.push(item(label, st === "off" ? "off" : version || "—", st === "off" ? "turned off" : `:${port}`, st, "services"));
  };
  db("MariaDB", "mariadb", status.mariadb, status.mysqlPort);
  db("PostgreSQL", "postgres", status.postgres, status.postgresPort);
  db("Redis", "redis", status.redis, status.redisPort);
  db("Mailpit", "mailpit", status.mailpit, status.smtpPort);
  $("rack").replaceChildren(...items);
}

// Apps: install, open, update, pick the db.test app, remove (two clicks,
// because removing deletes what the app stored).
function renderApps() {
  const busy = apps.job.running;
  const host = (name) => `${name}.${apps.tld}`;
  const installedCount = apps.apps.filter((a) => a.installed).length;
  $("nav-apps").textContent = installedCount ? String(installedCount) : "";
  $("dbhost").textContent = host("db");
  $("apps").replaceChildren(...apps.apps.map((a) => {
    const actions = [];
    if (!a.installed) {
      actions.push(el("button", { class: "primary", disabled: busy, onclick: () => act(() => api("POST", "/api/apps/install", { name: a.name })) }, "Install"));
    } else {
      actions.push(el("a", { class: "btn primary", href: siteURL(host(a.name)), target: "_blank", rel: "noopener" }, "Open ↗"));
      if (a.latest && a.latest !== a.installed) {
        actions.push(el("button", { disabled: busy, onclick: () => act(() => api("POST", "/api/apps/update", { name: a.name })) }, `Update to ${a.latest}`));
      }
      if (a.category === "database" && !a.default) {
        actions.push(el("button", { disabled: busy, title: `Make ${host("db")} open ${a.title}`, onclick: () => act(() => api("POST", "/api/apps/default", { name: a.name })) }, `Use for ${host("db")}`));
      }
      const waiting = removing && removing.name === a.name && Date.now() < removing.until;
      actions.push(el("button", {
        class: waiting ? "confirm" : "linkish", disabled: busy,
        onclick: () => {
          if (!waiting) {
            removing = { name: a.name, until: Date.now() + 6000 };
            render(true);
            setTimeout(() => render(true), 6100);
            return;
          }
          removing = null;
          act(() => api("POST", "/api/apps/remove", { name: a.name }));
        },
      }, waiting ? `Confirm: remove ${a.title} and its data` : "Remove"));
    }
    return el("div", { class: "app" },
      el("div", {},
        el("h3", {}, a.title,
          a.installed ? el("span", { class: "ver" }, a.installed) : null,
          a.default ? el("span", { class: "tag default" }, host("db")) : null),
        el("p", {}, a.about),
        el("span", { class: "src" }, a.installed ? host(a.name) : `from ${a.source} · PHP ${a.php}`)),
      el("div", { class: "actions" }, actions));
  }));
  $("appjob").className = "job " + (apps.job.error ? "err" : "");
  $("appjob").textContent = jobText("apps", apps.job);
}

// Queues: Laravel jobs in laman's Redis. Polled only while the view is open;
// a queue's jobs are fetched when it is opened, and refreshed with it.
let queues = null;        // GET /api/queues
const openQueues = new Set();   // ids of queues showing their jobs
const queueJobs = {};     // id -> {waiting, delayed, reserved, failed}: lists, or {error}
let clearing = null;      // Redis queue waiting for its second "Clear" click: {id, until}
const othersOpen = new Set(); // blocks below whose full site list is shown

// A queue's id: Redis queues by key, database ones by site and name.
const qid = (q) => `${q.driver}|${q.site || ""}|${q.name}|${q.key || ""}`;
const qsite = (q) => q.site || (q.prefix ? q.prefix.replace(/_database_$/, "") : "(no site)");
const bare = (h) => h.replace(/\.test$/, "");

async function loadQueues() {
  try { queues = await api("GET", "/api/queues"); } catch { return; }
  for (const id of openQueues) {
    const q = queues.queues.find((x) => qid(x) === id);
    if (!q) { openQueues.delete(id); continue; }
    await loadJobs(q);
  }
  render();
}

// The lists behind a queue row. Redis keeps waiting/delayed/running jobs;
// failures (any driver) and database queues are read from the site's database.
async function loadJobs(q) {
  const redis = (part) => api("GET", `/api/queues/jobs?db=${q.db}&key=${encodeURIComponent(q.key)}&part=${part}`);
  const db = (part) => api("GET", `/api/queues/dbjobs?site=${encodeURIComponent(q.site)}&queue=${encodeURIComponent(q.name)}&part=${part}`);
  const get = (part, n) => !n ? Promise.resolve([]) : (q.driver === "redis" && part !== "failed" ? redis(part) : db(part)).catch((e) => ({ error: e.message }));
  const [waiting, delayed, reserved, failed] = await Promise.all([
    get("waiting", q.waiting || q.driver === "database"), get("delayed", q.delayed), get("reserved", q.reserved), get("failed", q.failed)]);
  queueJobs[qid(q)] = { waiting, delayed, reserved, failed };
}

const ago = (t) => {
  if (!t) return "";
  const s = Math.round(Date.now() / 1000 - t);
  if (s < 0) return `in ${fmtSpan(-s)}`;
  return `${fmtSpan(s)} ago`;
};
const fmtSpan = (s) => s < 60 ? `${s}s` : s < 3600 ? `${Math.round(s / 60)}m` : s < 86400 ? `${Math.round(s / 3600)}h` : `${Math.round(s / 86400)}d`;

// What the other queue drivers mean for someone looking for their jobs.
const DRIVERS = {
  sync: ["Run immediately", "Jobs run inside the request that dispatches them, so nothing ever waits. Nothing to watch here."],
  null: ["Switched off", "Dispatched jobs are thrown away."],
};

function renderQueues() {
  const list = queues.queues || [];
  const sum = (k) => list.reduce((n, q) => n + (q[k] || 0), 0);
  const pending = sum("waiting") + sum("delayed") + sum("reserved");
  const failed = sum("failed");
  // The rail shows what needs you: failed jobs first, else work waiting.
  const nq = $("nav-queues");
  nq.textContent = failed ? String(failed) : pending ? String(pending) : "";
  nq.className = "count" + (failed ? " bad" : "");
  nq.title = failed ? `${failed} failed job${failed === 1 ? "" : "s"}` : pending ? `${pending} job${pending === 1 ? "" : "s"} waiting or running` : "";
  const stat = (n, label, bad) => el("span", { class: "stat" }, el("b", { class: n ? (bad ? "bad" : "") : "zero" }, String(n)), el("span", {}, label));
  $("qtotals").replaceChildren(el("span", { class: "qcap" }, "All sites:"), stat(sum("waiting"), "waiting"), stat(sum("delayed"), "delayed"), stat(sum("reserved"), "running"), stat(failed, "failed", true));

  const idle = queues.idle || [], unchecked = queues.unchecked || [], other = queues.other || [];
  const known = new Set([...list.map((q) => q.site).filter(Boolean), ...idle, ...unchecked.map((u) => u.site), ...other.map((o) => o.site)]);
  $("qfind").hidden = known.size < 8;
  const q = ($("qfind").value || "").trim().toLowerCase();
  const hit = (host) => !q || host.toLowerCase().includes(q);

  const rows = list.filter((x) => hit(qsite(x)) || (q && x.name.toLowerCase().includes(q)));
  const siteCount = new Set(list.map(qsite)).size;
  $("qhint").textContent = list.length ? `${list.length} queue${list.length === 1 ? "" : "s"} in ${siteCount} site${siteCount === 1 ? "" : "s"} with work or failures` : "";
  qPerSite = new Map();
  for (const x of list) qPerSite.set(qsite(x), (qPerSite.get(qsite(x)) || 0) + 1);
  const box = $("queues");
  if (rows.length) box.replaceChildren(...rows.map(queueRow));
  else box.replaceChildren(el("div", { class: "qempty" },
    el("h3", {}, q ? `No busy queue matches “${q}”` : "All quiet"),
    el("p", {}, q ? "Idle and unchecked sites are listed below."
      : `Nothing is waiting, running or failed${idle.length ? ` in ${idle.length} site${idle.length === 1 ? "" : "s"} checked` : ""}. This page refreshes every few seconds.`)));

  // Below: idle sites, sites laman couldn't check (grouped by reason), and
  // other drivers. Long lists show 12 names until opened.
  const chips = (key, hosts, title) => {
    const shown = hosts.filter(hit);
    const open = othersOpen.has(key) || q;
    const cut = open ? shown : shown.slice(0, 12);
    return el("div", { class: "chips" },
      cut.map((h) => el("a", { class: "chip", href: siteURL(h), target: "_blank", rel: "noopener", title: title ? title(h) : `Open ${h}` }, bare(h))),
      shown.length > 12 && !q ? el("button", { class: "linkish more", onclick: () => { othersOpen.has(key) ? othersOpen.delete(key) : othersOpen.add(key); render(true); } },
        open ? "Show fewer" : `+${shown.length - 12} more`) : null);
  };
  const block = (key, title, count, text, hosts, tip) => hosts.some(hit) ? el("div", { class: "driver" },
    el("h3", {}, title, el("span", { class: "n" }, count)), text ? el("p", {}, text) : null, chips(key, hosts, tip)) : null;
  const blocks = [];
  if (idle.length) blocks.push(block("idle", "Idle", `${idle.length} site${idle.length === 1 ? "" : "s"}`, "Checked: nothing waiting, running or failed.", idle));
  const byWhy = new Map();
  const detail = new Map(unchecked.map((u) => [u.site, u.detail]));
  for (const u of unchecked) byWhy.set(u.why, [...(byWhy.get(u.why) || []), u.site]);
  if (unchecked.length) {
    const reasons = [...byWhy.entries()].sort((a, b) => b[1].length - a[1].length);
    if (reasons.some(([, hosts]) => hosts.some(hit))) blocks.push(el("div", { class: "driver" },
      el("h3", {}, "Not checked", el("span", { class: "n" }, `${unchecked.length} site${unchecked.length === 1 ? "" : "s"}`)),
      reasons.filter(([, hosts]) => hosts.some(hit)).map(([why, hosts]) => el("div", { class: "why-group" },
        el("p", {}, why.charAt(0).toUpperCase() + why.slice(1)),
        chips(`why:${why}`, hosts, (h) => detail.get(h) ? `${h}: ${detail.get(h)}` : `Open ${h}`)))));
  }
  const byDriver = new Map();
  for (const o of other) byDriver.set(o.driver, [...(byDriver.get(o.driver) || []), o.site]);
  for (const [driver, hosts] of [...byDriver.entries()].sort()) {
    const [title, text] = DRIVERS[driver] || [driver, `Jobs go to ${driver}, which laman can't read.`];
    blocks.push(block(`drv:${driver}`, title, `${hosts.length} site${hosts.length === 1 ? "" : "s"} · QUEUE_CONNECTION=${driver}`, text, hosts));
  }
  const shown = blocks.filter(Boolean);
  $("qother").replaceChildren(
    ...(queues.error ? [el("div", { class: "note err" }, queues.error)] : []),
    ...(shown.length ? [el("div", { class: "sect-head spaced" }, el("h2", {}, "Everything else")), el("div", { class: "panel" }, shown)] : []));
}

let qPerSite = new Map(); // site -> number of busy queues (names the queue when > 1)
function queueRow(q) {
  const id = qid(q);
  const open = openQueues.has(id);
  const stat = (n, label, bad) => el("span", { class: "stat" }, el("b", { class: n ? (bad ? "bad" : "") : "zero" }, String(n || 0)), el("span", {}, label));
  const waiting = clearing && clearing.id === id && Date.now() < clearing.until;
  const pending = q.waiting + q.delayed;
  const row = el("div", { class: "row" },
    el("div", { class: "who" },
      el("span", { class: "qsite" }, q.site ? el("a", { href: siteURL(q.site), target: "_blank", rel: "noopener" }, bare(q.site)) : qsite(q),
        qPerSite.get(qsite(q)) > 1 ? el("span", { class: "qn" }, ` · ${q.name}`) : null),
      el("span", { class: "qname" }, `${q.driver === "redis" ? "redis" : `database · ${q.conn}`} · queue ${q.name}`)),
    el("div", { class: "counts" }, stat(q.waiting, "waiting"), stat(q.delayed, "delayed"), stat(q.reserved, "running"), stat(q.failed, "failed", true)),
    el("div", { class: "acts" },
      el("button", { "aria-expanded": open ? "true" : "false", onclick: () => toggleQueue(q) }, open ? "Hide jobs" : "Show jobs"),
      q.driver === "redis" && q.key ? el("button", {
        class: waiting ? "confirm" : "", disabled: !pending,
        title: "Delete the waiting and delayed jobs (running ones finish)",
        onclick: () => {
          if (!waiting) {
            clearing = { id, until: Date.now() + 6000 };
            render(true);
            setTimeout(() => render(true), 6100);
            return;
          }
          clearing = null;
          act(async () => { await api("POST", "/api/queues/clear", { db: q.db, key: q.key }); await loadQueues(); });
        },
      }, waiting ? `Confirm: delete ${pending} job${pending === 1 ? "" : "s"}` : "Clear") : null));
  const out = el("div", { class: "queue" + (q.failed ? " has-failed" : "") }, row);
  if (open) {
    const jobs = queueJobs[id];
    const part = (title, data, when) => [
      el("h4", {}, title),
      !data ? el("div", { class: "none" }, "Loading…")
        : data.error ? el("div", { class: "none err" }, data.error)
        : !data.length ? el("div", { class: "none" }, "None")
        : data.map((j) => el("div", { class: "job-row" + (when === "failed" ? " failed" : "") },
          el("span", { class: "cls", title: `job ${j.id}` }, j.name),
          el("span", { class: "meta" }, j.attempts ? `attempt ${j.attempts}` : ""),
          el("span", { class: "meta" },
            when === "pushed" ? ago(j.pushed)
            : when === "due" ? `runs ${ago(j.due)}`
            : when === "failed" ? `failed ${ago(j.failedAt)}`
            : j.started ? `started ${ago(j.started)}`
            : j.due * 1000 > Date.now() ? `times out ${ago(j.due)}` : `timed out ${ago(j.due)}`),
          when === "failed" ? failActs(q, j) : null,
          when === "failed" && j.error ? el("span", { class: "trace" }, j.error) : null)),
    ];
    out.append(el("div", { class: "jobs" },
      q.failed ? part("Failed (newest first)", jobs?.failed, "failed") : null,
      q.failed && q.site ? failBar(q) : null,
      q.waiting || q.driver === "database" ? part("Next up", jobs?.waiting, "pushed") : null,
      q.delayed ? part("Delayed", jobs?.delayed, "due") : null,
      q.reserved ? part("Running", jobs?.reserved, "run") : null));
  }
  return out;
}

// Failed jobs: Retry puts a job back on its queue through the project's
// own `php artisan queue:retry`; Delete removes it from failed_jobs.
// Deleting more than one job asks for a second click.
let failBusy = {};       // key -> true while a request runs
let failAsk = null;      // {key, until}: "Delete all" waiting for its second click
const failKey = (q, what) => `${qid(q)}|${what}`;

function failActs(q, j) {
  if (!q.site) return null;
  const k = failKey(q, j.id);
  const busy = failBusy[k];
  return el("span", { class: "jacts" },
    el("button", { disabled: busy, title: "Put this job back on the queue (php artisan queue:retry)",
      onclick: () => failAction(q, "retry", [j.uuid || j.id], k) }, "Retry"),
    el("button", { disabled: busy, title: "Delete this failed job",
      onclick: () => failAction(q, "forget", [j.id], k) }, "Delete"));
}

function failBar(q) {
  const k = failKey(q, "*");
  const busy = failBusy[k];
  const n = q.failed;
  const asking = failAsk && failAsk.key === k && Date.now() < failAsk.until;
  const all = n === 1 ? "the failed job" : `all ${n} failed jobs`;
  return el("div", { class: "failbar" },
    el("button", { disabled: busy, title: `php artisan queue:retry --queue=${q.name}`,
      onclick: () => failAction(q, "retry", null, k) }, n === 1 ? "Retry" : `Retry all ${n}`),
    el("button", { class: asking ? "confirm" : "", disabled: busy,
      onclick: () => {
        if (!asking && n > 1) {
          failAsk = { key: k, until: Date.now() + 6000 };
          render(true);
          setTimeout(() => render(true), 6100);
          return;
        }
        failAsk = null;
        failAction(q, "forget", null, k);
      } }, asking ? `Confirm: delete ${n}` : n === 1 ? "Delete" : `Delete all ${n}`),
    busy ? el("span", { class: "fine" }, "Working…") : el("span", { class: "fine" }, `Retry sends ${all} back to queue ${q.name}; a worker has to be running to pick them up.`));
}

async function failAction(q, action, ids, key) {
  failBusy = { ...failBusy, [key]: true };
  render(true);
  try {
    const res = await api("POST", "/api/queues/failed", { site: q.site, queue: q.name, action, ids: ids || [], all: !ids });
    const n = ids ? ids.length : q.failed;
    const jobs = `${n} job${n === 1 ? "" : "s"}`;
    alertNote(action === "retry" ? `${bare(q.site)}: ${jobs} back on queue ${q.name}.`
      : `${bare(q.site)}: deleted ${res.Removed} failed job${res.Removed === 1 ? "" : "s"}.`, "note info");
  } catch (e) { alertNote(e.message); }
  const { [key]: _, ...rest } = failBusy;
  failBusy = rest;
  await loadQueues();
  render(true);
}

async function toggleQueue(q) {
  const id = qid(q);
  if (openQueues.has(id)) { openQueues.delete(id); render(true); return; }
  openQueues.add(id);
  render(true);
  await loadJobs(q);
  render(true);
}

$("qfind").addEventListener("input", () => render(true));
$("qfind").addEventListener("keydown", (e) => { if (e.key === "Escape") { e.target.value = ""; render(true); } });

setInterval(() => { if (!document.querySelector('[data-view="queues"]').hidden) loadQueues(); }, 3000);

// The address a site gets on the user's domain (the default name).
function shareAddress(host, as) {
  const label = as || host.replace(/\.[^.]+$/, "");
  return `${(shareCfg?.pattern || "{site}").replace("{site}", label)}.${status.shareDomain}`;
}

// The "▾" panel on a site row: share under another name, or on a random link.
function shareOptions(s) {
  const input = el("input", { type: "text", class: "asname", placeholder: s.host.replace(/\.[^.]+$/, ""),
    "aria-label": `Address name for ${s.host}`, maxlength: "63", spellcheck: "false", autocomplete: "off" });
  const preview = el("span", { class: "mono preview" }, shareAddress(s.host));
  input.oninput = () => { input.value = input.value.toLowerCase(); preview.textContent = shareAddress(s.host, input.value.trim()); };
  input.onkeydown = (e) => { if (e.key === "Enter") go(); };
  const go = () => { shareOpts = ""; shareSite(s.host, { as: input.value.trim() }); };
  return el("div", { class: "shared opts" },
    el("label", { class: "aslabel" }, "Share as ", input),
    preview,
    el("span", { class: "acts" },
      el("button", { class: "primary", onclick: go }, "Share"),
      el("button", { onclick: () => { shareOpts = ""; shareSite(s.host, { quick: true }); } }, "Random link instead"),
      el("button", { class: "linkish", onclick: () => { shareOpts = ""; render(true); } }, "Cancel")));
}

// Services → Sharing: connect `laman share` to the user's own domain.
let shareCfg = null;     // GET /api/share/setup
let shareEditing = false; // the connect form is open while already connected
let shareTyped = "";      // the domain last submitted, kept if setup fails

async function loadShareSetup() {
  try { shareCfg = await api("GET", "/api/share/setup"); } catch { return; }
  render();
}

function renderSharing() {
  const c = shareCfg;
  const sharing = $("sharing");
  const box = { replaceChildren: (...kids) => sharing.replaceChildren(...kids.filter(Boolean)) };
  if (c.running) {
    box.replaceChildren(
      el("p", { class: "step" }, el("span", { class: "spin", "aria-hidden": "true" }), c.step || "Working…"),
      c.loginUrl ? el("p", {}, "Cloudflare's sign-in should have opened in your browser. Approve it there and choose ",
        el("b", {}, c.domain || "your domain"), ". If nothing opened, ",
        el("a", { href: c.loginUrl, target: "_blank", rel: "noopener" }, "open the sign-in page ↗"), ".") : null,
      el("div", { class: "row-acts" }, el("button", { onclick: () => act(async () => { await api("POST", "/api/share/setup/cancel"); await loadShareSetup(); }) }, "Cancel")));
    return;
  }
  const err = c.error ? el("p", { class: "err" }, c.error) : null;
  if (c.connected && !shareEditing) {
    box.replaceChildren(
      el("p", {}, "Shared sites get fixed addresses on ", el("b", {}, c.domain), ", like ",
        el("span", { class: "mono" }, `https://${status.shareDomain ? shareAddress(status.sites.find((x) => !x.app)?.host || "blog.test") : c.example}`), ". The Share button on each site uses it; its ▾ menu can share under another name or on a random link."),
      err,
      el("div", { class: "row-acts" },
        el("button", { onclick: () => { shareEditing = true; render(true); } }, "Change"),
        el("button", { class: "linkish", onclick: () => act(async () => { shareEditing = false; shareCfg = await api("POST", "/api/share/setup/off"); }) }, "Use random links instead")));
    return;
  }
  const domain = el("input", { type: "text", id: "sharedomain", placeholder: "example.com", value: shareTyped || c.domain || "",
    "aria-label": "Your domain", spellcheck: "false", autocomplete: "off" });
  // One of the user's own sites makes the example concrete.
  const sample = (status.sites.find((x) => !x.app)?.host || "blog.test").replace(/\.[^.]+$/, "");
  const dev = el("input", { type: "checkbox", checked: c.pattern === "{site}-dev" });
  const preview = el("span", { class: "mono" });
  const sync = () => {
    const d = domain.value.trim().toLowerCase() || "example.com";
    preview.textContent = `https://${sample}${dev.checked ? "-dev" : ""}.${d}`;
  };
  domain.oninput = sync;
  dev.onchange = sync;
  sync();
  const connect = () => {
    const d = domain.value.trim().toLowerCase();
    if (!d) { domain.focus(); return; }
    shareEditing = false;
    shareTyped = d;
    act(async () => { shareCfg = await api("POST", "/api/share/setup", { domain: d, pattern: dev.checked ? "{site}-dev" : "{site}" }); });
  };
  domain.onkeydown = (e) => { if (e.key === "Enter") connect(); };
  box.replaceChildren(
    el("p", {}, c.connected ? "Switch to another domain, or change how addresses are named." :
      "Shares get random trycloudflare.com links that change each time. With your own domain they get fixed addresses, handy for webhooks and links you send to clients. The domain's DNS must be on Cloudflare; the free plan is enough."),
    el("div", { class: "form" },
      el("label", {}, el("span", {}, "Your domain"), domain)),
    el("p", { class: "example" }, `Each site gets its own address. ${sample}.test would be shared at `, preview),
    el("label", { class: "check" }, dev,
      el("span", {}, "Add ", el("span", { class: "mono" }, "-dev"), " to every address, to keep them apart from subdomains you already use on this domain")),
    el("div", { class: "row-acts" },
      el("button", { class: "primary", onclick: connect }, "Connect to Cloudflare"),
      c.connected ? el("button", { class: "linkish", onclick: () => { shareEditing = false; render(true); } }, "Cancel") : null),
    el("p", { class: "fine" }, "Your browser opens Cloudflare's sign-in: approve it there and choose the domain. laman never sees your password, and keeps this sign-in apart from any cloudflared setup of your own."),
    err);
}

setInterval(() => {
  if (!document.querySelector('[data-view="services"]').hidden || shareCfg?.running) loadShareSetup();
}, 2000);

// Project folders: every folder inside one becomes a site. Removing only
// stops serving it, but asks twice since its sites disappear.
let unfolding = null;   // folder waiting for its second "Remove" click: {path, until}
let picking = false;    // Windows' folder picker is open
let naming = "";        // folder whose name prefix is being edited

const normDir = (p) => (p || "").replace(/\//g, "\\").replace(/\\+$/, "").toLowerCase();
const slug = (v) => v.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "");

function renderFolders() {
  const folders = status.folders || (status.sitesDirs || [status.sitesDir]).map((dir) => ({ dir }));
  const dirs = folders.map((f) => f.dir);
  const count = (dir) => status.sites.filter((s) => !s.app && s.folder && normDir(s.folder) === normDir(dir)).length;
  const rows = folders.map(({ dir, prefix }, i) => {
    const n = count(dir);
    const waiting = unfolding && unfolding.path === dir && Date.now() < unfolding.until;
    const example = status.sites.find((s) => !s.app && s.folder && normDir(s.folder) === normDir(dir));
    const base = example ? (prefix ? example.name.slice(prefix.length + 1) : example.name) : "<folder>";
    let names;
    if (naming === dir) {
      const box = el("input", { type: "text", class: "fadd fprefix", value: prefix || slug(dir.split(/[\\/]/).filter(Boolean).pop() || ""),
        "aria-label": `Name prefix for sites in ${dir}`, spellcheck: "false", autocomplete: "off", maxlength: "40" });
      const preview = el("span", { class: "mono" });
      const show = () => { const p = slug(box.value); preview.textContent = `${p ? p + "-" : ""}${base}.test`; };
      const save = () => { naming = ""; box.blur(); act(() => api("POST", "/api/folders/prefix", { path: dir, prefix: box.value })); };
      box.oninput = show;
      box.onkeydown = (e) => { if (e.key === "Enter") save(); if (e.key === "Escape") { naming = ""; box.blur(); render(true); } };
      show();
      setTimeout(() => { box.focus(); box.select(); });
      names = el("div", { class: "fnames editing" },
        el("label", {}, "Put in front of every name: ", box), el("span", { class: "why" }, "e.g. ", preview),
        el("button", { onclick: save }, "Save"),
        el("button", { class: "linkish", onclick: () => { naming = ""; render(true); } }, "Cancel"));
    } else {
      names = el("div", { class: "fnames" },
        "Sites are named ", el("span", { class: "mono" }, `${prefix ? prefix + "-" : ""}${base}.test`),
        el("button", { class: "linkish", onclick: () => { naming = dir; render(true); } }, prefix ? "Change" : "Add a prefix"),
        prefix ? el("button", { class: "linkish", onclick: () => act(() => api("POST", "/api/folders/prefix", { path: dir, prefix: "" })) }, `Use ${base}.test`) : null);
    }
    return el("div", { class: "folder" },
      el("div", { class: "fpath" },
        el("span", { class: "mono", title: dir }, tidy(dir)),
        i === 0 ? el("span", { class: "tag" }, "main") : null),
      el("span", { class: "fcount" }, `${n} site${n === 1 ? "" : "s"}`),
      el("span", { class: "facts" },
        el("button", { class: "linkish", onclick: () => act(() => api("POST", "/api/folders/open", { path: dir })) }, "Open"),
        dirs.length > 1 ? el("button", {
          class: waiting ? "confirm" : "linkish",
          onclick: () => {
            if (!waiting) {
              unfolding = { path: dir, until: Date.now() + 6000 };
              render(true);
              setTimeout(() => render(true), 6100);
              return;
            }
            unfolding = null;
            act(() => api("POST", "/api/folders/remove", { path: dir }));
          },
        }, waiting ? `Confirm: stop serving ${n} site${n === 1 ? "" : "s"}` : "Remove") : null),
      names);
  });
  const typed = el("input", { type: "text", class: "fadd", placeholder: "or paste a folder path, e.g. C:\\laragon\\www",
    "aria-label": "Folder path to add", spellcheck: "false", autocomplete: "off" });
  const addTyped = () => {
    const path = typed.value.trim().replace(/^"(.*)"$/, "$1");
    if (!path) { typed.focus(); return; }
    act(() => api("POST", "/api/folders/add", { path }));
  };
  typed.onkeydown = (e) => { if (e.key === "Enter") addTyped(); };
  $("folders").replaceChildren(...rows,
    el("div", { class: "folder add" },
      el("button", { disabled: picking, onclick: pickFolder }, picking ? "Choose a folder in the window that opened…" : "Add folder…"),
      typed,
      el("button", { class: "linkish", onclick: addTyped }, "Add")));
}

async function pickFolder() {
  picking = true;
  render(true);
  try {
    const { Path } = await api("POST", "/api/folders/pick");
    if (Path) await api("POST", "/api/folders/add", { path: Path });
  } catch (e) { alertNote(e.message); }
  picking = false;
  drawn = {};
  refresh();
}

// Views: one per rail entry, chosen by the URL hash so reloads keep it.
const VIEWS = ["home", "folders", "apps", "queues", "versions", "extensions", "services", "logs"];
const currentView = () => (VIEWS.includes(location.hash.slice(1)) ? location.hash.slice(1) : "home");
function showView() {
  const view = currentView();
  for (const sec of document.querySelectorAll(".view")) {
    const on = sec.dataset.view === view;
    if (on && sec.hidden) { sec.classList.remove("enter"); void sec.offsetWidth; sec.classList.add("enter"); }
    sec.hidden = !on;
  }
  for (const a of document.querySelectorAll("#nav a")) {
    if (a.dataset.view === view) a.setAttribute("aria-current", "page"); else a.removeAttribute("aria-current");
  }
  if (view === "logs") loadLog(true);
  if (view === "queues") loadQueues();
  if (view === "services") loadShareSetup();
  render();
}
window.addEventListener("hashchange", showView);

// Add-on servers can be turned off; caddy and the PHP workers are laman.
const ADDONS = ["mariadb", "postgres", "redis", "mailpit"];

function toggle(kind, on) {
  const input = el("input", { type: "checkbox", role: "switch", "aria-label": `${kind} starts with laman`, checked: on });
  input.onchange = () => {
    input.disabled = true;
    act(() => api("POST", "/api/services/enable", { kind, enabled: input.checked }));
  };
  return el("label", { class: "switch" }, input);
}

const expanded = new Set(); // PHP versions whose workers are shown

const stateDot = (state) => el("span", { class: "dot " + (state === "running" ? "ok" : state === "stopped" || state === "off" ? "" : "warn"), style: "display:inline-block;margin-right:6px" });

const SERVICE_NAMES = { caddy: "Caddy (web)", mariadb: "MariaDB", postgres: "PostgreSQL", redis: "Redis", mailpit: "Mailpit" };
const label = (name) => SERVICE_NAMES[name] || name;

function serviceRow(s, cls) {
  return el("tr", cls ? { class: cls } : {},
    el("td", {}, cls === "worker" ? el("span", { class: "muted mono" }, s.name) : label(s.name)),
    el("td", {}, stateDot(s.state), s.state),
    el("td", {}, String(s.restarts)),
    el("td", { class: s.lastError && s.state !== "running" ? "err" : "muted", "data-up": s.state === "running" ? s.name : undefined },
      s.state === "running" ? s.uptime || "" : s.lastError || ""),
    el("td", {}, ADDONS.includes(s.name) ? toggle(s.name, true) : null));
}

// PHP runs as several php-cgi workers per version (each serves one request
// at a time). They show as one row per version; the workers on request.
function renderServices() {
  const rows = [];
  const groups = new Map(); // "8.5" -> workers, in order of first appearance
  for (const s of status.services) {
    const m = /^php(\d+\.\d+)-\d+$/.exec(s.name);
    if (!m) continue;
    if (!groups.has(m[1])) groups.set(m[1], []);
    groups.get(m[1]).push(s);
  }
  const done = new Set();
  for (const s of status.services) {
    const m = /^php(\d+\.\d+)-\d+$/.exec(s.name);
    if (!m) { rows.push(serviceRow(s)); continue; }
    if (done.has(m[1])) continue;
    done.add(m[1]);
    const ws = groups.get(m[1]);
    const up = ws.filter((w) => w.state === "running").length;
    const failing = ws.find((w) => w.state !== "running" && w.lastError);
    const open = expanded.has(m[1]);
    const btn = el("button", { class: "linkish", "aria-expanded": open ? "true" : "false",
      onclick: () => { open ? expanded.delete(m[1]) : expanded.add(m[1]); render(); } },
      `${open ? "▾" : "▸"} ${ws.length} worker${ws.length > 1 ? "s" : ""}`);
    rows.push(el("tr", {},
      el("td", {}, `PHP ${m[1]}`),
      el("td", {}, stateDot(up === ws.length ? "running" : "warn"), up === ws.length ? "running" : `${up} of ${ws.length} running`),
      el("td", {}, String(ws.reduce((n, w) => n + w.restarts, 0))),
      el("td", { class: failing ? "err" : "muted" }, failing ? failing.lastError : btn),
      el("td", {}, failing ? btn : null)));
    if (open) for (const w of ws) rows.push(serviceRow(w, "worker"));
  }
  for (const kind of status.off || []) {
    rows.push(el("tr", { class: "off" },
      el("td", {}, label(kind)),
      el("td", {}, stateDot("off"), "off"),
      el("td", {}, "-"),
      el("td", {}, "turned off"),
      el("td", {}, toggle(kind, false))));
  }
  $("services").replaceChildren(...rows);
}

// lineOf mirrors the server: PostgreSQL and Node lines are the major alone,
// the rest are major.minor.
const lineOf = (kind, v) => {
  const parts = short(v).split(".");
  return kind === "postgres" || kind === "node" ? parts[0] : parts.slice(0, 2).join(".");
};
const newer = (a, b) => {
  const pa = a.split(".").map(Number), pb = b.split(".").map(Number);
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) if ((pa[i] || 0) !== (pb[i] || 0)) return (pa[i] || 0) > (pb[i] || 0);
  return false;
};

function renderVersions() {
  const busy = vers.job.running;
  $("vers").replaceChildren(...vers.versions.map((v) => {
    const others = v.installed.filter((x) => x !== v.active).reverse();
    const have = new Set(v.installed.map((x) => lineOf(v.kind, x)));
    const lines = avail[v.kind] || [];
    const addable = lines.filter((l) => !have.has(l));

    let state = null;
    if (!v.active) state = el("span", { class: "tag" }, "not installed");
    else if (v.server && !v.enabled) state = el("span", { class: "tag" }, "off");
    else if (v.server && !v.running) state = el("span", { class: "tag err" }, "not running");

    const pick = (items, label, fmt) => el("select", { "aria-label": label, disabled: busy }, items.map((x) => el("option", { value: x }, fmt(x))));

    let use = el("td", { class: "empty" }, v.installed.length ? "only one installed" : "");
    if (others.length) {
      const sel = pick(others, `${v.name} version to use`, (x) => short(x));
      use = el("td", {}, el("span", { class: "pair" }, sel,
        el("button", { disabled: busy, onclick: () => act(() => api("POST", "/api/versions/use", { kind: v.kind, version: sel.value })) }, "Use")));
    }

    let add = el("td", { class: "empty" }, lines.length ? "all installed" : "…");
    if (addable.length) {
      const sel = pick(addable, `${v.name} version to install`, (x) => x);
      add = el("td", {}, el("span", { class: "pair" }, sel,
        el("button", { disabled: busy, onclick: () => act(() => api("POST", "/api/versions/install", { kind: v.kind, line: sel.value })) }, "Install")));
    }

    let up = el("td", { class: "empty" }, v.server ? (v.active ? "on the newest" : "") : "n/a");
    const targets = v.server && v.active ? lines.filter((l) => newer(l, lineOf(v.kind, v.active))) : [];
    if (targets.length) {
      const sel = pick(targets, `${v.name} line to upgrade to`, (x) => `to ${x}`);
      const waiting = pending && pending.kind === v.kind && Date.now() < pending.until;
      if (waiting) sel.value = pending.line;
      const btn = el("button", {
        class: waiting ? "confirm" : "", disabled: busy,
        title: "Copies your data to the newer version and restarts laman; the old version's data is kept as a way back",
        onclick: () => {
          if (!waiting || pending.line !== sel.value) {
            pending = { kind: v.kind, line: sel.value, until: Date.now() + 8000 };
            render(true);
            setTimeout(() => render(true), 8100);
            return;
          }
          pending = null;
          act(async () => {
            await api("POST", "/api/versions/upgrade", { kind: v.kind, line: sel.value });
            alertNote(`Upgrading ${v.name} to ${sel.value}. laman restarts for this, and the page reconnects by itself. Progress is in Logs > Upgrade.`, "note info");
            location.hash = "#logs";
            showLog("upgrade");
          });
        },
      }, waiting ? `Confirm: restart and upgrade to ${pending.line}` : "Upgrade…");
      up = el("td", {}, el("span", { class: "pair" }, sel, btn));
    }

    return el("tr", {},
      el("td", {}, el("strong", {}, v.name)),
      el("td", {}, v.active ? el("span", { class: "ver" }, short(v.active)) : null, state),
      use, add, up);
  }));
  $("job").className = "job " + (vers.job.error ? "err" : "muted");
  $("job").textContent = jobText("versions", vers.job);
}

// PHP extensions: shipped ones get a switch; extras can be installed and
// uninstalled. Enabled first, then the rest alphabetically.
function renderExts() {
  const installed = vers?.versions.find((v) => v.kind === "php")?.installed || [];
  const pick = $("extphp");
  if (document.activeElement !== pick) {
    pick.replaceChildren(...installed.slice().reverse().map((v) => el("option", { value: v, selected: v === exts.php }, `PHP ${short(v)}`)));
  }
  const q = $("extfilter").value.trim().toLowerCase();
  const busy = exts.job.running;
  const list = exts.exts
    .filter((e) => !q || e.name.includes(q) || (e.note || "").toLowerCase().includes(q))
    .sort((a, b) => (b.enabled - a.enabled) || (b.present - a.present) || a.name.localeCompare(b.name));
  $("exts").replaceChildren(...list.map((e) => {
    let control;
    if (!e.present) {
      control = el("button", { disabled: busy, onclick: () => act(() => api("POST", "/api/ext/install", { php: exts.php, name: e.name })) }, "Install");
    } else {
      const input = el("input", { type: "checkbox", role: "switch", "aria-label": `${e.name} enabled`, checked: e.enabled, disabled: busy });
      input.onchange = () => {
        input.disabled = true;
        act(() => api("POST", "/api/ext/enable", { php: exts.php, name: e.name, enabled: input.checked }));
      };
      control = el("label", { class: "switch" }, input);
    }
    const extra = e.installable && e.present
      ? el("button", { class: "linkish", disabled: busy, title: "Remove its files", onclick: () => act(() => api("POST", "/api/ext/uninstall", { php: exts.php, name: e.name })) }, "uninstall")
      : null;
    return el("div", { class: "ext" + (e.present ? "" : " missing") },
      control,
      el("span", { class: "what" }, el("span", { class: "name" }, e.name), el("span", { class: "desc", title: e.note || "" }, e.note || (e.installable ? "extra" : ""))),
      extra);
  }));
  if (!list.length) $("exts").replaceChildren(el("p", { class: "muted" }, "No extension matches."));
  $("extjob").className = "job " + (exts.job.error ? "err" : "muted");
  $("extjob").textContent = jobText("exts", exts.job);
}

// Blur first: redraws pause while a dropdown has focus.
$("extphp").onchange = (e) => { extPHP = e.target.value; e.target.blur(); drawn.exts = ""; refresh(); };
$("extfilter").oninput = () => render();

async function loadAvailable() {
  for (const kind of ["php", "node", "mariadb", "postgres", "redis"]) {
    try {
      const r = await api("GET", `/api/versions/available?kind=${kind}`);
      avail[kind] = r.lines;
    } catch { /* offline: the Add column stays empty */ }
  }
  render(true);
}

// Theme: light, dark or "" (follow Windows). Saved in laman's config so the
// localhost page matches; theme.js applies the last choice before paint.
let themeSaving = null;
function applyTheme(t) {
  if (t) document.documentElement.dataset.theme = t;
  else delete document.documentElement.dataset.theme;
  try { t ? localStorage.setItem("laman.theme", t) : localStorage.removeItem("laman.theme"); } catch { /* private window */ }
  for (const b of $("theme").querySelectorAll("button")) {
    b.setAttribute("aria-checked", String(b.dataset.theme === t));
    b.tabIndex = b.dataset.theme === t ? 0 : -1;
  }
}
applyTheme(document.documentElement.dataset.theme || "");
for (const b of $("theme").querySelectorAll("button")) {
  b.addEventListener("click", async () => {
    const t = b.dataset.theme;
    applyTheme(t);
    themeSaving = t;
    try { await api("POST", "/api/theme", { theme: t }); } catch (e) { alertNote(e.message); }
    if (themeSaving === t) themeSaving = null;
  });
}
$("theme").addEventListener("keydown", (e) => {
  const list = [...$("theme").querySelectorAll("button")];
  const i = list.indexOf(document.activeElement);
  if (i < 0 || !["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown"].includes(e.key)) return;
  e.preventDefault();
  const next = list[(i + (e.key === "ArrowLeft" || e.key === "ArrowUp" ? list.length - 1 : 1)) % list.length];
  next.focus();
  next.click();
});

async function refresh() {
  try {
    [status, vers, exts, apps] = await Promise.all([api("GET", "/status"), api("GET", "/api/versions"),
      api("GET", `/api/ext?php=${encodeURIComponent(extPHP)}`).catch(() => exts),
      api("GET", "/api/apps").catch(() => apps)]);
    document.body.classList.remove("down");
    if (themeSaving === null && (status.theme || "") !== (document.documentElement.dataset.theme || "")) applyTheme(status.theme || "");
    render();
  } catch {
    document.body.classList.add("down");
    $("dot").className = "dot bad";
    $("state").textContent = "stopped";
    drawn = {};
  }
}

let checkingUpdate = false;
async function checkUpdate() {
  checkingUpdate = true;
  render();
  try {
    const u = await api("POST", "/api/update/check");
    status.update = u;
    alertNote(u.error ? `Couldn't check for updates: ${u.error}`
      : u.available ? `laman ${u.available} is available.` : `laman ${u.current} is up to date.`,
      u.error ? "note err" : "note info");
  } catch (e) { alertNote(e.message); }
  checkingUpdate = false;
  render(true);
}

async function act(fn) {
  lastAct = Date.now();
  try { await fn(); } catch (e) { alertNote(e.message); }
  drawn = {};
  refresh();
}

// alertNote floats a message bottom-right. The same message again bumps a
// counter on the toast that is already there instead of stacking a copy.
// Errors stay longer; hovering a toast keeps it until the pointer leaves.
const toasts = new Map(); // msg -> {node, count, timer}
function alertNote(msg, cls = "note err") {
  const kind = /\binfo\b/.test(cls) ? "info" : /\berr\b/.test(cls) ? "err" : "warn";
  const life = kind === "err" ? 12000 : 8000;
  let t = toasts.get(msg);
  if (t) {
    t.count++;
    t.node.querySelector(".times").textContent = `×${t.count}`;
  } else {
    const node = el("div", { class: `toast ${kind}` },
      el("div", { class: "msg" }, msg, el("span", { class: "times" })),
      el("button", { class: "x", "aria-label": "Dismiss", title: "Dismiss", onclick: () => dropToast(msg) }, "×"));
    t = { node, count: 1, timer: 0 };
    node.addEventListener("mouseenter", () => clearTimeout(t.timer));
    node.addEventListener("mouseleave", () => { t.timer = setTimeout(() => dropToast(msg), 4000); });
    toasts.set(msg, t);
    $("toasts").append(node);
    // Keep the stack short: the oldest goes first.
    if (toasts.size > 4) dropToast(toasts.keys().next().value);
  }
  clearTimeout(t.timer);
  t.timer = setTimeout(() => dropToast(msg), life);
}

function dropToast(msg) {
  const t = toasts.get(msg);
  if (!t) return;
  toasts.delete(msg);
  clearTimeout(t.timer);
  t.node.classList.add("leaving");
  const gone = () => t.node.remove();
  t.node.addEventListener("animationend", gone, { once: true });
  setTimeout(gone, 300); // reduced motion: no animation, so no animationend
}

async function shareSite(host, opts = {}) {
  opening = { ...opening, [host]: true };
  render();
  try { await api("POST", "/api/share", { site: host, ...opts }); } catch (e) { alertNote(e.message); }
  const { [host]: _, ...rest } = opening;
  opening = rest;
  drawn = {};
  refresh();
}

async function copyLink(host, url) {
  try { await navigator.clipboard.writeText(url); } catch { return alertNote("Could not copy; select the link instead."); }
  copied = host;
  render();
  setTimeout(() => { if (copied === host) { copied = ""; render(); } }, 2000);
}

const setSitePHP = (site, version) => act(() => api("POST", "/api/sites/php", { site, version }));
$("reload").onclick = () => act(() => api("POST", "/reload"));
$("opensites").onclick = () => {
  if ((status?.sitesDirs || []).length > 1) location.hash = "#folders";
  else act(() => api("POST", "/api/sites/open-folder"));
};
// Stop takes down every server, so it asks first (like Remove).
let stopAsk = 0;
const TITLE_STOP = $("stop").title;
$("stop").onclick = () => {
  const b = $("stop");
  if (Date.now() > stopAsk) {
    stopAsk = Date.now() + 6000;
    b.classList.add("confirm");
    b.title = "Click again within 6 seconds to stop the web server, PHP and every database";
    b.textContent = "Stop all?";
    setTimeout(() => { if (Date.now() >= stopAsk) { b.classList.remove("confirm"); b.textContent = "Stop"; b.title = TITLE_STOP; } }, 6050);
    return;
  }
  stopAsk = 0;
  b.classList.remove("confirm");
  b.textContent = "Stop";
  b.title = TITLE_STOP;
  act(() => api("POST", "/stop"));
};

function showLog(name) {
  logName = name;
  for (const o of $("tabs").querySelectorAll("button")) {
    o.setAttribute("aria-selected", String(o.dataset.log === name));
    o.tabIndex = o.dataset.log === name ? 0 : -1;
  }
  $("log").setAttribute("aria-labelledby", `tab-${name}`);
  loadLog(true);
}
for (const b of $("tabs").querySelectorAll("button")) b.onclick = () => showLog(b.dataset.log);
$("tabs").addEventListener("keydown", (e) => {
  const list = [...$("tabs").querySelectorAll("button")];
  const i = list.indexOf(document.activeElement);
  const step = { ArrowLeft: -1, ArrowRight: 1, Home: -i, End: list.length - 1 - i }[e.key];
  if (i < 0 || step === undefined) return;
  e.preventDefault();
  const next = list[(i + step + list.length) % list.length];
  next.focus();
  showLog(next.dataset.log);
});

async function loadLog(jump) {
  const pre = $("log");
  const atBottom = jump || pre.scrollHeight - pre.scrollTop - pre.clientHeight < 40;
  try {
    const text = await api("GET", `/api/logs?name=${logName}&lines=400`);
    if (pre.textContent !== text) pre.textContent = text;
  } catch {
    return;
  }
  if (atBottom) pre.scrollTop = pre.scrollHeight;
}

showView();
refresh();
loadShareSetup(); // site rows need the address style
loadAvailable();
setInterval(refresh, 2000);
setInterval(() => { if (!document.querySelector('[data-view="logs"]').hidden) loadLog(false); }, 3000);

$("sitefind").addEventListener("input", () => render());
$("sitefind").addEventListener("keydown", (e) => {
  if (e.key === "Escape") { if (e.target.value) { e.target.value = ""; render(); } else e.target.blur(); }
  if (e.key === "Enter") {
    const first = $("sites").querySelector(".site .host");
    if (first && e.target.value.trim()) window.open(first.href, "_blank", "noopener");
  }
});

// "/" jumps to the view's search box, as on the localhost page.
const FINDS = { home: "sitefind", queues: "qfind", extensions: "extfilter" };
document.addEventListener("keydown", (e) => {
  if (e.key !== "/" || e.ctrlKey || e.metaKey || e.altKey) return;
  const t = e.target;
  if (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName)) return;
  const box = $(FINDS[currentView()] || "");
  if (!box || box.hidden) return;
  e.preventDefault();
  box.focus();
  box.select();
});
$("skip").addEventListener("click", (e) => { e.preventDefault(); $("main").focus(); });

// A site's "start it" page links here with ?start=<name>; a group's or
// another site's page with ?site=<name>. Show that row and, for ?start,
// ask before running anything (a link alone never starts a server).
(function siteFromURL() {
  const p = new URLSearchParams(location.search);
  const start = p.get("start"), site = p.get("site");
  if (!start && !site) return;
  history.replaceState(null, "", location.pathname + (location.hash || "#home"));
  devAsk = start || "";
  flashSite = site || "";
  const name = start || site;
  let tries = 0;
  const show = () => {
    const s = status?.sites.find((x) => x.name === name);
    if (!s) { if (++tries < 40) setTimeout(show, 300); return; }
    foldChosen = true;
    if (s.kind === "none") folded.add(NONE);
    else if (s.folder) folded.delete(s.folder);
    render(true);
    document.getElementById(`site-${name}`)?.scrollIntoView({ block: "center" });
    setTimeout(() => { flashSite = ""; render(true); }, 6000);
  };
  show();
})();

setInterval(() => { if (devOpen.size && currentView() === "home") loadDevOut(); }, 2000);
