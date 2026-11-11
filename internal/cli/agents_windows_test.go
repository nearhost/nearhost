//go:build windows

package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
)

// ssh cannot forward a socket whose local path holds a drive's colon, so a
// session on Windows forwards none, and the run says why.
func TestNoServerGetsTheAgentSocketOnWindows(t *testing.T) {
	a := &agents{src: &agentSource{}}
	eng := engine.New(&config.Config{}, engine.Options{})
	a.src.eng.Store(eng)
	events, unsubscribe := eng.Events().Subscribe(8)
	defer unsubscribe()
	if f, ok := a.forward("mini"); ok {
		t.Fatalf("forward %+v on Windows", f)
	}
	select {
	case ev := <-events:
		if ev.Server != "mini" || ev.Message != "mini: agents unavailable: coding agents on servers cannot reach Nearhost on Windows yet" {
			t.Fatalf("event %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("nothing was logged")
	}
}

// A session can ask for its forward before the engine exists; the reason is
// kept and sent once when the engine is attached.
func TestServerAgentsUnavailableIsReportedEvenBeforeAttachOnWindows(t *testing.T) {
	a := &agents{src: &agentSource{}}
	for range 2 {
		if _, ok := a.forward("mini"); ok {
			t.Fatal("forward on Windows")
		}
	}
	eng := engine.New(&config.Config{}, engine.Options{})
	events, unsubscribe := eng.Events().Subscribe(8)
	defer unsubscribe()
	a.attach(eng)
	select {
	case ev := <-events:
		if ev.Server != "mini" || ev.Message != "mini: agents unavailable: coding agents on servers cannot reach Nearhost on Windows yet" {
			t.Fatalf("event %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("nothing was logged on attach")
	}
	select {
	case ev := <-events:
		t.Fatalf("second event %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

// Doctor says plainly that agents on servers are not there yet on Windows,
// without asking the server.
func TestDoctorSaysAgentsOnServersAreNotOnWindowsYetOnWindows(t *testing.T) {
	b := doctorBackend{checkAgents: func(context.Context, config.Server) error {
		t.Error("asked the server about agents")
		return nil
	}}
	c := checkAgents(context.Background(), b, config.Server{Name: "mini", SSHHost: "mini.lan"})
	if c.level != levelOK || c.name != "agents on mini" || !strings.Contains(c.detail, "cannot reach Nearhost on Windows yet") {
		t.Fatalf("check %+v", c)
	}
}
