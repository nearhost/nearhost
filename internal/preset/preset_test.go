package preset

import (
	"errors"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
)

const validPreset = `
schema: 1
name: stack
title: Test stack
description: A preset for tests.
after: [bundler]
detect: compose.contains("stack")
ports:
  - var: APP_PORT
    label: App
    web: preferred
  - var: ASSET_PORT
    label: Assets
    role: loopback
facts:
  port:
    scan: [bundler.config.js, bundler.config.ts]
    regex: 'port:\s*(\d+)'
    as: int
routes:
  - id: config-port
    when: facts.port > 0
    port: facts.port
    label: Bundler
    web: fallback
suppress: [other-route]
advisories:
  - id: app-url
    when: '"APP_URL" in env'
    message: Set APP_URL to https://{{ alias }}.
    snippet: APP_URL=https://{{ alias }}
`

func TestParseAcceptsAFullPreset(t *testing.T) {
	p, err := Parse("stack", []byte(validPreset))
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "stack" || p.Title != "Test stack" || len(p.After) != 1 || len(p.Suppress) != 1 {
		t.Fatalf("preset = %+v", p)
	}
	if p.Ports[0].Role != config.RoleIgnore || p.Ports[0].Web != WebPreferred {
		t.Errorf("a port with no role should default to ignore: %+v", p.Ports[0])
	}
	if p.Ports[1].Role != config.RoleLoopback {
		t.Errorf("port role lost: %+v", p.Ports[1])
	}
	if p.Routes[0].Role != config.RoleIgnore || p.Routes[0].Web != WebFallback {
		t.Errorf("a route with no role should default to ignore: %+v", p.Routes[0])
	}
	if p.Facts["port"].As != "int" || len(p.Facts["port"].Scan) != 2 {
		t.Errorf("fact = %+v", p.Facts["port"])
	}
}

// minimal is the smallest valid preset, with one line swapped or added.
func minimal(extra string) string {
	return "schema: 1\nname: x\ntitle: X\ndetect: 'true'\n" + extra
}

func TestParseRejectsInvalidPresets(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want string
	}{
		{"empty file", "", "empty"},
		{"unknown key", minimal("colour: red\n"), "colour"},
		{"missing schema", "name: x\ntitle: X\ndetect: 'true'\n", "schema must be 1"},
		{"bad name", "schema: 1\nname: My_Preset\ntitle: X\ndetect: 'true'\n", "lowercase letters, digits and hyphens"},
		{"name differs from file", "schema: 1\nname: y\ntitle: X\ndetect: 'true'\n", `must match the file name "x"`},
		{"missing title", "schema: 1\nname: x\ndetect: 'true'\n", "title is required"},
		{"missing detect", "schema: 1\nname: x\ntitle: X\n", "detect is required"},
		{"compose services", minimal("compose: services\n"), `compose "services" must be ports`},
		{"after itself", minimal("after: [x]\n"), "after"},
		{"after a path", minimal("after: [a/b/c]\n"), "after"},
		{"after a bad alias", minimal("after: [Acme/x]\n"), "after"},
		{"port var", minimal("ports:\n  - {var: '8080', label: App}\n"), "not a variable name"},
		{"port label", minimal("ports:\n  - {var: APP_PORT}\n"), "label is required"},
		{"port listed twice", minimal("ports:\n  - {var: A, label: One}\n  - {var: A, label: Two}\n"), "listed twice"},
		{"role web", minimal("ports:\n  - {var: A, label: App, role: web}\n"), "web: preferred"},
		{"unknown role", minimal("ports:\n  - {var: A, label: App, role: proxy}\n"), `role "proxy"`},
		{"unknown web hint", minimal("ports:\n  - {var: A, label: App, web: always}\n"), `web "always"`},
		{"fact name", minimal("facts:\n  Port: {scan: [a.js], regex: 'p(\\d+)', as: int}\n"), "facts.Port"},
		{"fact without files", minimal("facts:\n  port: {regex: 'p(\\d+)', as: int}\n"), "scan needs at least one file"},
		{"fact absolute path", minimal("facts:\n  port: {scan: [/etc/passwd], regex: 'p(\\d+)', as: int}\n"), "relative to the project"},
		{"fact parent path", minimal("facts:\n  port: {scan: [../secret], regex: 'p(\\d+)', as: int}\n"), "inside the project"},
		{"fact glob", minimal("facts:\n  port: {scan: ['conf.*'], regex: 'p(\\d+)', as: int}\n"), "globs"},
		{"fact bad regex", minimal("facts:\n  port: {scan: [a.js], regex: 'p(', as: int}\n"), "regex"},
		{"fact missing regex", minimal("facts:\n  port: {scan: [a.js], as: bool}\n"), "regex is required"},
		{"fact unknown type", minimal("facts:\n  port: {scan: [a.js], regex: 'p', as: float}\n"), "int, string, bool or quoted_list"},
		{"fact needs a capture", minimal("facts:\n  port: {scan: [a.js], regex: 'p\\d+', as: int}\n"), "exactly one capture group"},
		{"route id", minimal("routes:\n  - {port: '1', label: A}\n"), "id"},
		{"route port", minimal("routes:\n  - {id: a, label: A}\n"), "port is required"},
		{"route label", minimal("routes:\n  - {id: a, port: '1'}\n"), "label is required"},
		{"advisory when", minimal("advisories:\n  - {id: a, message: M}\n"), "when is required"},
		{"advisory message", minimal("advisories:\n  - {id: a, when: 'true'}\n"), "message is required"},
		{"id used twice", minimal("routes:\n  - {id: a, port: '1', label: A}\nadvisories:\n  - {id: a, when: 'true', message: M}\n"), `id "a" is used twice`},
		{"empty suppress", minimal("suppress: ['']\n"), "suppress"},
		{"control in title", "schema: 1\nname: x\ntitle: \"X\\e[2J\"\ndetect: 'true'\n", "title must not contain control characters"},
		{"control in description", minimal("description: \"A\\nB\"\n"), "description must not contain control characters"},
		{"control in port label", minimal("ports:\n  - {var: A, label: \"App\\e[31m\"}\n"), "ports[0]: label must not contain control characters"},
		{"control in route label", minimal("routes:\n  - {id: a, port: '1', label: \"A\\rB\"}\n"), "routes[0]: label must not contain control characters"},
		{"control in message", minimal("advisories:\n  - {id: a, when: 'true', message: \"M\\e[2J\"}\n"), "advisories[0]: message must not contain control characters"},
		{"control in snippet", minimal("advisories:\n  - {id: a, when: 'true', message: M, snippet: \"S\\e]0;x\\a\"}\n"), "advisories[0]: snippet must not contain control characters"},
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
			if !strings.HasPrefix(err.Error(), "x.yaml: ") {
				t.Errorf("error %q should start with the file name", err)
			}
		})
	}
}

func TestParseAcceptsComposePorts(t *testing.T) {
	p, err := Parse("x", []byte(minimal("compose: ports\n")))
	if err != nil {
		t.Fatal(err)
	}
	if p.Compose != ComposePorts {
		t.Errorf("compose = %q", p.Compose)
	}
	p, err = Parse("x", []byte(minimal("")))
	if err != nil || p.Compose != "" {
		t.Fatalf("a preset without compose: %+v %v", p, err)
	}
}

// Advice is shown on several lines and may be indented.
func TestParseAcceptsLineBreaksAndTabsInAdvice(t *testing.T) {
	p, err := Parse("x", []byte(minimal("advisories:\n  - {id: a, when: 'true', message: \"One.\\nTwo.\", snippet: \"a:\\n\\tb: 1\"}\n")))
	if err != nil {
		t.Fatal(err)
	}
	if a := p.Advisories[0]; a.Message != "One.\nTwo." || a.Snippet != "a:\n\tb: 1" {
		t.Errorf("advisory = %+v", a)
	}
}

func TestParseReportsEveryProblem(t *testing.T) {
	_, err := Parse("x", []byte("schema: 1\nname: x\nports:\n  - {var: A}\n"))
	if err == nil {
		t.Fatal("Parse accepted it")
	}
	for _, want := range []string{"title is required", "detect is required", "label is required"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestParseRefusesANewerSchema(t *testing.T) {
	_, err := Parse("x", []byte("schema: 3\nname: x\ntitle: X\ndetect: 'true'\nfuture_key: 1\n"))
	if !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("expected ErrNewerSchema, got %v", err)
	}
	if !strings.Contains(err.Error(), "upgrade nearhost") {
		t.Errorf("error %q should say to upgrade", err)
	}
}

func TestValidName(t *testing.T) {
	for name, want := range map[string]bool{"sail": true, "my-stack": true, "v2": true, "": false, "My": false, "a_b": false, "-a": false, "a-": false, "../x": false} {
		if ValidName(name) != want {
			t.Errorf("ValidName(%q) = %v, want %v", name, !want, want)
		}
	}
}

func TestParseSchemaTwoFields(t *testing.T) {
	two := func(route string) string {
		return "schema: 2\nname: x\ntitle: X\ndetect: 'true'\nroutes:\n  - " + route + "\n"
	}
	route := `{id: a, port: "80", label: App, hosts: '[url_host(env["APP_URL"])]'}`
	if _, err := Parse("x", []byte(two(route))); err != nil {
		t.Fatalf("schema 2 with hosts: %v", err)
	}
	if _, err := Parse("x", []byte(strings.Replace(two(route), "schema: 2", "schema: 1", 1))); err == nil || !strings.Contains(err.Error(), "hosts needs schema: 2") {
		t.Errorf("schema 1 with hosts: %v", err)
	}
	if _, err := Parse("x", []byte(strings.Replace(two(route), "schema: 2", "schema: 3", 1))); !errors.Is(err, ErrNewerSchema) {
		t.Errorf("schema 3: %v", err)
	}
	if _, err := Parse("x", []byte(two(`{id: a, port: "80", label: App, role: loopback, hosts: '["a.test"]'}`))); err == nil || !strings.Contains(err.Error(), "a loopback route cannot have hosts") {
		t.Errorf("loopback with hosts: %v", err)
	}
	if _, err := Parse("x", []byte(two(`{id: a, port: "80", label: App, hosts: '"x"'}`))); err == nil || !strings.Contains(err.Error(), "hosts") || !strings.Contains(err.Error(), "must give list") {
		t.Errorf("hosts that is not a list: %v", err)
	}
}
