package cli

import "testing"

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
		if got := plainLog(tc.in); got != tc.want {
			t.Errorf("plainLog(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
