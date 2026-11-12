package caddyconf

import (
	"fmt"
	"html"
	"net/url"
	"strings"

	"laman/internal/sites"
)

// NoticeInput describes a site with nothing to serve right now.
type NoticeInput struct {
	Site      sites.Site
	State     string   // Node sites: "stopped", "starting", "running" or "crashed"
	Port      int      // Node sites: the dev server's port
	Output    string   // Node sites: the last output lines after a crash
	Sub       []string // groups: the projects inside
	Dashboard string
	Theme     string
}

// Notice renders the page a Node site shows while its dev server is not
// up, a proxy shows while its port is quiet, and a group or non-website
// folder shows instead of a bare 404. Node and proxy pages check every two
// seconds and turn into the app once it answers.
func Notice(in NoticeInput) string {
	s := in.Site
	e := html.EscapeString
	link := func(param, label string) string {
		// A new tab, so this page stays open and turns into the app.
		return fmt.Sprintf(`<a class="btn" href="%s?%s=%s#home" target="_blank" rel="noopener">%s</a>`, e(in.Dashboard), param, url.QueryEscape(s.Name), label)
	}
	var tag, title, body string
	watch := false
	switch s.Kind {
	case sites.KindNode:
		tag, watch = s.Framework+" app", true
		cmd := `<code>` + e(s.DevCmd) + `</code>`
		switch in.State {
		case "starting":
			title = "Starting the dev server…"
			body = `<p>laman is running ` + cmd + ` in <code>` + e(s.Path) + `</code>. This page opens the app as soon as it answers.</p>`
		case "crashed":
			title = "The dev server stopped"
			body = `<p>` + cmd + ` exited. Its last output:</p><pre>` + e(in.Output) + `</pre><p>` + link("start", "Start it again") + `</p>`
		case "running":
			title = "The dev server isn't answering"
			body = fmt.Sprintf(`<p>%s is running on port %d but didn't answer. It may still be compiling; this page retries by itself.</p>`, cmd, in.Port)
		default:
			title = "The dev server isn't running"
			body = `<p>This is a ` + e(s.Framework) + ` project, so it needs its dev server. laman can run ` + cmd + ` for you and send <b>` + e(s.Host) + `</b> to it.</p>` +
				`<p>` + link("start", "Start it in the dashboard") + `</p>` +
				`<p class="fine">Or in a terminal: <code>laman dev start ` + e(s.Name) + `</code>. This page opens the app once it's up.</p>`
		}
	case sites.KindProxy:
		tag, watch = "proxy", true
		title = "Nothing is answering on " + e(s.Proxy)
		body = `<p><b>` + e(s.Host) + `</b> forwards to <code>` + e(s.Proxy) + `</code>. Start the app that listens there; this page opens it once it answers.</p>` +
			`<p class="fine">Change it with <code>laman proxy ` + e(s.Name) + ` &lt;port&gt;</code>, or remove it with <code>laman proxy remove ` + e(s.Name) + `</code>.</p>`
	case sites.KindGroup:
		tag = "projects folder"
		title = fmt.Sprintf("This folder holds %d projects", len(in.Sub))
		items := make([]string, len(in.Sub))
		for i, n := range in.Sub {
			items[i] = `<li><code>` + e(n) + `</code></li>`
		}
		body = `<p><code>` + e(s.Path) + `</code> isn't a website itself; the folders inside it are:</p><ul>` + strings.Join(items, "") + `</ul>` +
			`<p>laman can give each one its own address, such as <b>` + e(s.Name) + `-` + e(sites.Slug(firstOr(in.Sub, "app"))) + `.test</b>.</p><p>` + link("site", "Set it up in the dashboard") + `</p>`
	default:
		tag = "not a website"
		title = "Nothing to open here"
		body = `<p><code>` + e(s.Path) + `</code> has no <code>index.php</code>, no <code>index.html</code> and no <code>package.json</code> with a dev script, so there's nothing for a browser to show.</p>` +
			`<p class="fine">laman checks again when files change. In the dashboard it is listed under <b>Not websites</b>.</p>`
	}

	theme := ""
	if in.Theme == "light" || in.Theme == "dark" {
		theme = fmt.Sprintf(` data-theme="%s"`, in.Theme)
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<!doctype html>
<html lang="en"%s>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>%s · laman</title>
<style>%s%s</style>
</head>
<body>
<main>
<header>
  %s
  <a class="dash" href="%s">Open dashboard</a>
</header>
<div class="card">
<p class="site"><b>%s</b> <span class="tag">%s</span></p>
<h1>%s</h1>
%s
</div>
</main>
`, theme, e(s.Host), landingCSS, noticeCSS, landingLogo, e(in.Dashboard), e(s.Host), e(tag), title, body)
	if watch {
		b.WriteString(`<script>` + noticeJS + `</script>`)
	}
	b.WriteString("</body>\n</html>\n")
	return b.String()
}

func firstOr(list []string, def string) string {
	if len(list) > 0 {
		return list[0]
	}
	return def
}

const noticeCSS = `
.card { background: var(--panel); border: 1px solid var(--line); border-radius: 10px; padding: 26px 28px; }
.card .site { display: flex; align-items: baseline; gap: 10px; margin: 0 0 6px; padding: 0; border: 0; font-size: 14px; }
.card .site:hover { background: none; }
.tag { font: 12px var(--mono); color: var(--muted); border: 1px solid var(--line-strong); border-radius: 99px; padding: 1px 8px; }
.card h1 { font-size: 22px; margin: 0 0 12px; text-wrap: balance; }
.card p { margin: 0 0 12px; max-width: 65ch; }
.card ul { margin: 0 0 12px; padding-left: 20px; }
.btn { display: inline-block; margin-top: 4px; padding: 8px 16px; border-radius: 8px; background: var(--ink); color: var(--bg); font-weight: 600; text-decoration: none; }
.btn:hover { opacity: .9; }
.fine { font-size: 13px; color: var(--muted); }
pre { margin: 0 0 12px; padding: 12px 14px; background: var(--code); border: 1px solid var(--line); border-radius: 8px; font: 12px/1.5 var(--mono); white-space: pre-wrap; word-break: break-word; max-height: 320px; overflow: auto; }
`

// Reloads once the site answers with something other than this page, or
// this page changes (the dev server crashed or started).
const noticeJS = `
(function () {
  var first = null;
  function check() {
    fetch(location.href, { cache: "no-store" }).then(function (r) {
      if (!r.headers.get("X-Laman-Notice")) { location.reload(); return; }
      return r.text().then(function (t) {
        if (first === null) first = t; else if (t !== first) location.reload();
      });
    }).catch(function () {}).then(function () { setTimeout(check, 2000); });
  }
  check();
})();
`
