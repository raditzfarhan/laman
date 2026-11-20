package daemon

import (
	"reflect"
	"testing"
)

func TestReverbEnv(t *testing.T) {
	want := []envKV{
		{"BROADCAST_CONNECTION", "reverb"},
		{"REVERB_HOST", "127.0.0.1"},
		{"REVERB_PORT", "8101"},
		{"REVERB_SCHEME", "http"},
		{"REVERB_SERVER_HOST", "127.0.0.1"},
		{"REVERB_SERVER_PORT", "8101"},
		{"VITE_REVERB_HOST", "acme.test"},
		{"VITE_REVERB_PORT", "443"},
		{"VITE_REVERB_SCHEME", "https"},
	}
	if got := reverbEnv("acme.test", "https", 443, 8101); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestEnvMismatch(t *testing.T) {
	vals := map[string]string{"A": `"x"`, "B": "'y'", "C": "z"}
	got := envMismatch(vals, []envKV{{"A", "x"}, {"B", "y"}, {"C", "q"}, {"D", "1"}})
	if !reflect.DeepEqual(got, []string{"C", "D"}) {
		t.Fatalf("got %v", got)
	}
}

func applyOne(t *testing.T, text string, c envChange) string {
	t.Helper()
	got, err := applyEnvChanges(text, []envChange{c})
	if err != nil {
		t.Fatalf("apply %+v on %q: %v", c, text, err)
	}
	return got
}

func checkApply(t *testing.T, text string, c envChange, want string) {
	t.Helper()
	if got := applyOne(t, text, c); got != want {
		t.Errorf("%q + %+v = %q, want %q", text, c, got, want)
	}
}

func TestApplySetKeepsComment(t *testing.T) {
	checkApply(t, "A=1 # one\n", envChange{"set", "A", "2"}, "A=2 # one\n")
}

func TestApplySetSpacing(t *testing.T) {
	checkApply(t, "export A = 1\n", envChange{"set", "A", "2"}, "export A = 2\n")
}

func TestApplySetKeepsQuoteStyle(t *testing.T) {
	checkApply(t, "N=\"Acme #1\"\n", envChange{"set", "N", "Shop #2"}, "N=\"Shop #2\"\n")
	checkApply(t, "S='x'\n", envChange{"set", "S", "y"}, "S='y'\n")
	checkApply(t, "S='x' # c\n", envChange{"set", "S", "it's"}, "S=\"it's\" # c\n")
	checkApply(t, "Q=\"x\"\n", envChange{"set", "Q", "y"}, "Q=\"y\"\n")
	checkApply(t, "Q=\"x\"\n", envChange{"set", "Q", ""}, "Q=\"\"\n")
}

func TestApplySetQuotesWhenNeeded(t *testing.T) {
	checkApply(t, "A=1\n", envChange{"set", "A", "two words"}, "A=\"two words\"\n")
	checkApply(t, "A=1\n", envChange{"set", "A", `say "hi"`}, "A=\"say \\\"hi\\\"\"\n")
	checkApply(t, "A=1\n", envChange{"set", "A", "a=b"}, "A=\"a=b\"\n")
	checkApply(t, "A=1\n", envChange{"set", "A", "$HOME"}, "A=\"$HOME\"\n")
	checkApply(t, "A=1\n", envChange{"set", "A", "plain-1/x"}, "A=plain-1/x\n")
}

func TestApplySetLastDuplicate(t *testing.T) {
	checkApply(t, "P=a\nP=b\n", envChange{"set", "P", "c"}, "P=a\nP=c\n")
}

func TestApplyDeleteAllDuplicates(t *testing.T) {
	checkApply(t, "P=a\nX=1\nP=b\n", envChange{"delete", "P", ""}, "X=1\n")
	checkApply(t, "# c\nA=1\n", envChange{"delete", "Z", ""}, "# c\nA=1\n")
}

func TestApplyAppend(t *testing.T) {
	checkApply(t, "A=1", envChange{"set", "B", "2"}, "A=1\nB=2\n")
	checkApply(t, "", envChange{"set", "B", "2"}, "B=2\n")
}

func TestApplyCRLFNoFinalNewline(t *testing.T) {
	checkApply(t, "A=1\r\nB=2", envChange{"set", "A", "3"}, "A=3\r\nB=2")
	checkApply(t, "A=1\r\nB=2", envChange{"set", "C", "4"}, "A=1\r\nB=2\r\nC=4\r\n")
}

func TestApplyLeavesMultilineAlone(t *testing.T) {
	text := "K=\"a\nA=9\nb\"\nA=1\n"
	checkApply(t, text, envChange{"set", "A", "2"}, "K=\"a\nA=9\nb\"\nA=2\n")
}

func TestApplyRefuses(t *testing.T) {
	multi := "K=\"a\nb\"\n"
	for _, tc := range []struct {
		text string
		c    envChange
	}{
		{"", envChange{"set", "1A", "x"}},
		{"", envChange{"set", "A", "a\nb"}},
		{multi, envChange{"set", "K", "x"}},
		{multi, envChange{"delete", "K", ""}},
		{"A=1\rB=2\n", envChange{"set", "A", "x"}},
		{"", envChange{"nope", "A", "x"}},
	} {
		if got, err := applyEnvChanges(tc.text, []envChange{tc.c}); err == nil {
			t.Errorf("%q + %+v = %q, want error", tc.text, tc.c, got)
		}
	}
}
