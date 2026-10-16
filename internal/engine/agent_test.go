package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/testutil"
	"github.com/nearhost/nearhost/internal/transport"
)

func TestEachServerIsAskedForItsAgentSocketOnce(t *testing.T) {
	var mu sync.Mutex
	asked := map[string]int{}
	h := buildHarness(t, remoteConfig(testutil.FreePort(t)), func(o *Options) {
		o.Agent = func(server string) (transport.AgentForward, bool) {
			mu.Lock()
			asked[server]++
			mu.Unlock()
			return transport.AgentForward{Name: "k3f9q2.sock", Local: "/run/nh/agent-mini.sock"}, true
		}
	})
	if err := h.eng.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	// The fake ssh has no SFTP, so readying ~/.nearhost fails: the server's
	// health says so and the project serves regardless.
	deadline := time.Now().Add(5 * time.Second)
	for h.eng.ServerHealth("mini").AgentErr == "" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if hl := h.eng.ServerHealth("mini"); hl.AgentErr == "" || hl.Status != transport.StatusConnected {
		t.Fatalf("health %+v", hl)
	}
	if st := h.eng.Status("myapp"); st.State != StateRunning {
		t.Fatalf("status %+v", st)
	}
	if err := h.eng.StopProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if asked["mini"] != 1 || len(asked) != 1 {
		t.Fatalf("asked %v", asked)
	}
}
