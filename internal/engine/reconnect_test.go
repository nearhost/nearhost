package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/testutil"
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
