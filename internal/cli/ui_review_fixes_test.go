package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

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
	want := "Note: Nearhost running in a terminal is 0.0.1; this is " + Version + ". It was started by hand, so restart it there. Stop it with Ctrl-C in its terminal, or: nearhost stop Then start it again."
	if stdout != "" || flat(stderr) != want {
		t.Errorf("stdout %q, stderr %q, want %q", stdout, stderr, want)
	}
	if slices.Contains(old.asked(), "Stop") {
		t.Error("the core in a terminal was stopped")
	}
}

// A core that ran in a terminal and went says so with no log: its terminal
// was its log.
func TestCoreInATerminalGoneHasNoLogTail(t *testing.T) {
	stderr, code := coreGoneFrom(t, func(f *steeredCore) { f.info.StartedBy = control.StartedForeground }, func(f *steeredCore) { f.close() })
	if want := "Nearhost stopped. It ran in a terminal, which shows why.\n"; code != 1 || flat(stderr) != flat("Error: "+want) {
		t.Errorf("exit %d, stderr %q, want %q", code, stderr, want)
	}
}

// quitRun runs bare nearhost on the fake core f, served by the time the
// interface opens, and quits it with q. It returns what the run printed.
func quitRun(t *testing.T, f *steeredCore, spawn func(args []string)) (stdout, stderr string, code int) {
	t.Helper()
	b := fakeUI(t, true, new(bool))
	b.detach = f.detach(spawn)
	b.run = runProgram(func(p *tea.Program) {
		// Off the test's goroutine, so no t.Fatal: the run goes on, and
		// ends, either way.
		if !testutil.Eventually(10*time.Second, func() bool { return f.hub.Clients() == 1 }) {
			t.Error("timed out waiting for the interface to follow the core")
		}
		p.Send(keyQ)
	})
	type result struct {
		stdout, stderr string
		code           int
	}
	done := make(chan result, 1)
	go func() {
		stdout, stderr, code := runRootSplit(t, uiRoot(b))
		done <- result{stdout, stderr, code}
	}()
	select {
	case r := <-done:
		return r.stdout, r.stderr, r.code
	case <-time.After(30 * time.Second):
		t.Fatal("q did not quit the interface")
	}
	return "", "", 0
}

// keepRunningConfig is the YAML of webConfig with keep_running set to keep.
func keepRunningConfig(t *testing.T, keep bool) string {
	t.Helper()
	cfg, err := config.Parse([]byte(webConfig(t)))
	if err != nil {
		t.Fatal(err)
	}
	cfg.KeepRunning = &keep
	data, err := config.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Quitting the interface and leaving the core running says so, once the
// terminal is the shell's again; quitting that stops the core says nothing.
func TestLeavingTheCoreRunningSaysSo(t *testing.T) {
	const still = "Nearhost is still running in the background. Stop it with nearhost stop.\n"
	t.Run("started elsewhere", func(t *testing.T) {
		f := newSteeredCore(t, configHome(t))
		f.serve()
		stdout, stderr, code := quitRun(t, f, nil)
		if code != 0 || stdout != still {
			t.Errorf("exit %d, stdout %q, want %q; stderr %q", code, stdout, still, stderr)
		}
	})
	t.Run("keep_running true", func(t *testing.T) {
		f := newSteeredCore(t, configHome(t))
		// Started for the interface, as a core spawned for it says.
		f.info.StartedBy = control.StartedTUI
		f.st.Config = keepRunningConfig(t, true)
		stdout, stderr, code := quitRun(t, f, func([]string) { f.serve() })
		if code != 0 || stdout != still {
			t.Errorf("exit %d, stdout %q, want %q; stderr %q", code, stdout, still, stderr)
		}
		if slices.Contains(f.asked(), "Stop") {
			t.Error("the core was stopped")
		}
	})
	t.Run("keep_running false", func(t *testing.T) {
		f := newSteeredCore(t, configHome(t))
		// Started for the interface, as a core spawned for it says.
		f.info.StartedBy = control.StartedTUI
		f.st.Config = keepRunningConfig(t, false)
		stdout, stderr, code := quitRun(t, f, func([]string) { f.serve() })
		if code != 0 || stdout != "" {
			t.Errorf("exit %d, stdout %q, want nothing; stderr %q", code, stdout, stderr)
		}
		if !slices.Contains(f.asked(), "Stop") {
			t.Error("the core was not stopped")
		}
	})
}

// A core started for the interface is stopped when the interface fails to
// run: no interface would follow it.
func TestACoreStartedForAnInterfaceThatFailsIsStopped(t *testing.T) {
	f := newSteeredCore(t, configHome(t))
	f.info.StartedBy = control.StartedTUI
	b := fakeUI(t, true, new(bool))
	b.detach = f.detach(func([]string) { f.serve() })
	b.run = func(*tui.Model, func(*tea.Program)) error { return errors.New("no terminal after all") }
	if _, stderr, code := runRootSplit(t, uiRoot(b)); code == 0 {
		t.Fatalf("exit 0, stderr %q", stderr)
	}
	if !slices.Contains(f.asked(), "Stop") {
		t.Error("the core started for the interface was left running")
	}
}

// Ctrl-C while a core starts says what became of it: when another nearhost's
// core answered in its place, that one is left running; when the core does
// not answer before the wait is over, it may still be starting.
func TestInterruptedWhileTheCoreStartsSaysWhatHappened(t *testing.T) {
	t.Run("another nearhost's core", func(t *testing.T) {
		core := newSteeredCore(t, configHome(t))
		sigs := make(chan os.Signal, 1)
		ran := false
		b := fakeUI(t, true, &ran)
		b.stops = func() (<-chan os.Signal, func()) { return sigs, func() {} }
		d := core.detach(nil)
		// The child stops at once, because another core holds the lock.
		d.spawn = func(string, []string) (int, func() error, error) { return 0, func() error { return nil }, nil }
		d.held = func() bool {
			sigs <- os.Interrupt
			return true
		}
		b.detach = d
		_, stderr, code := runRootSplit(t, uiRoot(b))
		want := "Interrupted while Nearhost started in the background. Another nearhost started the Nearhost that runs, so it was left running.\n"
		if code != 130 || ran || flat(stderr) != flat("Error: "+want) {
			t.Errorf("exit %d, ran %v, stderr %q, want %q", code, ran, stderr, want)
		}
	})
	t.Run("no answer", func(t *testing.T) {
		core := newSteeredCore(t, configHome(t))
		sigs := make(chan os.Signal, 1)
		ran := false
		b := fakeUI(t, true, &ran)
		b.stops = func() (<-chan os.Signal, func()) { return sigs, func() {} }
		d := core.detach(func([]string) { sigs <- os.Interrupt })
		d.timeout = 2 * time.Second
		b.detach = d
		_, stderr, code := runRootSplit(t, uiRoot(b))
		logPath, err := config.LogPath()
		if err != nil {
			t.Fatal(err)
		}
		want := "Interrupted while Nearhost started in the background. It did not answer in time to be stopped, so it may still be starting. See how it is going in its log: " + logPath + " Stop it: nearhost stop\n"
		if code != 130 || ran || flat(stderr) != flat("Error: "+want) {
			t.Errorf("exit %d, ran %v, stderr %q, want %q", code, ran, stderr, want)
		}
		if slices.Contains(core.asked(), "Stop") {
			t.Error("a core that never answered was asked to stop")
		}
	})
}

// A restart names the config file only when it is not the default one, which
// may not exist yet, and keeps --no-hosts and the ports.
func TestRestartArgs(t *testing.T) {
	def := configHome(t)
	got := restartArgs(control.StartedDetached, control.State{ConfigPath: def, HTTPSPort: 8443, WebPort: 6327, NoHosts: true})
	want := []string{"daemon", "--started-by", "detached", "--no-hosts", "--https-port", "8443", "--web-port", "6327"}
	if !slices.Equal(got, want) {
		t.Errorf("on the default file: %q, want %q", got, want)
	}
	other := filepath.Join(t.TempDir(), "other.yaml")
	got = restartArgs(control.StartedTUI, control.State{ConfigPath: other})
	want = []string{"daemon", "--started-by", "tui", "--config", other, "--web-port", "0"}
	if !slices.Equal(got, want) {
		t.Errorf("on another file: %q, want %q", got, want)
	}
}
