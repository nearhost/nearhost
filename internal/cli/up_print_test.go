package cli

import (
	"bytes"
	"errors"
	"github.com/nearhost/nearhost/internal/report"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/hosts"
)

// The engine announces a state again when its reason changes. Each distinct
// line is printed; the same line twice in a row is printed once.
func TestUpPrintsAStateAgainOnlyWhenItsReasonChanges(t *testing.T) {
	cfg := &config.Config{Servers: mini, Projects: []config.Project{remoteProject("api"), remoteProject("blog")}}
	var out bytes.Buffer
	p := &upPrinter{out: &out, eng: engine.New(cfg, engine.Options{}), advise: true}
	at := time.Date(2026, 10, 13, 9, 30, 0, 0, time.Local)
	state := func(project string, st engine.State, msg string) {
		p.event(engine.Event{Time: at, Project: project, Server: "mini", State: st, Message: msg})
	}
	state("api", engine.StateDegraded, "state: degraded: server mini is reconnecting: connection refused")
	state("api", engine.StateDegraded, "state: degraded: server mini is reconnecting: connection refused")
	state("api", engine.StateDegraded, "state: degraded: server mini is reconnecting: no route to host")
	state("blog", engine.StateDegraded, "state: degraded: server mini is reconnecting: no route to host")
	state("api", engine.StateRunning, "state: running")
	state("api", engine.StateError, "state: error: loopback port 5174 is not forwarded: ssh could not bind it on this machine")
	state("api", engine.StateError, "state: error: loopback port 5174 is not forwarded: ssh could not bind it on this machine (in use by node (pid 42))")
	// A log line is never held back, however often it repeats.
	p.event(engine.Event{Time: at, Project: "api", Message: "bind: address already in use"})
	p.event(engine.Event{Time: at, Project: "api", Message: "bind: address already in use"})

	const degradedFix = "  fix: Nothing is needed while Nearhost reconnects. If the project stays degraded, fix access by running ssh once in a terminal:\n        ssh mini.lan\n"
	const errorFix = "  fix: Fix access by running ssh once in a terminal:\n        ssh mini.lan\n  fix: Then start it again:\n        nearhost up\n"
	want := "" +
		"09:30:00 [api] state: degraded: server mini is reconnecting: connection refused\n" + degradedFix +
		"09:30:00 [api] state: degraded: server mini is reconnecting: no route to host\n" + degradedFix +
		"09:30:00 [blog] state: degraded: server mini is reconnecting: no route to host\n" + degradedFix +
		"09:30:00 [api] state: running\n" +
		"09:30:00 [api] state: error: loopback port 5174 is not forwarded: ssh could not bind it on this machine\n" + errorFix +
		"09:30:00 [api] state: error: loopback port 5174 is not forwarded: ssh could not bind it on this machine (in use by node (pid 42))\n" + errorFix +
		"09:30:00 [api] bind: address already in use\n" +
		"09:30:00 [api] bind: address already in use\n"
	if out.String() != want {
		t.Fatalf("printed:\n%s\nwant:\n%s", out.String(), want)
	}
}

// A run directory too long for a control socket is not fixed by ssh in a
// terminal, which works: the fix, chosen by the problem's code and not its
// text, names the setting.
func TestUpPrintsTheRunDirectoryFixNotSSH(t *testing.T) {
	cfg := &config.Config{Servers: mini, Projects: []config.Project{remoteProject("api")}}
	var out bytes.Buffer
	p := &upPrinter{out: &out, eng: engine.New(cfg, engine.Options{})}
	reason := "control socket path /very/long/run/mini.sock is too long for ssh: it is 120 bytes and at most 86 bytes fit; the run directory /very/long/run is too long, set a shorter XDG_CONFIG_HOME"
	fixFor(p, "api", engine.StateError, reason, report.Errorf(report.SSHSocketPathTooLong, "%s", reason))
	if got := out.String(); !strings.Contains(got, "XDG_CONFIG_HOME to a shorter path and move the nearhost folder that holds your config there.\n  fix: Then start it again:\n        nearhost up\n") || strings.Contains(got, "ssh") {
		t.Fatalf("printed %q", got)
	}
	out.Reset()
	fixFor(p, "api", engine.StateError, "ssh mini: Permission denied (publickey).", nil)
	if got := out.String(); !strings.Contains(got, "\n        ssh mini.lan\n") {
		t.Fatalf("another reason lost the ssh advice: %q", got)
	}
}

// A stopped project refused for a read-only hosts file shows the message's
// own steps, the retry worded for up, when Nearhost wrote the file itself
// and the reason is the title alone. The elevated helper's reason is the
// old text, which says how to clear the file already: it gets no fix line,
// and approving again would fail the same.
func TestUpPrintsAReadOnlyHostsFilesSteps(t *testing.T) {
	cfg := &config.Config{Servers: mini, Projects: []config.Project{remoteProject("api")}}
	readOnly := report.Errorf(report.HostsReadOnly, `C:\hosts is read-only.`).
		Step("Clear the read-only attribute in an administrator terminal:", `attrib -r "C:\hosts"`).
		Step("Or serve without updating it:", "nearhost --no-hosts")
	direct := engine.HostsProblem("api", readOnly)
	for _, tc := range []struct {
		background bool
		want       string
	}{
		{false, "  fix: Clear the read-only attribute in an administrator terminal:\n        attrib -r \"C:\\hosts\"\n" +
			"  fix: Then start it again:\n        nearhost up\n" +
			"  fix: Or serve without updating it:\n        nearhost up --no-hosts\n"},
		{true, "  fix: Clear the read-only attribute in an administrator terminal:\n        attrib -r \"C:\\hosts\"\n" +
			"  fix: Then update the hosts file from a terminal, and Nearhost starts the project:\n        nearhost hosts update\n"},
	} {
		var out bytes.Buffer
		p := &upPrinter{out: &out, eng: engine.New(cfg, engine.Options{}), keepRunning: tc.background}
		fixFor(p, "api", engine.StateStopped, direct.Title, direct)
		if out.String() != tc.want {
			t.Errorf("background %v:\n%s\nwant:\n%s", tc.background, out.String(), tc.want)
		}
	}

	var out bytes.Buffer
	p := &upPrinter{out: &out, eng: engine.New(cfg, engine.Options{})}
	said := `hosts apply as administrator: C:\hosts is read-only. Clear it with: attrib -r "C:\hosts" (in an administrator terminal), or run with --no-hosts.`
	helper := engine.HostsProblem("api", errors.New(said))
	fixFor(p, "api", engine.StateStopped, helper.Title, helper)
	if out.Len() != 0 {
		t.Fatalf("output = %q, want none", out.String())
	}
	fixFor(p, "api", engine.StateStopped, "hosts update declined", nil)
	if out.Len() == 0 {
		t.Fatal("another reason lost its fix line")
	}
}

// A core in the background is not started again with `nearhost up`: its
// fixes say how to start it again in the background, and up's stay as they
// are.
func TestBackgroundFixesSayHowToRestartInTheBackground(t *testing.T) {
	cfg := &config.Config{Servers: mini, Projects: []config.Project{remoteProject("api"), localProject("web", 5173, true)}}
	at := time.Date(2026, 10, 13, 9, 30, 0, 0, time.Local)
	for _, tc := range []struct {
		name       string
		background bool
		print      func(p *upPrinter)
		want       string
	}{
		{"ssh", false, func(p *upPrinter) { fixFor(p, "api", engine.StateError, "server mini is unreachable", nil) },
			"  fix: Fix access by running ssh once in a terminal:\n        ssh mini.lan\n  fix: Then start it again:\n        nearhost up\n"},
		{"ssh", true, func(p *upPrinter) { fixFor(p, "api", engine.StateError, "server mini is unreachable", nil) },
			"  fix: Fix access by running ssh once in a terminal:\n        ssh mini.lan\n  fix: Then stop Nearhost:\n        nearhost stop\n  fix: And start it again in the background:\n        nearhost -d\n"},
		{"port", false, func(p *upPrinter) { fixFor(p, "web", engine.StateError, "port 5173 is in use", nil) },
			"  fix: Resolve what is named above (a port in use: stop the process holding it).\n  fix: Then start it again:\n        nearhost up\n"},
		{"port", true, func(p *upPrinter) { fixFor(p, "web", engine.StateError, "port 5173 is in use", nil) },
			"  fix: Resolve what is named above (a port in use: stop the process holding it).\n  fix: Then stop Nearhost:\n        nearhost stop\n  fix: And start it again in the background:\n        nearhost -d\n"},
		{"hosts", false, func(p *upPrinter) { fixFor(p, "web", engine.StateStopped, "hosts update failed", nil) },
			"  fix: Start it again and " + hostsApprove + ":\n        nearhost up\n  fix: Or add the project's hosts to " + hosts.DefaultPath + " yourself, then serve without updating it:\n        nearhost up --no-hosts\n"},
		{"hosts", true, func(p *upPrinter) { fixFor(p, "web", engine.StateStopped, "hosts update failed", nil) },
			"  fix: Update the hosts file from a terminal, and Nearhost starts the project:\n        nearhost hosts update\n"},
		{"pending", false, func(p *upPrinter) {
			p.event(engine.Event{Time: at, Project: "web", Message: engine.PendingPrefix + "routes"})
		}, "09:30:00 [web] " + engine.PendingPrefix + "routes\n  fix: Open the interface to approve them; nearhost up cannot:\n        nearhost\n"},
		{"pending", true, func(p *upPrinter) {
			p.event(engine.Event{Time: at, Project: "web", Message: engine.PendingPrefix + "routes"})
		}, "09:30:00 [web] " + engine.PendingPrefix + "routes\n  fix: Nearhost in the background cannot approve them.\n  fix: Stop it:\n        nearhost stop\n  fix: Then open the interface to approve them:\n        nearhost\n"},
	} {
		var out bytes.Buffer
		p := &upPrinter{out: &out, eng: engine.New(cfg, engine.Options{}), keepRunning: tc.background}
		tc.print(p)
		if out.String() != tc.want {
			t.Errorf("%s, background %v:\n%s\nwant:\n%s", tc.name, tc.background, out.String(), tc.want)
		}
	}
}

// writeCounter records each Write on its own, as approval lines written
// from another goroutine could fall between any two of them.
type writeCounter struct{ writes []string }

func (w *writeCounter) Write(p []byte) (int, error) {
	w.writes = append(w.writes, string(p))
	return len(p), nil
}

// An event and the fix under it, and the whole summary, are each written in
// one Write: the core's approval lines, written from a goroutine of their
// own, can come between them but never inside one.
func TestUpPrintsEachBlockInOneWrite(t *testing.T) {
	cfg := &config.Config{Servers: mini, Projects: []config.Project{remoteProject("api"), remoteProject("blog")}}
	var out writeCounter
	p := &upPrinter{out: &out, eng: engine.New(cfg, engine.Options{}), advise: true}
	at := time.Date(2026, 10, 13, 9, 30, 0, 0, time.Local)
	p.event(engine.Event{Time: at, Project: "api", Server: "mini", State: engine.StateDegraded, Message: "state: degraded: server mini is reconnecting: connection refused"})
	if len(out.writes) != 1 || !strings.Contains(out.writes[0], "[api] state: degraded") || !strings.Contains(out.writes[0], "  fix: ") {
		t.Errorf("an event and its fix took %d writes: %q", len(out.writes), out.writes)
	}
	out.writes = nil
	p.event(engine.Event{Time: at, Project: "api", Message: engine.PendingPrefix + "routes"})
	if len(out.writes) != 1 || !strings.Contains(out.writes[0], "Open the interface to approve them") {
		t.Errorf("a pending change and its note took %d writes: %q", len(out.writes), out.writes)
	}
	out.writes = nil
	p.summary([]string{"api", "blog"}, []error{errors.New("server mini is unreachable"), nil})
	if len(out.writes) != 1 || !strings.Contains(out.writes[0], "api: ") || !strings.Contains(out.writes[0], "blog: ") || !strings.Contains(out.writes[0], "nearhost up is stopping") {
		t.Errorf("the summary took %d writes: %q", len(out.writes), out.writes)
	}
	out.writes = nil
	p.event(engine.Event{Time: at, Project: "api", Message: "nothing to fix"})
	p.summary(nil, nil)
	if len(out.writes) != 2 {
		t.Errorf("a plain event and an empty summary took %d writes: %q", len(out.writes), out.writes)
	}
}

// fixFor writes what p says to do about the project called name in the
// state st, for reason, whose problem is problem.
func fixFor(p *upPrinter, name string, st engine.State, reason string, problem *report.Message) {
	p.writeSteps(p.steps(name, st, reason, problem))
}

// In the core's log, a change that waits for approval says why the core
// cannot approve it before the commands that do: the log shows a step's
// command alone, so the reason is a step of its own.
func TestCoreLogPendingSaysWhy(t *testing.T) {
	cfg := &config.Config{Servers: mini, Projects: []config.Project{remoteProject("web")}}
	var out bytes.Buffer
	p := &upPrinter{out: &out, eng: engine.New(cfg, engine.Options{}), keepRunning: true, stamped: true}
	msg := engine.PendingPrefix + "routes"
	p.event(engine.Event{Time: time.Now(), Project: "web", Message: msg, Report: report.Warnf(report.NearhostrcPending, "%s", msg)})
	want := "warning [nearhostrc.pending] web: " + msg + "\n" +
		"  Nearhost in the background cannot approve them.\n" +
		"  Fix: nearhost stop\n" +
		"  Fix: nearhost\n"
	if out.String() != want {
		t.Errorf("log:\n%s\nwant:\n%s", out.String(), want)
	}
}
