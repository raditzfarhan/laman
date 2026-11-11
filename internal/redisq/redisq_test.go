package redisq

import "testing"

func TestSplitKey(t *testing.T) {
	for _, c := range []struct{ key, prefix, name, part string }{
		{"blog_database_queues:default", "blog_database_", "default", ""},
		{"blog_database_queues:emails:delayed", "blog_database_", "emails", "delayed"},
		{"queues:default:reserved", "", "default", "reserved"},
		{"x_queues:a:b", "x_", "a:b", ""},
	} {
		p, n, part, ok := SplitKey(c.key)
		if !ok || p != c.prefix || n != c.name || part != c.part {
			t.Errorf("SplitKey(%q) = %q %q %q %v", c.key, p, n, part, ok)
		}
	}
	for _, bad := range []string{"cache:foo", "queues:", "queues::delayed"} {
		if _, _, _, ok := SplitKey(bad); ok {
			t.Errorf("SplitKey(%q) accepted", bad)
		}
	}
}

func TestParseJob(t *testing.T) {
	j := parseJob(`{"uuid":"u1","displayName":"App\\Jobs\\SendInvoice","job":"Illuminate\\Queue\\CallQueuedHandler@call","attempts":2,"pushedAt":"1794281583.5"}`)
	if j.ID != "u1" || j.Name != `App\Jobs\SendInvoice` || j.Attempts != 2 {
		t.Errorf("parseJob = %+v", j)
	}
	if parseJob("not json").Name == "" {
		t.Error("garbage should still give a row")
	}
}
