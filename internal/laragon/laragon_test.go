package laragon

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func autoConf(host, root string) string {
	return `<VirtualHost *:80> 
    DocumentRoot "` + root + `"
    ServerName ` + host + `
    ServerAlias *.` + host + `
    <Directory "` + root + `">
        AllowOverride All
        Require all granted
    </Directory>
</VirtualHost>

# If you want to use SSL, enable it by going to Menu > Apache > SSL > Enabled
`
}

func fixture(t *testing.T) (root, www string) {
	t.Helper()
	root = t.TempDir()
	www = filepath.Join(root, "www")
	write(t, filepath.Join(root, "laragon.exe"), "")
	write(t, filepath.Join(root, "usr", "laragon.ini"), "[preferences]\r\nAutoVirtualHosts=-1\r\nDataDir="+filepath.Join(root, "data")+"\r\n\r\n"+
		"[mysql]\r\nVersion=mysql-8.4.0-winx64\r\nUse=-1\r\n\r\n[apache]\r\nVersion=httpd-2.4.54-win64-VS16\r\nUse=-1\r\nDocumentRoot="+www+"\r\n\r\n[nginx]\r\nVersion=nginx-1.22.0\r\n")
	sites := filepath.Join(root, "etc", "apache2", "sites-enabled")
	slash := filepath.ToSlash(www)
	write(t, filepath.Join(sites, "00-default.conf"), "<VirtualHost *:80>\n    DocumentRoot \""+slash+"\"\n    ServerName localhost\n</VirtualHost>\n")
	write(t, filepath.Join(sites, "auto.acme.test.conf"), autoConf("acme.test", slash+"/acme"))
	write(t, filepath.Join(sites, "auto.shop_app.test.conf"), autoConf("shop_app.test", slash+"/shop_app/public"))
	write(t, filepath.Join(sites, "custom.conf"), `<VirtualHost *:80>
    DocumentRoot "D:/work/api/public"
    ServerName api.local
    <Directory "D:/work/api/public">
        AllowOverride All
        Require all granted
    </Directory>
    RewriteRule ^/old /new [R=301]
</VirtualHost>
`)
	return root, www
}

func TestFind(t *testing.T) {
	root, www := fixture(t)
	l, err := Find(root)
	if err != nil {
		t.Fatal(err)
	}
	if l.WWW != www {
		t.Errorf("WWW = %q, want %q", l.WWW, www)
	}
	if want := filepath.Join(root, "data", "mysql-8.4"); l.MySQLData != want {
		t.Errorf("MySQLData = %q, want %q", l.MySQLData, want)
	}
	if want := filepath.Join(root, "bin", "mysql", "mysql-8.4.0-winx64", "bin"); l.MySQLBin != want {
		t.Errorf("MySQLBin = %q, want %q", l.MySQLBin, want)
	}
	if l.MySQLVersion != "mysql-8.4.0-winx64" {
		t.Errorf("MySQLVersion = %q", l.MySQLVersion)
	}
	got := map[string]VHost{}
	for _, v := range l.VHosts {
		got[v.Host] = v
	}
	if len(l.VHosts) != 3 {
		t.Fatalf("VHosts = %+v, want 3 (00-default ignored)", l.VHosts)
	}
	if v := got["acme.test"]; !v.Auto || v.Custom || v.Extra || v.Root != filepath.Join(www, "acme") {
		t.Errorf("acme = %+v", v)
	}
	if v := got["shop_app.test"]; !v.Auto || v.Root != filepath.Join(www, "shop_app") {
		t.Errorf("shop_app = %+v", v)
	}
	if v := got["api.local"]; !v.Custom || v.Auto || !v.Extra || v.Root != `D:\work\api` {
		t.Errorf("api = %+v", v)
	}
}

func TestFindNginx(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "laragon.exe"), "")
	write(t, filepath.Join(root, "usr", "laragon.ini"), "[apache]\nUse=0\n[nginx]\nUse=-1\n")
	write(t, filepath.Join(root, "etc", "nginx", "sites-enabled", "auto.acme.test.conf"), `server {
    listen 80;
    server_name acme.test *.acme.test;
    root "C:/laragon/www/acme/public";
}
`)
	write(t, filepath.Join(root, "etc", "apache2", "sites-enabled", "auto.other.test.conf"), autoConf("other.test", "C:/x/other"))
	l, err := Find(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.VHosts) != 1 || l.VHosts[0].Host != "acme.test" || l.VHosts[0].Root != `C:\laragon\www\acme` || l.VHosts[0].Extra {
		t.Errorf("VHosts = %+v", l.VHosts)
	}
}

func TestFindDefaults(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "laragon.exe"), "")
	l, err := Find(root)
	if err != nil {
		t.Fatal(err)
	}
	if l.WWW != filepath.Join(root, "www") || l.DataDir != filepath.Join(root, "data") || l.MySQLData != "" || l.MySQLBin != "" {
		t.Errorf("got %+v", l)
	}
}

func TestFindNotLaragon(t *testing.T) {
	_, err := Find(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "is not a Laragon folder") {
		t.Errorf("err = %v", err)
	}
}

func TestDatabasesDecodesNames(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "mysql-8.4")
	for _, d := range []string{"acme", "acme@002dshop", "mysql", "sys", "#innodb_temp"} {
		if err := os.MkdirAll(filepath.Join(data, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(data, "acme", "t.ibd"), "0123456789")
	l := &Laragon{MySQLData: data}
	got, err := l.Databases()
	if err != nil {
		t.Fatal(err)
	}
	want := []Database{{"acme", 10}, {"acme-shop", 0}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestLooksLikeCopy(t *testing.T) {
	for n, want := range map[string]bool{
		"acme_test": true, "acme_test_12": true, "acme_testing": true, "acme_backup": true, "acme_bak": true,
		"acme_old": true, "acme_copy": true, "acme_20260427": true,
		"acme": false, "testshop": false, "contest": false, "acme_2026": false,
	} {
		if LooksLikeCopy(n) != want {
			t.Errorf("LooksLikeCopy(%q) = %v, want %v", n, !want, want)
		}
	}
}

func TestServersIn(t *testing.T) {
	root := `C:\laragon`
	got := serversIn([]string{
		root + `\bin\apache\httpd-2.4\bin\httpd.exe`,
		root + `\bin\git\bin\git.exe`,
		root + `\bin\mysql\mysql-8.4\bin\MYSQLD.EXE`,
		root + `\bin\cmder\vendor\conemu-maximus5\ConEmu64.exe`,
		root + `\bin\apache\httpd-2.4\bin\httpd.exe`,
	})
	if want := []string{"httpd.exe", "mysqld.exe"}; !reflect.DeepEqual(got, want) {
		t.Errorf("serversIn = %v, want %v", got, want)
	}
}
