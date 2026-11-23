//go:build unix

package cli

import (
	"context"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/agentapi"
	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/testutil"
	"github.com/nearhost/nearhost/internal/transport"
)

// These tests ask for passthroughs during up, which only a server's agent
// socket takes. On Unix that socket's path is known from the run directory;
// on Windows it is a loopback port with a token the test cannot see, and
// TestWithdrawnPassthroughEndsInTheQueue and TestApprovalLine cover the
// same ground there.

// serverAgentUp is an up serving the project api on mini, with coding
// agents and grants, and the page. It returns once mini's agent socket
// answers, with the forward that reaches it.
// adjust, when not nil, changes the backend too.
func serverAgentUp(t *testing.T, adjust func(*upBackend)) (*upRun, transport.AgentForward) {
	t.Helper()
	home := agentHome(t)
	u := newUpRun(t, &config.Config{Servers: mini, Projects: []config.Project{remoteProject("api")}})
	u.noHosts = true
	u.adjust = func(b *upBackend) {
		b.agents = withGrants(fakeAgents(home), home)
		if adjust != nil {
			adjust(b)
		}
	}
	u.start()
	f := transport.AgentForward{Local: serverAgentSocket(u.runDir(), "mini")}
	testutil.WaitUntil(t, 30*time.Second, func() bool { return agentapi.Answers(f.Local) }, "mini's agent socket to answer")
	return u, f
}

func TestUpPrintsApprovalLines(t *testing.T) {
	engines := make(chan *engine.Engine, 1)
	u, f := serverAgentUp(t, giveEngine(engines))
	page, err := (<-engines).PageURL()
	if err != nil {
		t.Fatal(err)
	}
	status, out := postPass(t, f, "")
	if status != 202 {
		t.Fatalf("passthrough request: %d %v", status, out)
	}
	id := out["id"].(string)
	u.waitFor("Claude Code on mini asks to open port 5173. Answer at " + page + "/approve/" + id + "\n")
	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
	}
}

// A passthrough request the agent withdraws while it waits leaves the core's
// state, and a client following the stream is told it ended.
func TestWithdrawnRequestLeavesTheQueue(t *testing.T) {
	u, f := serverAgentUp(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := u.control()
	envs, streamErr, err := c.Events(ctx, u.hubSeq(t))
	if err != nil {
		t.Fatal(err)
	}
	status, out := postPass(t, f, "")
	if status != 202 {
		t.Fatalf("passthrough request: %d %v", status, out)
	}
	id := out["id"].(string)
	if st, err := c.State(ctx); err != nil || len(st.Approvals) != 1 || st.Approvals[0].ID != id {
		t.Fatalf("before the withdrawal: approvals %+v, %v", st.Approvals, err)
	}
	if status := deleteAgent(t, f, "/v1/passthroughs/"+id); status != 204 {
		t.Fatalf("withdraw: %d", status)
	}
	timeout := time.After(10 * time.Second)
	for ended := false; !ended; {
		select {
		case env, ok := <-envs:
			if !ok {
				t.Fatalf("the stream ended before it said the request ended: %v", streamErr())
			}
			ended = env.Kind == control.KindEnded && env.ID == id
		case <-timeout:
			t.Fatal("the stream did not say the request ended")
		}
	}
	if st, err := c.State(ctx); err != nil || len(st.Approvals) != 0 {
		t.Errorf("after the withdrawal: approvals %+v, %v", st.Approvals, err)
	}
	cancel()
	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
	}
}
