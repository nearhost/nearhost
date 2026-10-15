package proxy

import (
	"testing"
	"time"
)

func TestPlainLog(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"level":"info","ts":1.5,"logger":"http.log.access.web.test","msg":"handled request","request":{"method":"GET","host":"web.test","uri":"/a?b=1"},"duration":0.0123,"size":18,"status":200}`,
			"GET /a?b=1 200 in 12ms"},
		{`{"level":"warn","ts":1.5,"logger":"admin","msg":"admin endpoint disabled"}`, "warn: admin endpoint disabled"},
		{`{"level":"error","logger":"http.log.error","msg":"dial tcp 127.0.0.1:8000: connect: connection refused","request":{"method":"POST","uri":"/login"},"status":502}`,
			"error: dial tcp 127.0.0.1:8000: connect: connection refused (POST /login 502)"},
		{`{"level":"error","logger":"http.log.access.web.test","msg":"handled request","request":{"method":"GET","uri":"/down"},"duration":0.002,"status":502}`,
			"GET /down 502 in 2ms"},
		{`{"level":"error","msg":"aborting with incomplete response","error":"context canceled","request":{"method":"GET","uri":"/x"}}`,
			"error: aborting with incomplete response: context canceled (GET /x)"},
		{"127.0.0.1:5174: bind: address already in use", "127.0.0.1:5174: bind: address already in use"},
		{`{"not":"a log line"}`, `{"not":"a log line"}`},
		{"{broken", "{broken"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := PlainLog(tc.in); got != tc.want {
			t.Errorf("PlainLog(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseAccess(t *testing.T) {
	line := `{"level":"info","ts":1.5,"logger":"http.log.access.web.test","msg":"handled request",` +
		`"request":{"method":"POST","host":"myapp.test","uri":"/login","headers":{"Accept":["text/html"],"Cookie":["REDACTED"]}},` +
		`"duration":0.0634,"size":0,"status":302,"resp_headers":{"Location":["/dashboard"],"Content-Type":["text/html; charset=utf-8"]},"nearhost_id":"ab12"}`
	a, ok := ParseAccess(line)
	if !ok {
		t.Fatal("not read as an access line")
	}
	if a.Method != "POST" || a.Host != "myapp.test" || a.URI != "/login" || a.Status != 302 || a.Took != 63*time.Millisecond ||
		a.Location != "/dashboard" || a.ContentType != "text/html; charset=utf-8" || a.Headers["Cookie"][0] != "REDACTED" || a.ID != "ab12" {
		t.Errorf("read %+v", a)
	}
	for _, other := range []string{
		`{"level":"error","logger":"http.log.error","msg":"dial tcp: refused","request":{"method":"GET","uri":"/"},"status":502}`,
		`{"level":"warn","msg":"admin endpoint disabled"}`, "plain text", "",
	} {
		if _, ok := ParseAccess(other); ok {
			t.Errorf("read %q as an access line", other)
		}
	}
}

func TestLevel(t *testing.T) {
	if got := Level(`{"level":"error","msg":"x"}`); got != "error" {
		t.Errorf("Level = %q", got)
	}
	if got := Level("plain text"); got != "" {
		t.Errorf("Level of a plain line = %q", got)
	}
}
