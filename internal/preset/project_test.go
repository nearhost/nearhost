package preset

import (
	"io/fs"
	"reflect"
	"strings"
	"testing"
)

// files is an in-memory Reader that records what was asked of it: asked
// holds every name, read the names it read.
type files struct {
	content map[string]string
	asked   []string
	read    []string
}

func newFiles(content map[string]string) *files { return &files{content: content} }

func (f *files) ReadFile(name string) ([]byte, error) {
	f.asked = append(f.asked, name)
	f.read = append(f.read, name)
	s, ok := f.content[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return []byte(s), nil
}

func (f *files) Exists(name string) (bool, error) {
	f.asked = append(f.asked, name)
	_, ok := f.content[name]
	return ok, nil
}

// file() asks whether a file is there and never reads it, so a file too
// large to read still counts.
func TestDetectFileDoesNotReadTheFile(t *testing.T) {
	f := newFiles(map[string]string{"big.lock": strings.Repeat("x", 2<<20)})
	got, err := DetectExpr(`file("big.lock")`, &Project{Files: f})
	if err != nil || !got {
		t.Fatalf("DetectExpr = %v %v, want true", got, err)
	}
	if len(f.read) != 0 {
		t.Errorf("read %v; file() must not read", f.read)
	}
}

func mustParse(t *testing.T, name, doc string) *Preset {
	t.Helper()
	p, err := Parse(name, []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDetect(t *testing.T) {
	p := mustParse(t, "x", "schema: 1\nname: x\ntitle: X\ndetect: file(\"a.config.js\", \"a.config.ts\") || pkg(\"a\") || compose.contains(\"a-image\") || \"A_PORT\" in env\n")
	cases := []struct {
		name string
		pr   Project
		want bool
	}{
		{"nothing", Project{Files: newFiles(nil)}, false},
		{"no reader", Project{}, false},
		{"second file", Project{Files: newFiles(map[string]string{"a.config.ts": ""})}, true},
		{"dependency", Project{Files: newFiles(map[string]string{"package.json": `{"dependencies":{"a":"1"}}`})}, true},
		{"dev dependency", Project{Files: newFiles(map[string]string{"package.json": `{"devDependencies":{"a":"1"}}`})}, true},
		{"other dependency", Project{Files: newFiles(map[string]string{"package.json": `{"dependencies":{"b":"1"}}`})}, false},
		{"broken package.json", Project{Files: newFiles(map[string]string{"package.json": `{`})}, false},
		{"compose", Project{Compose: "image: a-image:1", Files: newFiles(nil)}, true},
		{"env", Project{Env: map[string]string{"A_PORT": "1"}, Files: newFiles(nil)}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := p.Detected(&tc.pr)
			if err != nil || got != tc.want {
				t.Fatalf("Detected = %v %v, want %v", got, err, tc.want)
			}
		})
	}
}

// An index entry's detect expression arrives over the network and is never
// run through Parse, so the file helper has to refuse bad paths itself.
func TestDetectRefusesPathsOutsideTheProject(t *testing.T) {
	f := newFiles(map[string]string{"/etc/passwd": "x", "../secret": "x", "a/../../b": "x", "conf.*": "x"})
	got, err := DetectExpr(`file("/etc/passwd") || file("../secret") || file("a/../../b") || file("conf.*")`, &Project{Files: f})
	if err != nil || got {
		t.Fatalf("DetectExpr = %v %v, want false", got, err)
	}
	if len(f.asked) != 0 {
		t.Errorf("read %v; nothing outside the project may be read", f.asked)
	}
}

func TestDetectExprReportsABrokenExpression(t *testing.T) {
	if _, err := DetectExpr(`file(`, &Project{}); err == nil {
		t.Error("expected a syntax error")
	}
	if _, err := DetectExpr(`facts.port > 0`, &Project{}); err == nil {
		t.Error("detect must not see facts")
	}
}

const factsPreset = `
schema: 1
name: x
title: X
detect: 'true'
facts:
  port: {scan: [a.config.js, a.config.ts], regex: 'port:\s*(\d+)', as: int}
  name: {scan: [a.config.js, a.config.ts], regex: 'name:\s*"([^"]*)"', as: string}
  https: {scan: [a.config.js, a.config.ts], regex: 'https:\s*true', as: bool}
  hosts: {scan: [a.config.js, a.config.ts], regex: 'hosts:\s*\[([^\]]*)\]', as: quoted_list}
`

func TestEvalFacts(t *testing.T) {
	p := mustParse(t, "x", factsPreset)
	got := p.EvalFacts(&Project{Files: newFiles(map[string]string{
		"a.config.ts": `{ port: 3000, name: "shop", https: true, hosts: ['a.test', "b.test"] }`,
	})})
	want := map[string]any{"port": int64(3000), "name": "shop", "https": true, "hosts": []string{"a.test", "b.test"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("facts = %#v, want %#v", got, want)
	}
}

func TestEvalFactsAreEmptyWhenNothingIsFound(t *testing.T) {
	p := mustParse(t, "x", factsPreset)
	want := map[string]any{"port": int64(0), "name": "", "https": false, "hosts": []string{}}
	for name, pr := range map[string]*Project{
		"no file":   {Files: newFiles(nil)},
		"no match":  {Files: newFiles(map[string]string{"a.config.js": "export default {}"})},
		"no reader": {},
		"huge port": {Files: newFiles(map[string]string{"a.config.js": "port: 99999999999999999999999"})},
	} {
		if got := p.EvalFacts(pr); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: facts = %#v, want %#v", name, got, want)
		}
	}
}

// The first file that exists is the one scanned, even when a later one
// would have matched.
func TestEvalFactsUseTheFirstFileThatExists(t *testing.T) {
	p := mustParse(t, "x", factsPreset)
	got := p.EvalFacts(&Project{Files: newFiles(map[string]string{"a.config.js": "nothing here", "a.config.ts": "port: 3000"})})
	if got["port"] != int64(0) {
		t.Errorf("port = %v, want 0 from a.config.js", got["port"])
	}
}
