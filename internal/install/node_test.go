package install

import "testing"

func TestPickNode(t *testing.T) {
	win := []string{"win-x64-zip", "win-x64-msi"}
	index := []nodeRelease{
		{"v27.0.0-alpha.0.0.0", false, win},
		{"v26.13.0", "Lithium", win},
		{"v26.12.1", false, win},
		{"v25.9.0", false, win},
		{"v24.11.0", "Krypton", win},
		{"v22.9.0", "Jod", []string{"linux-x64"}}, // no Windows zip
		{"v22.8.0", "Jod", win},
	}
	cases := map[string]string{
		"":        "v26.13.0", // newest LTS, not the alpha
		"lts":     "v26.13.0",
		"latest":  "v26.13.0", // pre-releases never count
		"25":      "v25.9.0",
		"v24":     "v24.11.0",
		"24.11.0": "v24.11.0",
		"22":      "v22.8.0", // skips the release without a Windows zip
	}
	for want, exp := range cases {
		r, err := pickNode(index, want)
		if err != nil || r.Version != exp {
			t.Errorf("pickNode(%q) = %v, %v; want %s", want, r.Version, err, exp)
		}
	}
	if _, err := pickNode(index, "2"); err == nil {
		t.Error(`pickNode("2") should not match 26.x`)
	}
}
