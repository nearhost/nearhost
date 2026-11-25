package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/nearhost/nearhost/internal/report"
)

// When a step that needs root has to run in the plain terminal, Nearhost
// says who is asking and why, from the start of a fresh line.
func TestAsking(t *testing.T) {
	got := ansi.Strip(Asking("Nearhost needs your password to trust its local certificate authority, so browsers accept HTTPS from its web server.", 50))
	want := "\r\n" +
		" ∩ nearhost\r\n" +
		" ────────────────────────────────────────────────\r\n" +
		" Nearhost needs your password to trust its local\r\n" +
		" certificate authority, so browsers accept HTTPS\r\n" +
		" from its web server.\r\n" +
		"\r\n"
	if got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

// Afterwards it says how it went, and that the interface comes back: an
// error is rendered as a message, as the terminal shows one, steps and all.
func TestTold(t *testing.T) {
	if got := ansi.Strip(Told(nil, "Trusted.", 72)); got != "\r\n ✓ Trusted.\r\n Back to nearhost…\r\n" {
		t.Errorf("done: %q", got)
	}
	if got := ansi.Strip(Told(errors.New("sudo: 3 incorrect password attempts"), "Trusted.", 72)); got != "\r\n Error: sudo: 3 incorrect password attempts\r\n Back to nearhost…\r\n" {
		t.Errorf("failed: %q", got)
	}
	m := report.Errorf(report.HostsNeedsUpdate, "The hosts file was not updated.").Step("Update it in a terminal:", "nearhost hosts update")
	want := "\r\n Error: The hosts file was not updated.\r\n\r\n Update it in a terminal:\r\n\r\n   nearhost hosts update\r\n Back to nearhost…\r\n"
	if got := ansi.Strip(Told(m, "Added.", 72)); got != want {
		t.Errorf("message:\n%q\nwant\n%q", got, want)
	}
	if t.Setenv("NO_COLOR", ""); !strings.Contains(Told(errors.New("x"), "Trusted.", 72), "\x1b[") {
		t.Error("an error has no colour")
	}
}
