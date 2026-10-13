package cli

import (
	"bytes"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
)

// The engine announces a state again when its reason changes. Each distinct
// line is printed; the same line twice in a row is printed once.
func TestUpPrintsAStateAgainOnlyWhenItsReasonChanges(t *testing.T) {
	cfg := &config.Config{Servers: mini, Projects: []config.Project{remoteProject("api"), remoteProject("blog")}}
	var out bytes.Buffer
	p := &upPrinter{out: &out, eng: engine.New(cfg, engine.Options{}), cfg: cfg, advise: true}
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

	const degradedFix = "  fix: none needed while nearhost reconnects; if the project stays degraded, run `ssh mini.lan` once in a terminal to fix access\n"
	const errorFix = "  fix: run `ssh mini.lan` once in a terminal to fix access, then run `nearhost up` again\n"
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
