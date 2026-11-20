package daemon

import (
	"reflect"
	"testing"
)

func TestParseBasics(t *testing.T) {
	got := parseEnv("A=1\nexport B = two\n# c\nC=\"x y\" \nD=v # note\n")
	want := []envLine{
		{Key: "A", Value: "1", Line: 1},
		{Key: "B", Value: "two", Line: 2},
		{Key: "C", Value: "x y", Quote: '"', Line: 4},
		{Key: "D", Value: "v", Line: 5, Comment: " # note"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestParseQuotedHash(t *testing.T) {
	got := parseEnv("APP_NAME=\"Acme #1\"\n")
	if len(got) != 1 || got[0].Value != "Acme #1" || got[0].Comment != "" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseMultiline(t *testing.T) {
	got := parseEnv("K=\"a\nb\"\nZ=1\n")
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if !got[0].Multiline || got[0].Value != "a\nb" || got[0].Line != 1 {
		t.Fatalf("K: %+v", got[0])
	}
	if got[1].Key != "Z" || got[1].Line != 3 {
		t.Fatalf("Z: %+v", got[1])
	}
}

func TestParseDuplicatesLastWins(t *testing.T) {
	rows, _ := buildEnvView("P=a\nP=b\n", "")
	if len(rows) != 2 || rows[0].Ignored != 2 || rows[1].Ignored != 0 {
		t.Fatalf("rows %+v", rows)
	}
}

func TestSecretRule(t *testing.T) {
	for _, kv := range [][2]string{{"APP_KEY", "x"}, {"DB_PASSWORD", "x"}, {"STRIPE_SECRET", "x"}, {"AUTH_TOKEN", "x"}, {"DATABASE_URL", "mysql://u:p@h/db"}} {
		if !isSecretEnv(kv[0], kv[1]) {
			t.Errorf("%s should be secret", kv[0])
		}
	}
	for _, kv := range [][2]string{{"KEYBOARD", "x"}, {"MONKEY", "x"}, {"DB_PASSWORD", ""}, {"REDIS_URL", "redis://h:6379"}, {"APP_NAME", "x"}} {
		if isSecretEnv(kv[0], kv[1]) {
			t.Errorf("%s=%q should not be secret", kv[0], kv[1])
		}
	}
}

func TestViewMasksAndMissing(t *testing.T) {
	rows, missing := buildEnvView("APP_KEY=base64:z\nA=1\n", "A=\nMAIL_FROM_ADDRESS=hello@example.com\nAPP_KEY=\n")
	if len(rows) != 2 || !rows[0].Masked || rows[0].Value != "" || rows[1].Value != "1" {
		t.Fatalf("rows %+v", rows)
	}
	want := []envMissing{{Key: "MAIL_FROM_ADDRESS", Value: "hello@example.com"}}
	if !reflect.DeepEqual(missing, want) {
		t.Fatalf("missing %+v", missing)
	}
}

func TestDecodeEncodeRoundTrip(t *testing.T) {
	for _, v := range []string{`a"b`, `c\d`, "x y", "", "#h", "a" + bsl, "a" + bsl + `"b`, bsl + bsl, bsl + "n"} {
		enc := encodeEnvValue(v, '"')
		got, _, _, ok := decodeEnvValue(enc)
		if !ok || got != v {
			t.Errorf("%q -> %s -> %q ok=%v", v, enc, got, ok)
		}
	}
}

func TestEncodeEqualsStaysBare(t *testing.T) {
	if got := encodeEnvValue("base64:abc=", 0); got != "base64:abc=" {
		t.Errorf("got %s", got)
	}
}

const bsl = string(bs)

func TestDecodeEscapes(t *testing.T) {
	for raw, want := range map[string]string{
		`"C:` + bsl + bsl + `x"`: "C:" + bsl + "x",
		`"a` + bsl + `"b"`:       `a"b`,
		`"a` + bsl + `nb"`:       "a\nb",
		`"a` + bsl + bsl + `"`:   "a" + bsl,
		`"` + bsl + `$HOME"`:     "$HOME",
		`"a` + bsl + `qb"`:       "a" + bsl + "qb",
		`'a` + bsl + `nb'`:       "a" + bsl + "nb",
	} {
		got, _, _, ok := decodeEnvValue(raw)
		if !ok || got != want {
			t.Errorf("%s -> %q ok=%v, want %q", raw, got, ok, want)
		}
	}
}
