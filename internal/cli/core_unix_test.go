//go:build unix

package cli

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/agentapi"
	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
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
func serverAgentUp(t *testing.T) (*upRun, transport.AgentForward) {
	t.Helper()
	home := agentHome(t)
	u := newUpRun(t, &config.Config{Servers: mini, Projects: []config.Project{remoteProject("api")}})
	u.noHosts = true
	u.adjust = func(b *upBackend) { b.agents = withGrants(fakeAgents(home), home) }
	u.start()
	name := fmt.Sprintf("agent-%x", sha256.Sum256([]byte("mini")))[:len("agent-")+12]
	f := transport.AgentForward{Local: filepath.Join(u.runDir(), name+".sock")}
	testutil.WaitUntil(t, 30*time.Second, func() bool { return agentapi.Answers(f.Local) }, "mini's agent socket to answer")
	return u, f
}

func TestUpPrintsApprovalLines(t *testing.T) {
	u, f := serverAgentUp(t)
	st, err := u.control().State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	status, out := postPass(t, f, "")
	if status != 202 {
		t.Fatalf("passthrough request: %d %v", status, out)
	}
	id := out["id"].(string)
	page := fmt.Sprintf("https://nearhost.localhost:%d", st.WebPort)
	u.waitFor("Claude Code on mini asks to open port 5173. Answer at " + page + "/approve/" + id + "\n")
	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
	}
}

// A passthrough request the agent withdraws while it waits leaves the core's
// state, and a client following the stream is told it ended.
func TestWithdrawnRequestLeavesTheQueue(t *testing.T) {
	u, f := serverAgentUp(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := u.control()
	envs, _, err := c.Events(ctx, u.hubSeq(t))
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
		case env := <-envs:
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
