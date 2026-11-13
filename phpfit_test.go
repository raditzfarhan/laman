package main

import (
	"strings"
	"testing"

	"laman/internal/sites"
)

func TestPHPFitHint(t *testing.T) {
	cases := []struct {
		fit  *sites.PHPFit
		want string
	}{
		{nil, ""},
		{&sites.PHPFit{Needs: ">=8.1 <8.5", Use: "8.4.15-1"}, "  ! needs PHP >=8.1 <8.5 (laman isolate 8.4.15)"},
		{&sites.PHPFit{Needs: "~7.4.0", Install: "7.4"}, "  ! needs PHP ~7.4.0 (laman install php 7.4)"},
		{&sites.PHPFit{Needs: ">=9"}, "  ! needs PHP >=9 (no installed PHP fits; see laman install php)"},
	}
	for _, c := range cases {
		if got := phpFitHint(c.fit); got != c.want {
			t.Errorf("got %q want %q", got, c.want)
		}
	}
}

func TestPrintSitesShowsPHPFit(t *testing.T) {
	t.Setenv("LAMAN_HOME", t.TempDir())
	list := []sites.Site{
		{Name: "acme", Host: "acme.test", Root: `C:\www\acme\public`, Kind: sites.KindPHP,
			PHPFit: &sites.PHPFit{Needs: "^7.4", Install: "7.4"}},
		{Name: "shop", Host: "shop.test", Root: `C:\www\shop`, Kind: sites.KindPHP},
	}
	var b strings.Builder
	printSites(&b, list, func(h string) string { return "http://" + h })
	out := b.String()
	if !strings.Contains(out, `C:\www\acme\public  ! needs PHP ^7.4 (laman install php 7.4)`) {
		t.Errorf("acme's fit is missing:\n%s", out)
	}
	if strings.Count(out, "needs PHP") != 1 {
		t.Errorf("only acme needs another PHP:\n%s", out)
	}
}
