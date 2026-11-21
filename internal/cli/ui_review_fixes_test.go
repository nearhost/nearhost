package cli

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/testutil"
	"github.com/nearhost/nearhost/internal/tui"
)

// The state a real core gives carries the settings of its configuration,
// keep_running and notifications among them, and the quit choice made
// through it honours keep_running: the box is not asked again once the user
// said not to.
func TestCoreStateCarriesKeepRunning(t *testing.T) {
	yes, no := true, false
	r := newClientRun(t, &config.Config{KeepRunning: &yes, Notifications: &no})
	r.u.startDaemon("--started-by", "tui")
	testutil.WaitUntil(t, 30*time.Second, func() bool { _, err := control.Dial(r.u.runDir()); return err == nil }, "the core to answer")
	c, err := control.Dial(r.u.runDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := c.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse([]byte(st.Config))
	if err != nil {
		t.Fatalf("the state's config does not parse: %v", err)
	}
	if cfg.KeepRunning == nil || !*cfg.KeepRunning {
		t.Errorf("the state's keep_running is %v, want true", cfg.KeepRunning)
	}
	if cfg.Notifications == nil || *cfg.Notifications {
		t.Errorf("the state's notifications is %v, want false", cfg.Notifications)
	}
	b := newClientBackend(c, st, &localOps{}, &tui.Proposals{}, true)
	t.Cleanup(b.Close)
	if got := b.QuitChoice(); got != tui.QuitLeave {
		t.Errorf("QuitChoice = %v with keep_running: true, want QuitLeave", got)
	}
}

// restartRun runs bare nearhost, answering yes to the restart, against a core
// of an older release that old describes, and returns the arguments the new
// core was started with and the interface's backend.
func restartRun(t *testing.T, old *steeredCore) (spawned [][]string, back *clientBackend, stdout string) {
	t.Helper()
	old.info.Version = "0.0.1"
	old.serve()
	ran := false
	b := fakeUI(t, true, &ran)
	b.detach = old.detach(func(args []string) {
		spawned = append(spawned, args)
		next := newSteeredCore(t, old.st.ConfigPath)
		if i := slices.Index(args, "--started-by"); i >= 0 {
			next.info.StartedBy = control.StartedBy(args[i+1])
		}
		next.serve()
	})
	b.clientMade = func(c *clientBackend) { back = c }
	root := uiRoot(b)
	root.SetIn(strings.NewReader("y\n"))
	stdout, stderr, code := runRootSplit(t, root)
	if code != 0 || !ran {
		t.Fatalf("exit %d, ran %v, stderr %q", code, ran, stderr)
	}
	if !slices.Contains(old.asked(), "Stop") {
		t.Fatal("the old core was not stopped")
	}
	return spawned, back, stdout
}

// A restart after an update starts the new core as the old one was: on the
// config file it served, on its ports, and said to be started by what
// started the old one. A core started by nearhost -d stays the user's: the
// interface leaves it running when it quits.
func TestARestartKeepsTheOldCoresSettings(t *testing.T) {
	configHome(t)
	custom := filepath.Join(t.TempDir(), "x.yaml")
	old := newSteeredCore(t, custom)
	old.st.HTTPSPort, old.st.HTTPPort, old.st.WebPort = 9443, 9080, 7327
	spawned, back, _ := restartRun(t, old)
	want := []string{"daemon", "--started-by", "detached", "--config", custom, "--https-port", "9443", "--http-port", "9080", "--web-port", "7327"}
	if len(spawned) != 1 || !slices.Equal(spawned[0], want) {
		t.Fatalf("spawned %q, want %q", spawned, want)
	}
	if back == nil || back.started {
		t.Fatalf("the interface counts the restarted core as its own")
	}
	if got := back.QuitChoice(); got != tui.QuitLeave {
		t.Errorf("QuitChoice = %v after restarting a detached core, want QuitLeave", got)
	}
}

// A core the interface started, restarted, is still the interface's.
func TestARestartOfTheInterfacesCoreStaysItsOwn(t *testing.T) {
	old := newSteeredCore(t, configHome(t))
	old.info.StartedBy = control.StartedTUI
	spawned, back, _ := restartRun(t, old)
	if len(spawned) != 1 || !slices.Equal(spawned[0][:3], []string{"daemon", "--started-by", "tui"}) {
		t.Fatalf("spawned %q, want a core started by the interface", spawned)
	}
	if back == nil || !back.started {
		t.Fatal("the interface does not count the restarted core as its own")
	}
}

// A core of another release that runs in a terminal is not offered a
// restart, which would take it from its terminal: the user is told how to
// restart it, and the interface opens on it.
func TestACoreInATerminalIsNotRestarted(t *testing.T) {
	old := newSteeredCore(t, configHome(t))
	old.info.Version = "0.0.1"
	old.info.StartedBy = control.StartedForeground
	old.serve()
	ran := false
	b := fakeUI(t, true, &ran)
	b.detach = old.detach(nil)
	root := uiRoot(b)
	root.SetIn(strings.NewReader("y\n"))
	stdout, stderr, code := runRootSplit(t, root)
	if code != 0 || !ran {
		t.Fatalf("exit %d, ran %v, stderr %q", code, ran, stderr)
	}
	want := "Nearhost running in a terminal is 0.0.1; this is " + Version + ". It was started by hand, so restart it there: stop it with Ctrl-C or nearhost stop, then start it again.\n"
	if stdout != want {
		t.Errorf("stdout %q, want %q", stdout, want)
	}
	if slices.Contains(old.asked(), "Stop") {
		t.Error("the core in a terminal was stopped")
	}
}
