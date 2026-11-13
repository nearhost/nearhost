//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
)

// These tests make a server's agent socket impossible to listen on by putting
// a file where the run directory should be. On Windows a server's forward
// reaches a loopback port, which needs no run directory, so this cannot fail
// it there. TestAgentNoticesAreHeldUntilTheEngineIsAttached checks on every
// OS how the notice is held and logged.

func TestAServersSocketThatCannotBeListenedOnIsLogged(t *testing.T) {
	home := agentHome(t)
	blocked := filepath.Join(home, "run")
	os.WriteFile(blocked, []byte("a file where the run directory should be"), 0o600)
	opts := engine.Options{RunDir: blocked}
	a, _ := serveAgents(fakeAgents(home), &opts, nil, filepath.Join(home, "config.yaml"), 443, agentEnv{})
	defer a.close()
	eng := engine.New(&config.Config{}, engine.Options{})
	a.attach(eng)
	events, unsubscribe := eng.Events().Subscribe(8)
	defer unsubscribe()
	if _, ok := opts.Agent("mini"); ok {
		t.Fatal("a forward without a socket to reach")
	}
	select {
	case ev := <-events:
		if ev.Server != "mini" || !strings.Contains(ev.Message, "mini: agents unavailable: ") {
			t.Fatalf("event %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("nothing was logged")
	}
}

func TestAServersSocketThatCannotBeListenedOnIsLoggedOnceTheEngineIsAttached(t *testing.T) {
	home := agentHome(t)
	blocked := filepath.Join(home, "run")
	os.WriteFile(blocked, []byte("a file where the run directory should be"), 0o600)
	opts := engine.Options{RunDir: blocked}
	a, _ := serveAgents(fakeAgents(home), &opts, nil, filepath.Join(home, "config.yaml"), 443, agentEnv{})
	defer a.close()
	for range 2 {
		if _, ok := opts.Agent("mini"); ok {
			t.Fatal("a forward without a socket to reach")
		}
	}
	eng := engine.New(&config.Config{}, engine.Options{})
	// The order of up and the interface: attach, then subscribe, and only
	// then are the held events sent.
	a.attach(eng)
	events, unsubscribe := eng.Events().Subscribe(8)
	defer unsubscribe()
	a.publishHeld(eng)
	select {
	case ev := <-events:
		if ev.Server != "mini" || !strings.Contains(ev.Message, "mini: agents unavailable: ") {
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
