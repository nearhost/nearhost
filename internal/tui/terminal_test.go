package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// When a step that needs root has to run in the plain terminal, Nearhost
// says who is asking and why, from the start of a fresh line.
func TestAsking(t *testing.T) {
	got := ansi.Strip(Asking("Nearhost needs your password to trust its local certificate authority, so browsers accept HTTPS from Nearhost's web server.", 50))
	want := "\r\n" +
		" ∩ nearhost\r\n" +
		" ────────────────────────────────────────────────\r\n" +
		" Nearhost needs your password to trust its local\r\n" +
		" certificate authority, so browsers accept HTTPS\r\n" +
		" from Nearhost's web server.\r\n" +
		"\r\n"
	if got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

// Afterwards it says how it went, and that the interface comes back.
func TestTold(t *testing.T) {
	if got := ansi.Strip(Told(nil, "Trusted.")); got != "\r\n ✓ Trusted.\r\n Back to nearhost…\r\n" {
		t.Errorf("done: %q", got)
	}
	if got := ansi.Strip(Told(errors.New("sudo: 3 incorrect password attempts"), "Trusted.")); got != "\r\n ✗ Sudo: 3 incorrect password attempts\r\n Back to nearhost…\r\n" {
		t.Errorf("failed: %q", got)
	}
	if !strings.Contains(Told(errors.New("x"), "Trusted."), "38;2;242;107;107") {
		t.Error("an error is not red")
	}
}
