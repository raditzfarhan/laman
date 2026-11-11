package caddyconf

import (
	"fmt"
	"html"
	"path/filepath"
	"strings"

	"laman/internal/sites"
)

// LandingInput is what the page for localhost and unknown hosts shows.
type LandingInput struct {
	Sites     []sites.Site
	Folders   []sites.Folder // project folders, in the dashboard's order
	HTTPS     bool           // links use HTTPS (the CA is trusted)
	Port      int
	Dashboard string // the dashboard's address
	Theme     string // "light", "dark" or "" to follow the system
}

// Landing renders the page served for localhost and unknown hosts: the
// sites grouped by project folder, like the dashboard's Home list. It is
// only reachable from this computer (Caddy binds 127.0.0.1).
func Landing(in LandingInput) string {
	type group struct {
		title, path string
		list        []sites.Site
	}
	var groups []*group
	byDir := map[string]*group{}
	for _, f := range in.Folders {
		g := &group{title: filepath.Base(f.Dir), path: f.Dir}
		groups = append(groups, g)
		byDir[strings.ToLower(filepath.Clean(f.Dir))] = g
	}
	linked := &group{title: "Linked", path: "folders served with laman link"}
	tools := &group{title: "Tools", path: "apps laman installed"}
	none := &group{title: "Not websites", path: "nothing a browser can open"}
	for _, s := range in.Sites {
		g := byDir[strings.ToLower(filepath.Clean(s.Folder))]
		switch {
		case s.App != "":
			g = tools
		case s.Kind == sites.KindNone:
			g = none
		case s.Folder == "" || g == nil:
			g = linked
		}
		g.list = append(g.list, s)
	}
	groups = append(groups, linked, tools, none)
	used := 0
	for _, g := range groups {
		if len(g.list) > 0 {
			used++
		}
	}

	var b strings.Builder
	theme := ""
	if in.Theme == "light" || in.Theme == "dark" {
		theme = fmt.Sprintf(` data-theme="%s"`, in.Theme)
	}
	fmt.Fprintf(&b, `<!doctype html>
<html lang="en"%s>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>laman</title>
<style>%s</style>
</head>
<body>
<main>
<header>
  %s
  <a class="dash" href="%s">Open dashboard</a>
</header>
`, theme, landingCSS, landingLogo, html.EscapeString(in.Dashboard))

	if len(in.Sites) == 0 {
		b.WriteString(`<div class="empty"><h1>No sites yet</h1><p>Make a folder inside one of your project folders and it shows up here as <code>&lt;folder&gt;.test</code>.</p></div>`)
	} else {
		n := len(in.Sites) - len(tools.list) // like the dashboard: tools are counted apart
		plural := "s"
		if n == 1 {
			plural = ""
		}
		fmt.Fprintf(&b, `<div class="head"><h1>Your sites <span class="n">%d site%s</span></h1>`, n, plural)
		if n >= 8 {
			b.WriteString(`<input id="find" type="search" placeholder="Find a site" aria-label="Find a site" autocomplete="off" autofocus>`)
		}
		b.WriteString(`</div><div class="panel" id="list">`)
		for _, g := range groups {
			if len(g.list) == 0 {
				continue
			}
			b.WriteString(`<section class="grp">`)
			if used > 1 {
				fmt.Fprintf(&b, `<h2><span class="gname">%s</span><span class="gpath">%s</span><span class="gcount">%d</span></h2>`,
					html.EscapeString(g.title), html.EscapeString(g.path), len(g.list))
			}
			for _, s := range g.list {
				tag := ""
				switch s.Kind {
				case sites.KindNode:
					tag = s.Framework
				case sites.KindProxy:
					tag = "proxy"
				case sites.KindGroup:
					tag = "projects inside"
				}
				if tag != "" {
					tag = `<span class="tag">` + html.EscapeString(tag) + `</span>`
				}
				where := s.Path
				if s.Kind == sites.KindProxy {
					where = "→ " + s.Proxy
				}
				fmt.Fprintf(&b, `<a class="site" href="%s"><span class="host">%s</span>%s<span class="path">%s</span></a>`,
					html.EscapeString(URL(s.Host, in.HTTPS, in.Port)), html.EscapeString(s.Host), tag, html.EscapeString(where))
			}
			b.WriteString(`</section>`)
		}
		b.WriteString(`<p class="none" id="none" hidden>No site matches.</p></div>`)
		b.WriteString(`<script>` + landingJS + `</script>`)
	}
	b.WriteString("\n<p class=\"foot\">Only this computer can open this page. It shows for <code>localhost</code> and any <code>.test</code> name without a site.</p>\n</main>\n</body>\n</html>\n")
	return b.String()
}

const landingLogo = `<svg class="logo" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 835 256" role="img" aria-label="laman"><path fill="currentColor" d="M37.01 166.48L150.51 52.97L135.50 37.97L22.00 151.47ZM105.49 52.97L218.99 166.48L234.00 151.47L120.50 37.97ZM120.50 128.00L158.01 165.51L120.50 203.03L135.50 218.03L188.02 165.51L135.50 112.99Z"/><rect x="198.63" y="196.81" width="35.37" height="21.22" fill="#E2583E"/><path transform="translate(280.00 218.03) scale(1)" fill="currentColor" d="M0.0 0H21.0V-142L0.0 -121Z"/><path transform="translate(280.00 218.03) scale(1)" fill="currentColor" fill-rule="evenodd" d="M37.0 -48A48 48 0 1 0 133.0 -48A48 48 0 1 0 37.0 -48ZM58.0 -48A27 27 0 1 0 112.0 -48A27 27 0 1 0 58.0 -48Z"/><path transform="translate(280.00 218.03) scale(1)" fill="currentColor" d="M112.0 0H133.0V-96H112.0Z"/><path transform="translate(280.00 218.03) scale(1)" fill="currentColor" d="M153.0 0H174.0V-96H153.0Z"/><path transform="translate(280.00 218.03) scale(1)" fill="currentColor" d="M153.0 0V-53A43 43 0 0 1 239.0 -53V0H218.0V-53A22 22 0 0 0 174.0 -53V0Z"/><path transform="translate(280.00 218.03) scale(1)" fill="currentColor" d="M218.0 0V-53A43 43 0 0 1 304.0 -53V0H283.0V-53A22 22 0 0 0 239.0 -53V0Z"/><path transform="translate(280.00 218.03) scale(1)" fill="currentColor" fill-rule="evenodd" d="M321.0 -48A48 48 0 1 0 417.0 -48A48 48 0 1 0 321.0 -48ZM342.0 -48A27 27 0 1 0 396.0 -48A27 27 0 1 0 342.0 -48Z"/><path transform="translate(280.00 218.03) scale(1)" fill="currentColor" d="M396.0 0H417.0V-96H396.0Z"/><path transform="translate(280.00 218.03) scale(1)" fill="currentColor" d="M437.0 0H458.0V-96H437.0Z"/><path transform="translate(280.00 218.03) scale(1)" fill="currentColor" d="M437.0 0V-48A48 48 0 0 1 533.0 -48V0H512.0V-48A27 27 0 0 0 458.0 -48V0Z"/></svg>`

// Same palette as the dashboard (logo/kit: Malam, Pasir, Genting).
const landingCSS = `
:root { --bg: #f4ede1; --panel: #ffffff; --ink: #14213d; --muted: #5a6275; --line: #e4dccd; --line-strong: #d3c9b6; --code: #f7f2e9; --genting: #e2583e; --hover: #fbf7f0;
  --mono: ui-monospace, "Cascadia Mono", "Cascadia Code", Consolas, monospace; color-scheme: light; }
@media (prefers-color-scheme: dark) { :root:not([data-theme="light"]) { --bg: #0e172b; --panel: #14213d; --ink: #f4ede1; --muted: #aab1c2; --line: #24324f; --line-strong: #33436a; --code: #101a31; --hover: #18284a; color-scheme: dark; } }
:root[data-theme="dark"] { --bg: #0e172b; --panel: #14213d; --ink: #f4ede1; --muted: #aab1c2; --line: #24324f; --line-strong: #33436a; --code: #101a31; --hover: #18284a; color-scheme: dark; }
* { box-sizing: border-box; }
body { margin: 0; background: var(--bg); color: var(--ink); font: 14px/1.5 "Segoe UI", system-ui, sans-serif; -webkit-font-smoothing: antialiased; }
main { max-width: 760px; margin: 0 auto; padding: 36px 16px 56px; }
header { display: flex; align-items: center; justify-content: space-between; gap: 16px; margin-bottom: 32px; color: var(--ink); }
.logo { height: 28px; width: auto; display: block; }
.dash { color: var(--ink); font-weight: 600; text-decoration: none; padding: 7px 14px; border: 1px solid var(--line-strong); border-radius: 8px; background: var(--panel); transition: border-color .15s; }
.dash:hover { border-color: var(--ink); }
:focus-visible { outline: 2px solid var(--genting); outline-offset: 2px; }
.head { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; margin-bottom: 12px; }
h1 { margin: 0; font-size: 20px; font-weight: 650; letter-spacing: -0.01em; display: flex; align-items: baseline; gap: 10px; }
h1 .n { font: 400 13px var(--mono); color: var(--muted); }
#find { margin-left: auto; font: inherit; color: var(--ink); background: var(--panel); border: 1px solid var(--line-strong); border-radius: 8px; padding: 6px 12px; width: min(280px, 100%); }
#find::placeholder { color: var(--muted); }
.panel { background: var(--panel); border: 1px solid var(--line); border-radius: 10px; overflow: hidden; }
h2 { display: flex; align-items: baseline; gap: 10px; margin: 0; padding: 9px 18px; font-size: 14px; font-weight: 600; background: var(--code); border-bottom: 1px solid var(--line); }
.grp + .grp h2 { border-top: 1px solid var(--line); }
.gpath { font: 12px var(--mono); color: var(--muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; }
.gcount { margin-left: auto; font: 12px var(--mono); color: var(--muted); }
.site { display: flex; align-items: baseline; gap: 14px; padding: 9px 18px; text-decoration: none; color: var(--ink); border-top: 1px solid var(--line); }
h2 + .site, .grp:first-child > .site:first-child { border-top: 0; }
.site[hidden], .grp[hidden] { display: none; }
.site:hover { background: var(--hover); }
.site:hover .host { text-decoration: underline; text-underline-offset: 3px; }
.host { font-weight: 600; white-space: nowrap; }
.site .tag { font: 11px var(--mono); color: var(--muted); border: 1px solid var(--line-strong); border-radius: 99px; padding: 0 7px; white-space: nowrap; }
.path { margin-left: auto; font: 12px var(--mono); color: var(--muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; }
.none { margin: 0; padding: 18px; color: var(--muted); }
.empty { padding: 28px; background: var(--panel); border: 1px solid var(--line); border-radius: 10px; }
.empty p { margin: 8px 0 0; color: var(--muted); }
code { font-family: var(--mono); font-size: 0.92em; }
.foot { margin-top: 18px; font-size: 13px; color: var(--muted); }
@media (max-width: 560px) { .path, .gpath { display: none; } #find { margin-left: 0; width: 100%; } }
`

// Filters the list as you type; Enter opens the first match, Escape clears.
const landingJS = `
(function () {
  var box = document.getElementById("find");
  if (!box) return;
  function run() {
    var q = box.value.trim().toLowerCase(), any = false;
    document.querySelectorAll(".grp").forEach(function (g) {
      var shown = 0;
      g.querySelectorAll(".site").forEach(function (a) {
        var hit = !q || a.textContent.toLowerCase().indexOf(q) >= 0;
        a.hidden = !hit;
        if (hit) shown++;
      });
      g.hidden = !shown;
      if (shown) any = true;
    });
    document.getElementById("none").hidden = any;
  }
  box.addEventListener("input", run);
  box.addEventListener("keydown", function (e) {
    if (e.key === "Escape") { box.value = ""; run(); }
    if (e.key === "Enter") { var a = document.querySelector(".site:not([hidden])"); if (a) location.href = a.href; }
  });
})();
`
