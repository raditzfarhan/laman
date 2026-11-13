package install

import (
	"fmt"
	"strings"
	"testing"
)

func TestWPConfig(t *testing.T) {
	sample := `define( 'DB_NAME', 'database_name_here' );
define( 'DB_USER', 'username_here' );
define( 'DB_PASSWORD', 'password_here' );
define( 'DB_HOST', 'localhost' );
define( 'AUTH_KEY',         'put your unique phrase here' );
define( 'SECURE_AUTH_KEY',  'put your unique phrase here' );
`
	n := 0
	salt := func() string { n++; return fmt.Sprintf("s%d'x", n) }
	got := WPConfig(sample, "shop", "root", "", "127.0.0.1:3306", salt)
	for _, want := range []string{
		`'DB_NAME', 'shop'`, `'DB_USER', 'root'`, `'DB_PASSWORD', ''`, `'DB_HOST', '127.0.0.1:3306'`,
		`'AUTH_KEY',         's1\'x'`, `'SECURE_AUTH_KEY',  's2\'x'`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "put your unique phrase here") {
		t.Error("a key was left unset")
	}
}
