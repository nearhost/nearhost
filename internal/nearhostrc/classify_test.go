package nearhostrc

import (
	"errors"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
)

func TestClassify(t *testing.T) {
	read := func(body string) *File {
		t.Helper()
		f, err := Parse([]byte(body), nil)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	const base = "version: 1\nroutes:\n  - port: 3000\n    hosts: [app.test]\n"
	f := read(base)
	p, err := Merge(config.Project{Name: "app", Server: config.LocalServer, Path: "/p", Alias: "app.test", Enabled: true}, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	unapproved := p
	unapproved.Nearhostrc = nil

	cases := []struct {
		name  string
		p     config.Project
		f     *File
		found bool
		err   error
		want  Kind
		prob  string
	}{
		{"unchanged hash", p, f, true, nil, Nothing, ""},
		{"no file, none approved", unapproved, nil, false, nil, Nothing, ""},
		{"port moved", p, read("version: 1\nroutes:\n  - port: 3001\n    hosts: [app.test]\n"), true, nil, Updated, ""},
		{"real domain host", p, read("version: 1\nroutes:\n  - port: 3000\n    hosts: [app.test, app.example.com]\n"), true, nil, Pending, ""},
		{"read error", p, nil, false, errors.New(".nearhostrc: bad"), Problem, ".nearhostrc: bad"},
		{"file gone", p, nil, false, nil, Problem, ".nearhostrc is gone; serving what was last approved"},
	}
	for _, c := range cases {
		got := Classify(c.p, c.f, c.found, c.err)
		if got.Kind != c.want || got.Problem != c.prob {
			t.Errorf("%s: Kind %d Problem %q, want %d %q", c.name, got.Kind, got.Problem, c.want, c.prob)
		}
		if c.want == Pending && (len(got.Items) != 1 || !strings.Contains(got.Items[0].Host, "example.com")) {
			t.Errorf("%s: items %+v", c.name, got.Items)
		}
		if c.want == Updated && got.Next.Routes[0].Port != 3001 {
			t.Errorf("%s: next %+v", c.name, got.Next)
		}
	}
}
