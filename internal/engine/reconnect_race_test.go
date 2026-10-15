package engine

import (
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/testutil"
)

// A reconnect that waits for a session Apply is dropping does not start it
// again: it would be left running, unknown to the engine.
func TestReconnectDoesNotRestartADroppedSession(t *testing.T) {
	h := newHarness(t, remoteConfig(testutil.FreePort(t)))
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	// Apply stops a server's projects before it drops the session.
	if err := h.eng.StopProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	h.eng.mu.Lock()
	c := h.eng.servers["mini"]
	h.eng.mu.Unlock()

	c.ensure.Lock() // the reconnect will wait here
	reconnected := make(chan error, 1)
	go func() { reconnected <- h.eng.ReconnectServer("mini") }()
	time.Sleep(50 * time.Millisecond)
	dropped := make(chan struct{})
	go func() {
		h.eng.dropServer("mini") // as Apply does when the host changes
		close(dropped)
	}()
	testutil.WaitUntil(t, 5*time.Second, func() bool {
		h.eng.mu.Lock()
		defer h.eng.mu.Unlock()
		return h.eng.servers["mini"] == nil
	}, "the session to leave the engine")
	c.ensure.Unlock()

	if err := <-reconnected; err == nil {
		t.Error("the reconnect went ahead on a dropped session")
	}
	<-dropped
	// The harness's cleanup checks that no ssh process is left running.
}
