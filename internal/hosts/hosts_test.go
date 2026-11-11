package hosts

import "testing"

func TestMergeAppendsBlockKeepingCRLF(t *testing.T) {
	in := "# comment\r\n127.0.0.1 localhost\r\n"
	got := Merge(in, []string{"blog.test", "shop.test"})
	want := "# comment\r\n127.0.0.1 localhost\r\n\r\n# BEGIN laman\r\n127.0.0.1 blog.test\r\n127.0.0.1 shop.test\r\n# END laman\r\n"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestMergeReplacesExistingBlockAndIsIdempotent(t *testing.T) {
	in := "a\n\n# BEGIN laman\n127.0.0.1 old.test\n# END laman\nb\n"
	got := Merge(in, []string{"new.test"})
	want := "a\n\nb\n\n# BEGIN laman\n127.0.0.1 new.test\n# END laman\n"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if again := Merge(got, []string{"new.test"}); again != got {
		t.Fatalf("not idempotent: %q", again)
	}
}

func TestMergeRemovesBlockWhenNoSites(t *testing.T) {
	in := "a\r\n\r\n# BEGIN laman\r\n127.0.0.1 x.test\r\n# END laman\r\n"
	if got := Merge(in, nil); got != "a\r\n" {
		t.Fatalf("got %q", got)
	}
}

func TestMergeRemovesLegacyPerchBlock(t *testing.T) {
	in := "a\r\n\r\n# BEGIN perch\r\n127.0.0.1 blog.test\r\n# END perch\r\n"
	want := "a\r\n\r\n# BEGIN laman\r\n127.0.0.1 blog.test\r\n# END laman\r\n"
	if got := Merge(in, []string{"blog.test"}); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}
