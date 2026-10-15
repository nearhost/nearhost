package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

var errListing = errors.New("cannot list /home/dev: permission denied")

// pathLine is the path field's line on the screen, without its padding.
func pathLine(m *Model) string {
	for _, l := range strings.Split(screen(m), "\n") {
		if i := strings.Index(l, "path      "); i >= 0 && !strings.HasPrefix(l[i+10:], " ") {
			l = l[i:]
			if j := strings.Index(l, "│"); j >= 0 {
				l = l[:j]
			}
			return strings.TrimRight(l, " ")
		}
	}
	return ""
}

// typeSettled types s a key at a time, running what each key starts, as
// listings ahead coming back between keys.
func typeSettled(m *Model, s string) {
	for _, r := range s {
		_, cmd := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		settle(m, cmd)
	}
}

// The path step suggests the rest of a directory's name as it is typed,
// lists the other matches, and lets ↑↓ choose and tab or → accept.
func TestPathSuggestsAsYouType(t *testing.T) {
	m, _, _ := projectFormModel(t)
	toPath(t, m, 1) // mini: ~ holds code and .cache
	wantScreen(t, m, "path      ~/▏code/")
	typeSettled(m, "c")
	wantScreen(t, m, "path      ~/c▏ode/")
	settle(m, press(m, "tab"))
	wantScreen(t, m, "path      ~/code/▏blob/", "blob  blog  shop", "tab complete · ↑↓ choose · enter next")
	typeSettled(m, "bl")
	wantScreen(t, m, "path      ~/code/bl▏ob/", "blob  blog")
	press(m, "down")
	wantScreen(t, m, "path      ~/code/bl▏og/")
	press(m, "down") // wraps
	wantScreen(t, m, "path      ~/code/bl▏ob/")
	press(m, "up")
	wantScreen(t, m, "path      ~/code/bl▏og/")
	typeSettled(m, "o") // an edit chooses the first again
	press(m, "backspace")
	wantScreen(t, m, "path      ~/code/bl▏ob/")
	press(m, "down")
	settle(m, press(m, "right"))
	if pathLine(m) != "path      ~/code/blog/▏" {
		t.Errorf("accepted: %q", pathLine(m))
	}
	if s := screen(m); strings.Contains(s, "↑↓ choose") || strings.Contains(s, "blob  blog") {
		t.Errorf("a directory with nothing in it still offers a choice:\n%s", s)
	}
}

// Hidden directories are suggested once a dot is typed.
func TestPathSuggestsHiddenDirectoriesAfterADot(t *testing.T) {
	m, _, _ := projectFormModel(t)
	toPath(t, m, 1)
	typeSettled(m, ".")
	wantScreen(t, m, "path      ~/.▏cache/")
}

// Listings ahead run in the background: typing goes on while one is out,
// and the suggestion appears when it comes back.
func TestPathSuggestionsNeverWait(t *testing.T) {
	m, _, _ := projectFormModel(t)
	toPath(t, m, 1)
	var pending tea.Cmd
	for _, r := range "code/" {
		if _, cmd := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)}); cmd != nil {
			pending = cmd
		}
	}
	typeText(m, "b")
	if s := screen(m); strings.Contains(s, "Listing") || pathLine(m) != "path      ~/code/b▏" {
		t.Fatalf("typing waited for the listing: %q\n%s", pathLine(m), s)
	}
	settle(m, pending)
	wantScreen(t, m, "path      ~/code/b▏lob/", "blob  blog")
}

// A listing from a connection the form no longer has is dropped.
func TestPathSuggestionsFromAnOldConnectionAreDropped(t *testing.T) {
	m, _, _ := projectFormModel(t)
	toPath(t, m, 1)
	var old tea.Cmd
	for _, r := range "code/" {
		if _, cmd := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)}); cmd != nil {
			old = cmd
		}
	}
	next := run(m, press(m, "ctrl+r")) // connected again; its own listing waits
	settle(m, old)
	if pathLine(m) != "path      ~/code/▏" {
		t.Fatalf("the old connection's listing was used:\n%s", screen(m))
	}
	settle(m, next)
	wantScreen(t, m, "path      ~/code/▏blob/")
}

// A listing that fails shows nothing while typing; tab lists again and
// says why.
func TestPathTabListsWhenNoListingIsThere(t *testing.T) {
	m, _, files := projectFormModel(t)
	files.listErr = errListing
	toPath(t, m, 1)
	if s := screen(m); strings.Contains(strings.ToLower(s), "cannot list") {
		t.Fatalf("a failed listing ahead was shown:\n%s", s)
	}
	settle(m, press(m, "tab"))
	wantScreen(t, m, "Cannot list")
	files.listErr = nil
	settle(m, press(m, "tab"))
	wantScreen(t, m, "path      ~/code/▏blob/")
}

// enter checks the path as typed, not with the suggestion added.
func TestPathEnterIgnoresTheSuggestion(t *testing.T) {
	m, _, _ := projectFormModel(t)
	toPath(t, m, 1)
	typeSettled(m, "code/bl")
	wantScreen(t, m, "path      ~/code/bl▏ob/")
	settle(m, press(m, "enter"))
	wantScreen(t, m, "/home/dev/code/bl: no such directory")
}
