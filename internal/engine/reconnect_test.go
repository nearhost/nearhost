package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/testutil"
	"github.com/nearhost/nearhost/internal/transport"
)

func TestReconnectServerStartsTheMasterAgain(t *testing.T) {
	h := newHarness(t, remoteConfig(testutil.FreePort(t)))
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	masters := func() int { return strings.Count(h.log(), "-N -M ") }
	if n := masters(); n != 1 {
		t.Fatalf("%d masters before reconnecting", n)
	}
	if err := h.eng.ReconnectServer("mini"); err != nil {
		t.Fatalf("ReconnectServer: %v", err)
	}
	testutil.WaitUntil(t, 10*time.Second, func() bool { return masters() == 2 }, "a second master")
	waitState(t, h, "myapp", StateRunning)
	if f := h.forwards("mini"); len(f) != 2 {
		t.Errorf("forwards after reconnecting = %v", f)
	}
}

func TestReconnectServerRefusals(t *testing.T) {
	h := newHarness(t, remoteConfig(testutil.FreePort(t)))
	for name, want := range map[string]string{
		"mini":             "nothing is connected to mini yet",
		"nowhere":          `unknown server "nowhere"`,
		config.LocalServer: "this machine has no connection to reconnect",
	} {
		if err := h.eng.ReconnectServer(name); err == nil || err.Error() != want {
			t.Errorf("ReconnectServer(%q) = %v, want %q", name, err, want)
		}
	}
	if err := h.eng.Shutdown(bg); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.ReconnectServer("mini"); err == nil {
		t.Error("ReconnectServer after Shutdown succeeded")
	}
}

// A server disconnected on purpose stays down, its projects degraded, until
// it is connected again; then they come back with their forwards.
func TestDisconnectAndConnectServer(t *testing.T) {
	h := newHarness(t, remoteConfig(testutil.FreePort(t)))
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	masters := func() int { return strings.Count(h.log(), "-N -M ") }
	if err := h.eng.DisconnectServer("mini"); err != nil {
		t.Fatalf("DisconnectServer: %v", err)
	}
	waitState(t, h, "myapp", StateDegraded)
	if st := h.eng.Status("myapp"); st.Message != "server mini was disconnected on purpose" {
		t.Errorf("message %q", st.Message)
	}
	if hh := h.eng.ServerHealth("mini"); !hh.Held || hh.Status != transport.StatusDisconnected {
		t.Errorf("health %+v", hh)
	}
	// Nothing starts the session behind the user's back.
	if err := h.eng.StopProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.StartProject(bg, "myapp"); err == nil || !strings.Contains(err.Error(), "mini was disconnected; connect it first") {
		t.Errorf("StartProject while held = %v", err)
	}
	if n := masters(); n != 1 {
		t.Errorf("%d masters while held", n)
	}
	if err := h.eng.ConnectServer("mini"); err != nil {
		t.Fatalf("ConnectServer: %v", err)
	}
	testutil.WaitUntil(t, 10*time.Second, func() bool { return h.eng.ServerHealth("mini").Status == transport.StatusConnected }, "connected")
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	waitState(t, h, "myapp", StateRunning)
	if hh := h.eng.ServerHealth("mini"); hh.Held {
		t.Error("still held after connecting")
	}
}

// Connecting a server nothing has connected to yet starts its session;
// reconnecting a held server connects it.
func TestConnectServerFromNothingAndReconnectAHeldOne(t *testing.T) {
	h := newHarness(t, remoteConfig(testutil.FreePort(t)))
	if err := h.eng.ConnectServer("mini"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitUntil(t, 10*time.Second, func() bool { return h.eng.ServerHealth("mini").Status == transport.StatusConnected }, "connected")
	if err := h.eng.DisconnectServer("mini"); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.ReconnectServer("mini"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitUntil(t, 10*time.Second, func() bool { return h.eng.ServerHealth("mini").Status == transport.StatusConnected }, "connected again")
	for name, want := range map[string]string{
		config.LocalServer: "this machine has no connection to make",
		"nowhere":          `unknown server "nowhere"`,
	} {
		if err := h.eng.ConnectServer(name); err == nil || err.Error() != want {
			t.Errorf("ConnectServer(%q) = %v, want %q", name, err, want)
		}
	}
	if err := h.eng.DisconnectServer(config.LocalServer); err == nil || err.Error() != "this machine has no connection to drop" {
		t.Errorf("DisconnectServer(local) = %v", err)
	}
}

// A reload that changes a dropped server's host does not connect it: the
// hold outlives the session it was made on. Its project, started afresh by
// the reload, is refused and says why; connecting brings the server back.
// A server removed from the config loses its hold.
func TestAReloadKeepsADroppedServerDropped(t *testing.T) {
	cfg := remoteConfig(testutil.FreePort(t))
	h := newHarness(t, cfg)
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	masters := func() int { return strings.Count(h.log(), "-N -M ") }
	if err := h.eng.DisconnectServer("mini"); err != nil {
		t.Fatal(err)
	}
	moved := remoteConfig(cfg.Projects[0].Routes[1].Port)
	moved.Servers[0].SSHHost = "mini.lan"
	if err := h.eng.Apply(bg, moved); err != nil {
		t.Fatal(err)
	}
	waitState(t, h, "myapp", StateError)
	if st := h.eng.Status("myapp"); !strings.Contains(st.Message, "mini was disconnected; connect it first") {
		t.Errorf("message %q", st.Message)
	}
	if hh := h.eng.ServerHealth("mini"); !hh.Held {
		t.Errorf("the hold did not survive the reload: %+v", hh)
	}
	if n := masters(); n != 1 {
		t.Errorf("%d masters after the reload, want the first only", n)
	}
	if err := h.eng.ConnectServer("mini"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitUntil(t, 10*time.Second, func() bool { return h.eng.ServerHealth("mini").Status == transport.StatusConnected }, "connected")

	if err := h.eng.DisconnectServer("mini"); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.Apply(bg, &config.Config{}); err != nil {
		t.Fatal(err)
	}
	if hh := h.eng.ServerHealth("mini"); hh.Held {
		t.Errorf("a server gone from the config is still held: %+v", hh)
	}
}
