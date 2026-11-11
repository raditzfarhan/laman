package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnableExtension(t *testing.T) {
	ini := filepath.Join(t.TempDir(), "php.ini")
	os.WriteFile(ini, []byte("[PHP]\r\nextension=curl\r\n;extension=redis\r\n"), 0o644)
	for i := 0; i < 2; i++ { // the second run must not add it twice
		if err := enableExtension(ini, "redis"); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(ini)
	if n := strings.Count(string(b), "\nextension=redis\n"); n != 1 {
		t.Fatalf("extension=redis appears %d times:\n%s", n, b)
	}
}

func TestEnableExtensionKeepsExisting(t *testing.T) {
	ini := filepath.Join(t.TempDir(), "php.ini")
	orig := "extension=php_redis.dll\n"
	os.WriteFile(ini, []byte(orig), 0o644)
	if err := enableExtension(ini, "redis"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(ini); string(b) != orig {
		t.Fatalf("php.ini changed:\n%s", b)
	}
}
