package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/hosts"
	"github.com/nearhost/nearhost/internal/transport"
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

	const degradedFix = "  fix: None needed while nearhost reconnects; if the project stays degraded, run `ssh mini.lan` once in a terminal to fix access\n"
	const errorFix = "  fix: Run `ssh mini.lan` once in a terminal to fix access, then run `nearhost up` again\n"
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
// terminal, which works: the fix names the setting.
func TestUpPrintsTheRunDirectoryFixNotSSH(t *testing.T) {
	cfg := &config.Config{Servers: mini, Projects: []config.Project{remoteProject("api")}}
	var out bytes.Buffer
	p := &upPrinter{out: &out, eng: engine.New(cfg, engine.Options{})}
	reason := "control socket path /very/long/run/mini.sock is too long for ssh: it is 120 bytes and at most 86 bytes fit; the run directory /very/long/run is too long, set a shorter XDG_CONFIG_HOME"
	if !transport.IsSocketPathMessage(reason) {
		t.Fatal("the reason is not transport's message")
	}
	p.fix("api", engine.StateError, reason)
	if got := out.String(); !strings.Contains(got, "XDG_CONFIG_HOME to a shorter path and move the nearhost folder that holds your config there, then run `nearhost up` again") || strings.Contains(got, "`ssh") {
		t.Fatalf("printed %q", got)
	}
	out.Reset()
	p.fix("api", engine.StateError, "ssh mini: Permission denied (publickey).")
	if got := out.String(); !strings.Contains(got, "`ssh mini.lan` once") {
		t.Fatalf("another reason lost the ssh advice: %q", got)
	}
}

// A stopped project refused for a read-only hosts file gets no fix line: its
// reason says how to clear the file, and approving again would fail the same.
func TestUpPrintsNoFixForAReadOnlyHostsFile(t *testing.T) {
	cfg := &config.Config{Servers: mini, Projects: []config.Project{remoteProject("api")}}
	var out bytes.Buffer
	p := &upPrinter{out: &out, eng: engine.New(cfg, engine.Options{})}
	p.fix("api", engine.StateStopped, `C:\hosts is read-only. Clear it with: attrib -r "C:\hosts" (in an administrator terminal), or run with --no-hosts.`)
	if out.Len() != 0 {
		t.Fatalf("output = %q, want none", out.String())
	}
	p.fix("api", engine.StateStopped, "hosts update declined")
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
		{"ssh", false, func(p *upPrinter) { p.fix("api", engine.StateError, "server mini is unreachable") },
			"  fix: Run `ssh mini.lan` once in a terminal to fix access, then run `nearhost up` again\n"},
		{"ssh", true, func(p *upPrinter) { p.fix("api", engine.StateError, "server mini is unreachable") },
			"  fix: Run `ssh mini.lan` once in a terminal to fix access, then run `nearhost stop` and `nearhost -d`\n"},
		{"port", false, func(p *upPrinter) { p.fix("web", engine.StateError, "port 5173 is in use") },
			"  fix: Resolve what is named above (a port in use: stop the process holding it), then run `nearhost up` again\n"},
		{"port", true, func(p *upPrinter) { p.fix("web", engine.StateError, "port 5173 is in use") },
			"  fix: Resolve what is named above (a port in use: stop the process holding it), then run `nearhost stop` and `nearhost -d`\n"},
		{"hosts", false, func(p *upPrinter) { p.fix("web", engine.StateStopped, "hosts update failed") },
			"  fix: Run `nearhost up` again and " + hostsApprove + ", or add the project's hosts to " + hosts.DefaultPath + " yourself and pass --no-hosts\n"},
		{"hosts", true, func(p *upPrinter) { p.fix("web", engine.StateStopped, "hosts update failed") },
			"  fix: Run `nearhost hosts update` in a terminal, and Nearhost starts the project\n"},
		{"pending", false, func(p *upPrinter) {
			p.event(engine.Event{Time: at, Project: "web", Message: engine.PendingPrefix + "routes"})
		}, "09:30:00 [web] " + engine.PendingPrefix + "routes\n  open nearhost to approve them; nearhost up cannot\n"},
		{"pending", true, func(p *upPrinter) {
			p.event(engine.Event{Time: at, Project: "web", Message: engine.PendingPrefix + "routes"})
		}, "09:30:00 [web] " + engine.PendingPrefix + "routes\n  run `nearhost stop` and open nearhost to approve them; Nearhost in the background cannot\n"},
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
	if len(out.writes) != 1 || !strings.Contains(out.writes[0], "open nearhost to approve them") {
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
