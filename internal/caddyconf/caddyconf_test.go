package caddyconf

import (
	"encoding/json"
	"strings"
	"testing"

	_ "github.com/caddyserver/caddy/v2/modules/standard"

	"laman/internal/sites"
)

func input() Input {
	return Input{
		HTMLDir: `C:\p\html`, LogFile: `C:\p\logs\caddy.log`, StorageDir: `C:\p\data\caddy`,
		HTTPPort: 80, PHPPorts: []int{9100, 9101},
		Sites: []sites.Site{
			{Name: "blog", Host: "blog.test", Path: `C:\Sites\My Blog`, Root: `C:\Sites\My Blog\public`, Front: true},
			{Name: "bare", Host: "bare.test", Path: `C:\Sites\bare`, Root: `C:\Sites\bare`},
		},
		HTTPSPort:   443,
		Certs:       map[string]Cert{"blog.test": {Crt: `C:\c\blog.test.crt`, Key: `C:\c\blog.test.key`}},
		DefaultCert: Cert{Crt: `C:\c\localhost.crt`, Key: `C:\c\localhost.key`},
	}
}

func TestRenderedCaddyfileAdapts(t *testing.T) {
	out := Render(input())
	for _, want := range []string{
		"http://blog.test:80 {",
		"https://blog.test:443 {",
		`root * "C:/Sites/My Blog/public"`,
		`tls "C:/c/blog.test.crt" "C:/c/blog.test.key"`,
		"php_fastcgi 127.0.0.1:9100 127.0.0.1:9101 {",
		"http://bare.test:80 {", // no cert: HTTP only
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	js, err := Adapt(out)
	if err != nil {
		t.Fatalf("adapt: %v\n%s", err, out)
	}
	var cfg map[string]any
	if err := json.Unmarshal(js, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"blog.test"`, `"fastcgi"`, `"least_conn"`, `127.0.0.1:80"`, `127.0.0.1:443"`} {
		if !strings.Contains(string(js), want) {
			t.Errorf("adapted JSON lacks %s", want)
		}
	}
}

func TestSiteEnvGoesToThatSiteOnly(t *testing.T) {
	in := input()
	in.SiteEnv = map[string]map[string]string{"blog.test": {"B_KEY": "two", "A_KEY": "one"}}
	out := Render(in)
	if strings.Count(out, `env A_KEY "one"`) != 2 || strings.Count(out, `env B_KEY "two"`) != 2 { // http + https blocks
		t.Fatalf("env not in both blog.test blocks:\n%s", out)
	}
	if strings.Index(out, "env A_KEY") > strings.Index(out, "env B_KEY") {
		t.Error("env lines not sorted")
	}
	bare := out[strings.Index(out, "http://bare.test"):]
	if strings.Contains(bare, "env ") {
		t.Error("bare.test got blog.test's env")
	}
	js, err := Adapt(out)
	if err != nil {
		t.Fatalf("adapt: %v", err)
	}
	if !strings.Contains(string(js), `"A_KEY":"one"`) {
		t.Errorf("adapted JSON lacks the env: %s", js)
	}
}

func TestShareServesOnlyThatSiteWithoutAppKeys(t *testing.T) {
	in := input()
	in.Sites = append(in.Sites, sites.Site{Name: "db", Host: "db.test", Root: `C:\apps\dbzuper\public`, Front: true, App: "dbzuper"})
	in.SiteEnv = map[string]map[string]string{"blog.test": {"SECRET": "s"}, "db.test": {"KEY": "k"}}
	in.Shares = []Share{{Host: "blog.test", Port: 9901}, {Host: "db.test", Port: 9902}, {Host: "gone.test", Port: 9903}}
	out := Render(in)
	i := strings.Index(out, "http://:9901 {")
	if i < 0 {
		t.Fatalf("no share block:\n%s", out)
	}
	block := out[i:]
	block = block[:strings.Index(block, "\n}\n")]
	for _, want := range []string{"bind 127.0.0.1", `root * "C:/Sites/My Blog/public"`, `env HTTPS "on"`} {
		if !strings.Contains(block, want) {
			t.Errorf("share block lacks %q:\n%s", want, block)
		}
	}
	if strings.Contains(block, "SECRET") {
		t.Error("share block got the site's server env")
	}
	if strings.Contains(out, ":9902") || strings.Contains(out, ":9903") {
		t.Error("an app, or a site that no longer exists, was shared")
	}
	if _, err := Adapt(out); err != nil {
		t.Fatalf("adapt: %v", err)
	}
}

func TestRenderHTTPOnly(t *testing.T) {
	in := input()
	in.HTTPSPort = 0
	out := Render(in)
	if strings.Contains(out, "https://") || strings.Contains(out, "tls ") {
		t.Errorf("HTTPSPort 0 must render no TLS:\n%s", out)
	}
	if _, err := Adapt(out); err != nil {
		t.Fatal(err)
	}
}

func TestURL(t *testing.T) {
	cases := map[string]string{
		URL("a.test", false, 80):   "http://a.test/",
		URL("a.test", true, 443):   "https://a.test/",
		URL("a.test", true, 8443):  "https://a.test:8443/",
		URL("a.test", false, 8080): "http://a.test:8080/",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %s want %s", got, want)
		}
	}
}

func TestLandingEscapes(t *testing.T) {
	out := Landing(LandingInput{Sites: []sites.Site{{Host: "a.test", Path: `C:\<x>`}}, Port: 8080, Theme: `"><x>`})
	if !strings.Contains(out, "http://a.test:8080/") || strings.Contains(out, "<x>") || !strings.Contains(out, `<html lang="en">`) {
		t.Fatalf("bad landing: %s", out)
	}
}

func TestLandingGroupsByFolder(t *testing.T) {
	out := Landing(LandingInput{
		Folders: []sites.Folder{{Dir: `C:\www`}, {Dir: `C:\www\acme`, Prefix: "ac"}},
		Sites: []sites.Site{
			{Host: "a.test", Path: `C:\www\a`, Folder: `C:\www`},
			{Host: "ac-api.test", Path: `C:\www\acme\api`, Folder: `C:\www\acme`},
			{Host: "db.test", App: "dbzuper"},
		},
		Port: 80, Theme: "dark",
	})
	for _, want := range []string{`data-theme="dark"`, `<span class="gname">www</span>`, `<span class="gname">acme</span>`, `<span class="gname">Tools</span>`, "http://ac-api.test/"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(out, ">Linked<") {
		t.Error("empty Linked group shown")
	}
}

func TestStaticSitesDisableFrontController(t *testing.T) {
	out := Render(input())
	blog := out[strings.Index(out, "http://blog.test:80 {"):]
	blog = blog[:strings.Index(blog, "\n}")]
	bare := out[strings.Index(out, "http://bare.test:80 {"):]
	bare = bare[:strings.Index(bare, "\n}")]
	if strings.Contains(blog, "index off") || !strings.Contains(bare, "index off") {
		t.Errorf("index off belongs only on the static site:\nblog:\n%s\nbare:\n%s", blog, bare)
	}
}

func TestSitePortsOverrideDefaultPool(t *testing.T) {
	in := input()
	in.SitePorts = map[string][]int{"bare.test": {9200}}
	out := Render(in)
	if !strings.Contains(out, "php_fastcgi 127.0.0.1:9200 {") {
		t.Errorf("bare.test should use its own pool:\n%s", out)
	}
	if !strings.Contains(out, "php_fastcgi 127.0.0.1:9100 127.0.0.1:9101 {") {
		t.Error("blog.test should keep the default pool")
	}
	if _, err := Adapt(out); err != nil {
		t.Fatal(err)
	}
}

func TestOwnDomainSharesRouteByHostName(t *testing.T) {
	in := input()
	in.DomainPort = 9950
	in.Shares = []Share{{Host: "blog.test", Hostname: "blog.example.com"}}
	out := Render(in)
	for _, want := range []string{"http://blog.example.com:9950 {", "http://:9950 {", `respond "Not shared right now." 404`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if _, err := Adapt(out); err != nil {
		t.Fatalf("adapt: %v\n%s", err, out)
	}
	in.DomainPort = 0 // tunnel not running: nothing on the domain
	if out := Render(in); strings.Contains(out, "example.com") {
		t.Error("own-domain share rendered without its tunnel")
	}
}

func TestNodeProxyAndNoticeSites(t *testing.T) {
	in := input()
	in.Sites = append(in.Sites,
		sites.Site{Name: "web", Host: "web.test", Kind: sites.KindNode, Path: `C:\Sites\web`, Root: `C:\Sites\web`},
		sites.Site{Name: "shop", Host: "shop.test", Kind: sites.KindNode, Path: `C:\Sites\shop`, Root: `C:\Sites\shop`},
		sites.Site{Name: "dock", Host: "dock.test", Kind: sites.KindProxy, Proxy: "127.0.0.1:8080"},
		sites.Site{Name: "infra", Host: "infra.test", Kind: sites.KindNone, Path: `C:\Sites\infra`, Root: `C:\Sites\infra`},
	)
	in.DevPorts = map[string]int{"shop.test": 3101}
	out := Render(in)
	block := func(host string) string {
		b := out[strings.Index(out, "http://"+host+":80 {"):]
		return b[:strings.Index(b, "\n}")]
	}
	if b := block("web.test"); !strings.Contains(b, "rewrite * /web.html") || !strings.Contains(b, "status 503") || strings.Contains(b, "php_fastcgi") {
		t.Errorf("stopped dev server should show its notice:\n%s", b)
	}
	if b := block("shop.test"); !strings.Contains(b, "reverse_proxy localhost:3101") || !strings.Contains(b, "header_up Host {upstream_hostport}") {
		t.Errorf("running dev server should be proxied:\n%s", b)
	}
	if b := block("dock.test"); !strings.Contains(b, "reverse_proxy 127.0.0.1:8080") {
		t.Errorf("proxy:\n%s", b)
	}
	if b := block("infra.test"); !strings.Contains(b, "status 404") {
		t.Errorf("not a website:\n%s", b)
	}
	if _, err := Adapt(out); err != nil {
		t.Fatalf("adapt: %v\n%s", err, out)
	}
}
