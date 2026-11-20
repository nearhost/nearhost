package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/engine"
)

func TestStopStopsTheCore(t *testing.T) {
	u := newUpRun(t, &config.Config{})
	u.noHosts = true
	u.startDaemon("--started-by", "detached")
	u.waitForLog("Nothing is being served.\n")

	var out syncBuffer
	err := executeInto(context.Background(), &out, newStopCmd(), "stop")
	if err != nil || out.String() != "Stopped.\n" {
		t.Fatalf("stop: %v, output %q, want Stopped.", err, out.String())
	}
	// Stopped means gone: no socket, and no lock on the run directory.
	if _, err := control.Dial(u.runDir()); !errors.Is(err, control.ErrNotRunning) {
		t.Errorf("after stop, Dial = %v, want ErrNotRunning", err)
	}
	if held, err := engine.InstanceRunning(u.runDir()); held || err != nil {
		t.Errorf("after stop, InstanceRunning = %v, %v", held, err)
	}
	if err := u.exited(); err != nil {
		t.Fatalf("daemon returned %v; log:\n%s", err, u.coreLog())
	}
}

func TestStopNotRunning(t *testing.T) {
	configHome(t)
	var out syncBuffer
	err := executeInto(context.Background(), &out, newStopCmd(), "stop")
	if code := exitCode(err, &out); code != 0 || out.String() != "Nearhost is not running.\n" {
		t.Fatalf("exit %d, output %q", code, out.String())
	}
}

// A core still connecting to a server that never answers stops, and lets go
// of its lock, ports and socket before stop returns: a core started straight
// after answers at once.
func TestStopAfterStopStartsAgain(t *testing.T) {
	u := newUpRun(t, &config.Config{Servers: mini, Projects: []config.Project{remoteProject("api")}})
	u.noHosts = true
	u.sshEnv = []string{"FAKE_SSH_HANG=1"}
	u.startDaemon("--started-by", "detached")
	u.waitForLog("[api] state: starting")

	var out syncBuffer
	if err := executeInto(context.Background(), &out, newStopCmd(), "stop"); err != nil || out.String() != "Stopped.\n" {
		t.Fatalf("stop: %v, output %q", err, out.String())
	}

	// At once, with the same run directory and ports, as `nearhost -d` would
	// straight after: not after the first daemon has returned.
	first := u.done
	u.startDaemon("--started-by", "detached")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := control.Dial(u.runDir()); err == nil {
			break
		}
		select {
		case err := <-u.done:
			t.Fatalf("the second daemon returned %v; log:\n%s", err, u.coreLog())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("the second daemon did not answer; log:\n%s", u.coreLog())
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case err := <-first:
		if err != nil {
			t.Errorf("the first daemon returned %v; log:\n%s", err, u.coreLog())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the first daemon did not return")
	}
	if log := u.coreLog(); strings.Contains(log, "already running") || strings.Contains(log, "address already in use") {
		t.Errorf("the second daemon found the first in its way:\n%s", log)
	}
	if err := u.stop(); err != nil {
		t.Fatalf("daemon returned %v; log:\n%s", err, u.coreLog())
	}
}
