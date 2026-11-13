package main

import (
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
		{&sites.PHPFit{Needs: ">=9"}, "  ! needs PHP >=9"},
	}
	for _, c := range cases {
		if got := phpFitHint(c.fit); got != c.want {
			t.Errorf("got %q want %q", got, c.want)
		}
	}
}
