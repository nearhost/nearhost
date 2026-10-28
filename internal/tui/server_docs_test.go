package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/documents"
	"github.com/nearhost/nearhost/internal/engine"
)

// docsModel is passModel without passthroughs, and with two trees served on
// apollo, the newer first, and one on this machine below its home.
func docsModel(t *testing.T) (*Model, *fakeBackend) {
	t.Helper()
	m, f := passModel(t)
	m.homeDir = func() (string, error) { return "/Users/dev", nil }
	now := m.now()
	f.passes = nil
	f.trees = []documents.Tree{
		{ID: "t1", Server: "apollo", Root: "/home/dev/code/app", Requester: "claude-code", Opened: now.Add(-12 * time.Minute),
			Docs: []documents.Doc{{Path: "plans/x.md", Opened: now.Add(-12 * time.Minute)}, {Path: "spec.md", Opened: now.Add(-time.Hour)}}},
		{ID: "t2", Server: "apollo", Root: "/srv/site", Requester: "codex", Opened: now.Add(-3 * time.Hour),
			Docs: []documents.Doc{{Path: "index.html", Opened: now.Add(-3 * time.Hour)}}},
		{ID: "t3", Server: config.LocalServer, Root: "/Users/dev/code/app", Requester: "claude-code", Opened: now.Add(-5 * time.Minute),
			Docs: []documents.Doc{{Path: "plans/y.md", Opened: now.Add(-5 * time.Minute)}}},
	}
	return m, f
}

func TestServerListsDocuments(t *testing.T) {
	m, _ := docsModel(t)
	wantRow(t, m, "documents")
	wantRow(t, m, "/home/dev/code/app · Claude Code · plans/x.md · 12m")
	wantRow(t, m, "/srv/site · Codex · index.html · 3h")
	s := screen(m)
	if strings.Contains(s, "spec.md") || strings.Contains(s, "plans/y.md") {
		t.Errorf("panel shows an older document or another server's tree:\n%s", s)
	}
	if strings.Index(s, "/home/dev/code/app") > strings.Index(s, "/srv/site") {
		t.Errorf("trees are not newest first:\n%s", s)
	}
	if strings.Index(s, "/srv/site") > strings.Index(s, "always allowed") {
		t.Errorf("documents are not listed before the grants:\n%s", s)
	}
}

func TestLaptopListsDocuments(t *testing.T) {
	m, f := docsModel(t)
	m.selectRow(rowKey(rowServer, config.LocalServer))
	wantRow(t, m, "documents")
	wantRow(t, m, "~/code/app · Claude Code · plans/y.md · 5m")
	if s := screen(m); strings.Contains(s, "/srv/site") {
		t.Errorf("the laptop shows apollo's trees:\n%s", s)
	}
	if got := footer(m); !strings.Contains(got, "tab documents") {
		t.Fatalf("footer %q", got)
	}
	press(m, "tab")
	if got := footer(m); got != "↑↓ select · enter open · x remove · tab done" {
		t.Fatalf("footer %q", got)
	}
	run(m, press(m, "enter"))
	if got := f.documentTreeCalls(); !slices.Equal(got, []string{"open t3"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestEnterOpensDocument(t *testing.T) {
	m, f := docsModel(t)
	press(m, "tab")
	press(m, "down") // the second tree
	run(m, press(m, "enter"))
	if got := f.documentTreeCalls(); !slices.Equal(got, []string{"open t2"}) {
		t.Fatalf("calls %v", got)
	}
	if !m.passFocus {
		t.Fatal("enter took the focus away")
	}
	f.treeErr = errBoom
	run(m, press(m, "enter"))
	wantScreen(t, m, "Boom")
}

func TestRemoveDocumentsConfirms(t *testing.T) {
	m, f := docsModel(t)
	m.selectRow(rowKey(rowServer, config.LocalServer))
	press(m, "tab")
	press(m, "x")
	wantText(t, m, "Stop serving ~/code/app?")
	if got := footer(m); got != "y stop serving · n keep" {
		t.Fatalf("footer %q", got)
	}
	settle(m, press(m, "n"))
	if m.form != nil || len(f.documentTreeCalls()) != 0 {
		t.Fatalf("form %v, calls %v", m.form, f.documentTreeCalls())
	}
	press(m, "x")
	settle(m, press(m, "y"))
	if got := f.documentTreeCalls(); !slices.Equal(got, []string{"remove t3"}) {
		t.Fatalf("calls %v", got)
	}
	if m.form != nil {
		t.Fatalf("the question stays open: %T", m.form)
	}
	if s := screen(m); strings.Contains(s, "~/code/app") && strings.Contains(s, "plans/y.md") {
		t.Errorf("the removed tree is still listed:\n%s", s)
	}
}

func TestRemoveDocumentsFailureShown(t *testing.T) {
	m, f := docsModel(t)
	f.treeErr = errBoom
	press(m, "tab")
	press(m, "x")
	settle(m, press(m, "y"))
	if _, ok := m.form.(*treeForm); !ok {
		t.Fatalf("form %T", m.form)
	}
	wantScreen(t, m, "Boom")
}

func TestHomeCountsDocuments(t *testing.T) {
	m, f := docsModel(t)
	m.selectRow(homeKey)
	wantText(t, m, "agents apollo 2 documents · local 1 document")
	f.passes = append(f.passes, passModelPass(m))
	wantText(t, m, "agents apollo 1 passthrough, 2 documents · local 1 document")
}

// passModelPass is an open passthrough on apollo.
func passModelPass(m *Model) engine.Passthrough {
	return engine.Passthrough{PassthroughSpec: engine.PassthroughSpec{ID: "p1", Server: "apollo", Requester: "codex", Remote: 5173},
		Local: 5173, Opened: m.now()}
}
