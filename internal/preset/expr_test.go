package preset

import (
	"strings"
	"testing"
)

func testEnv(t *testing.T) (h host, vars map[string]any) {
	t.Helper()
	h = host{
		file:     func(names []string) bool { return len(names) == 2 && names[1] == "b" },
		pkg:      func(name string) bool { return name == "bundler" },
		active:   func(name string) bool { return name == "bundler" },
		freePort: func(from int) int { return from + 1 },
	}
	vars = map[string]any{
		"env":     map[string]string{"ASSET_PORT": " 5174 ", "APP_URL": "http://localhost"},
		"compose": "image: stack-8.3/app",
		"facts":   map[string]any{"port": int64(0), "https": false, "hosts": []string{".test"}},
		"presets": map[string]map[string]any{"bundler": {"port": int64(3000)}},
		"alias":   "myapp.test",
	}
	return h, vars
}

func TestExpressionsSeeTheProject(t *testing.T) {
	h, vars := testEnv(t)
	env, err := newEnv(scopeAdvisory, h)
	if err != nil {
		t.Fatal(err)
	}
	for src, want := range map[string]bool{
		`compose.contains("stack-")`:                                         true,
		`file("a", "b") || pkg("nope")`:                                      true,
		`pkg("bundler")`:                                                     true,
		`facts.port == 0 && !("X" in env && env["X"].trim() != "")`:          true,
		`facts.port > 0`:                                                     false,
		`!facts.https && !host_in(alias, facts.hosts)`:                       false,
		`active("bundler") && presets["bundler"].port == 3000`:               true,
		`active("nope") && presets["nope"].port == 0`:                        false,
		`!("APP_URL" in env) || env["APP_URL"].trim() != "https://" + alias`: true,
	} {
		got, err := evalBool(env, src, vars)
		if err != nil {
			t.Errorf("%s: %v", src, err)
		} else if got != want {
			t.Errorf("%s = %v, want %v", src, got, want)
		}
	}
	if n, err := evalInt(env, `free_port(5173)`, vars); err != nil || n != 5174 {
		t.Errorf("free_port = %d %v", n, err)
	}
}

func TestExpressionFailuresAreErrors(t *testing.T) {
	h, vars := testEnv(t)
	env, _ := newEnv(scopeAdvisory, h)
	if _, err := evalBool(env, `facts.missing > 0`, vars); err == nil {
		t.Error("a missing fact should be an error")
	}
	if _, err := evalBool(env, `facts.port`, vars); err == nil || !strings.Contains(err.Error(), "true or false") {
		t.Errorf("a number is not a condition: %v", err)
	}
	if _, err := evalInt(env, `alias`, vars); err == nil || !strings.Contains(err.Error(), "whole number") {
		t.Errorf("a string is not a port: %v", err)
	}
	deep := `[1,2,3,4,5,6,7,8,9].all(a, [1,2,3,4,5,6,7,8,9].all(b, [1,2,3,4,5,6,7,8,9].all(c, [1,2,3,4,5,6,7,8,9].all(d, [1,2,3,4,5,6,7,8,9].all(e, [1,2,3,4,5,6,7,8,9].all(f, a+b+c+d+e+f > 0))))))`
	if _, err := evalBool(env, deep, vars); err == nil || !strings.Contains(err.Error(), "cost") {
		t.Errorf("expected the cost limit to stop it, got %v", err)
	}
}

func TestRenderFillsInExpressions(t *testing.T) {
	h, vars := testEnv(t)
	env, _ := newEnv(scopeAdvisory, h)
	got, err := render(env, `Set {{ alias }} on port {{free_port(5173)}}; was "{{ "APP_URL" in env ? env["APP_URL"] : "" }}".`, vars)
	if err != nil {
		t.Fatal(err)
	}
	if want := `Set myapp.test on port 5174; was "http://localhost".`; got != want {
		t.Errorf("render = %q, want %q", got, want)
	}
	if got, _ := render(env, "no parts", vars); got != "no parts" {
		t.Errorf("plain text changed: %q", got)
	}
	if _, err := render(env, "broken {{ alias", vars); err == nil || !strings.Contains(err.Error(), "}}") {
		t.Errorf("an unclosed part should be an error, got %v", err)
	}
}

func TestHostIn(t *testing.T) {
	cases := []struct {
		host    string
		allowed []string
		want    bool
	}{
		{"myapp.test", []string{"myapp.test"}, true},
		{"myapp.test", []string{".test"}, true},
		{"test", []string{".test"}, true},
		{"myapp.test", []string{"other.test"}, false},
		{"myapp.test", nil, false},
		{"myapptest", []string{".test"}, false},
	}
	for _, tc := range cases {
		if got := hostIn(tc.host, tc.allowed); got != tc.want {
			t.Errorf("hostIn(%q, %v) = %v, want %v", tc.host, tc.allowed, got, tc.want)
		}
	}
}

func TestParseChecksExpressions(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want string
	}{
		{"detect is not a condition", "schema: 1\nname: x\ntitle: X\ndetect: '\"yes\"'\n", "detect"},
		{"detect cannot see facts", "schema: 1\nname: x\ntitle: X\ndetect: facts.port > 0\n", "detect"},
		{"detect syntax", "schema: 1\nname: x\ntitle: X\ndetect: 'file('\n", "detect"},
		{"route when", minimal("routes:\n  - {id: a, when: '1', port: '1', label: A}\n"), "routes[0]: when"},
		{"route port", minimal("routes:\n  - {id: a, port: '\"80\"', label: A}\n"), "routes[0]: port"},
		{"route cannot see alias", minimal("routes:\n  - {id: a, when: 'alias == \"x\"', port: '1', label: A}\n"), "routes[0]: when"},
		{"advisory when", minimal("advisories:\n  - {id: a, when: 'nope(1)', message: M}\n"), "advisories[0]: when"},
		{"advisory message", minimal("advisories:\n  - {id: a, when: 'true', message: 'Hi {{ nope }}'}\n"), "advisories[0]: message"},
		{"advisory snippet", minimal("advisories:\n  - {id: a, when: 'true', message: M, snippet: 'X={{ alias'}\n"), "advisories[0]: snippet"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse("x", []byte(tc.doc))
			if err == nil {
				t.Fatal("Parse accepted it")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q lacks %q", err, tc.want)
			}
		})
	}
}

func TestURLHelpers(t *testing.T) {
	h, vars := testEnv(t)
	vars["env"] = map[string]string{"P": " 8220 ", "Q": "abc"}
	env, err := newEnv(scopeRoute, h)
	if err != nil {
		t.Fatal(err)
	}
	for src, want := range map[string]string{
		`url_host(" https://App.Local.X.io:8220/path ")`: "app.local.x.io",
		`url_host("http://app.local.x.io.")`:             "app.local.x.io",
		`url_host("ws.local.x.io")`:                      "ws.local.x.io",
		`url_host("127.0.0.1:8000")`:                     "127.0.0.1",
		`url_host("http://[::1]:80")`:                    "::1",
		`url_host("https://")`:                           "",
		`url_host("")`:                                   "",
	} {
		got, err := eval(env, src, vars)
		if err != nil || got != want {
			t.Errorf("%s = %v %v, want %q", src, got, err, want)
		}
	}
	for src, want := range map[string]int{
		`url_port("https://a.b.io:8443/x")`: 8443,
		`url_port("https://a.b.io")`:        0,
		`url_port("a.b.io:70000")`:          0,
		`env_port("P", 80)`:                 8220,
		`env_port("Q", 80)`:                 80,
		`env_port("R", 80)`:                 80,
	} {
		h.env = vars["env"].(map[string]string)
		env, _ := newEnv(scopeRoute, h)
		if got, err := evalInt(env, src, vars); err != nil || got != want {
			t.Errorf("%s = %d %v, want %d", src, got, err, want)
		}
	}
}
