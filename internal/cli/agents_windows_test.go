//go:build windows

package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/agentapi"
	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
)

// ssh cannot forward to a socket whose local path holds a drive's colon,
// so on Windows a server's forward reaches a loopback port, with the token
// its requests must carry.
func TestServersGetALoopbackAgentForwardOnWindows(t *testing.T) {
	api := agentapi.New(agentapi.Options{Laptop: "dev@hera", Source: &agentSource{}})
	defer api.Close()
	a := &agents{api: api, id: "k3f9q2", src: &agentSource{}}
	eng := engine.New(&config.Config{}, engine.Options{})
	a.src.eng.Store(eng)
	events, unsubscribe := eng.Events().Subscribe(8)
	defer unsubscribe()
	f, ok := a.forward("leto")
	if !ok || !strings.HasPrefix(f.Local, "127.0.0.1:") || f.Token == "" || f.Name != "k3f9q2.sock" {
		t.Fatalf("forward %+v, %v", f, ok)
	}
	if again, _ := a.forward("leto"); again.Local != f.Local || again.Token != f.Token {
		t.Fatalf("a second forward %+v, the first %+v", again, f)
	}
	select {
	case ev := <-events:
		if strings.Contains(ev.Message, "agents unavailable") {
			t.Fatalf("event %+v", ev)
		}
	case <-time.After(50 * time.Millisecond):
	}
}
