package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/engine"
)

// restartRig is a core of the release before an update, 0.5.0, that
// nearhost update finds running, and the cores a restart starts in its
// place, which say they are 0.6.0.
type restartRig struct {
	t       *testing.T
	cfgPath string
	old     *steeredCore

	mu sync.Mutex
	// spawned is each core spawned, as the exe and its arguments.
	spawned [][]string
}

// newRestartRig serves an old core that says it was started by by.
func newRestartRig(t *testing.T, by control.StartedBy) *restartRig {
	t.Helper()
	cfgPath := configHome(t)
	old := newSteeredCore(t, cfgPath)
	old.info.Version = "0.5.0"
	old.info.StartedBy = by
	return &restartRig{t: t, cfgPath: cfgPath, old: old}
}

// next serves a core of the new release that says it was started by by.
func (r *restartRig) next(by control.StartedBy) {
	core := newSteeredCore(r.t, r.cfgPath)
	core.info.Version = "0.6.0"
	core.info.PID = 5151
	core.info.StartedBy = by
	core.serve()
}

// detach reaches the old core, and spawns a new one in its place.
func (r *restartRig) detach() *detachBackend {
	b := r.old.detach(nil)
	b.spawn = func(exe string, args []string) (int, func() error, error) {
		r.mu.Lock()
		r.spawned = append(r.spawned, append([]string{exe}, args...))
		r.mu.Unlock()
		by := control.StartedDetached
		if i := slices.Index(args, "--started-by"); i >= 0 && i+1 < len(args) {
			by = control.StartedBy(args[i+1])
		}
		r.next(by)
		block := make(chan struct{})
		return 0, func() error { <-block; return nil }, nil
	}
	return &b
}

func (r *restartRig) spawns() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.spawned)
}

func (r *restartRig) stopped() bool { return slices.Contains(r.old.asked(), "Stop") }

// liveState gives the old core two open passthroughs and a running project
// on a server, which a restart would close.
func liveState(t *testing.T, st *control.State) {
	t.Helper()
	data, err := config.Marshal(&config.Config{Projects: []config.Project{localProject("web", 8000, true), remoteProject("api")}})
	if err != nil {
		t.Fatal(err)
	}
	st.Config = string(data)
	st.Projects = map[string]engine.ProjectStatus{"web": {State: engine.StateRunning}, "api": {State: engine.StateRunning}}
	st.Passthroughs = []engine.Passthrough{
		{PassthroughSpec: engine.PassthroughSpec{ID: "a", Server: "mini", Remote: 3000}},
		{PassthroughSpec: engine.PassthroughSpec{ID: "b", Server: "mini", Remote: 5173}},
	}
}

// updateFor is an update from 0.5.0 to 0.6.0 by the install script on macOS,
// which has no setcap advice, with rig's old core running.
func updateFor(rig *restartRig, tty bool) *fakeUpdate {
	return &fakeUpdate{latest: "0.6.0", exe: "/usr/local/bin/nearhost", writable: true, tty: tty, goos: "darwin", detach: rig.detach()}
}

const restartedLine = "Restarted Nearhost (pid 5151); it now runs 0.6.0.\n"

// With nothing live, the core is restarted without asking: as it was
// started, --no-capture kept, from the new binary.
func TestUpdateRestartsADetachedCore(t *testing.T) {
	rig := newRestartRig(t, control.StartedDetached)
	rig.old.st.NoCapture = true
	rig.old.st.Passthroughs = nil
	rig.old.serve()
	out, err := runUpdate(t, updateFor(rig, false), "0.5.0", "", "--yes")
	if err != nil {
		t.Fatalf("err %v, out %q", err, out)
	}
	if !strings.HasSuffix(out, "Updated to 0.6.0.\n"+restartedLine) {
		t.Errorf("out %q, want it to end with the restart", out)
	}
	if !rig.stopped() {
		t.Error("the old core was not stopped")
	}
	want := append([]string{"/usr/local/bin/nearhost"}, restartArgs(control.StartedDetached, rig.old.st)...)
	if got := rig.spawns(); len(got) != 1 || !slices.Equal(got[0], want) {
		t.Errorf("spawned %q, want %q", got, want)
	}
	if !slices.Contains(want, "--no-capture") {
		t.Errorf("restartArgs %q lack --no-capture", want)
	}
}

// Live passthroughs and forwards are asked about first.
func TestUpdateAsksWhenLive(t *testing.T) {
	prompt := "Restart Nearhost now to run 0.6.0? This closes 2 passthroughs and 1 forward for a moment. [Y/n] "
	t.Run("no", func(t *testing.T) {
		rig := newRestartRig(t, control.StartedDetached)
		liveState(t, &rig.old.st)
		rig.old.serve()
		out, err := runUpdate(t, updateFor(rig, true), "0.5.0", "y\nn\n")
		if err != nil {
			t.Fatalf("err %v, out %q", err, out)
		}
		want := "Updated to 0.6.0.\n" + prompt + "Nearhost in the background still runs 0.5.0. Restart it when you are ready: nearhost stop, then nearhost -d.\n"
		if !strings.HasSuffix(out, want) {
			t.Errorf("out %q, want it to end with %q", out, want)
		}
		if rig.stopped() || len(rig.spawns()) != 0 {
			t.Errorf("stopped %v, spawned %q: the core was restarted", rig.stopped(), rig.spawns())
		}
	})
	t.Run("yes", func(t *testing.T) {
		rig := newRestartRig(t, control.StartedDetached)
		liveState(t, &rig.old.st)
		rig.old.serve()
		out, err := runUpdate(t, updateFor(rig, true), "0.5.0", "y\n\n")
		if err != nil {
			t.Fatalf("err %v, out %q", err, out)
		}
		if want := "Updated to 0.6.0.\n" + prompt + restartedLine; !strings.HasSuffix(out, want) {
			t.Errorf("out %q, want it to end with %q", out, want)
		}
		if !rig.stopped() || len(rig.spawns()) != 1 {
			t.Errorf("stopped %v, spawned %q, want a restart", rig.stopped(), rig.spawns())
		}
	})
}

// --yes restarts a live core without asking, with no terminal.
func TestUpdateYesRestartsWithoutAsking(t *testing.T) {
	rig := newRestartRig(t, control.StartedDetached)
	liveState(t, &rig.old.st)
	rig.old.serve()
	out, err := runUpdate(t, updateFor(rig, false), "0.5.0", "", "--yes")
	if err != nil {
		t.Fatalf("err %v, out %q", err, out)
	}
	if strings.Contains(out, "Restart Nearhost now") || !strings.HasSuffix(out, "Updated to 0.6.0.\n"+restartedLine) {
		t.Errorf("out %q, want a restart without a question", out)
	}
	if !rig.stopped() || len(rig.spawns()) != 1 {
		t.Errorf("stopped %v, spawned %q, want a restart", rig.stopped(), rig.spawns())
	}
}

// restartCmd is a command for restartAfterUpdate, which prints to out.
func restartCmd(out *bytes.Buffer) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	return cmd
}

// With no terminal to ask at and no --yes, a live core is left. update itself
// does not get this far without one or the other, so the restart is called on
// its own.
func TestUpdateNoTerminalLeavesALiveCore(t *testing.T) {
	rig := newRestartRig(t, control.StartedDetached)
	liveState(t, &rig.old.st)
	rig.old.serve()
	f := updateFor(rig, false)
	var out bytes.Buffer
	if err := restartAfterUpdate(restartCmd(&out), f.backend(), bufio.NewReader(strings.NewReader("")), false, false, "0.6.0", "/usr/local/bin/nearhost"); err != nil {
		t.Fatal(err)
	}
	if want := "Nearhost in the background still runs 0.5.0. Restart it when you are ready: nearhost stop, then nearhost -d.\n"; out.String() != want {
		t.Errorf("out %q, want %q", out.String(), want)
	}
	if rig.stopped() || len(rig.spawns()) != 0 {
		t.Errorf("stopped %v, spawned %q: the core was restarted", rig.stopped(), rig.spawns())
	}
}

// A core in a terminal is the user's to restart there.
func TestUpdateLeavesAForegroundCore(t *testing.T) {
	rig := newRestartRig(t, control.StartedForeground)
	rig.old.info.PID = 777
	rig.old.serve()
	out, err := runUpdate(t, updateFor(rig, false), "0.5.0", "", "--yes")
	if err != nil {
		t.Fatalf("err %v, out %q", err, out)
	}
	if want := "Updated to 0.6.0.\nNearhost running in a terminal (pid 777) is still 0.5.0. Restart it there: Ctrl-C, then start it again.\n"; !strings.HasSuffix(out, want) {
		t.Errorf("out %q, want it to end with %q", out, want)
	}
	if rig.stopped() || len(rig.spawns()) != 0 {
		t.Errorf("stopped %v, spawned %q: the core was restarted", rig.stopped(), rig.spawns())
	}
}

// A core that already runs the new release, or none at all, is left be.
func TestUpdateNothingToRestart(t *testing.T) {
	t.Run("new already", func(t *testing.T) {
		rig := newRestartRig(t, control.StartedDetached)
		rig.old.info.Version = "0.6.0"
		rig.old.serve()
		out, err := runUpdate(t, updateFor(rig, false), "0.5.0", "", "--yes")
		if err != nil || !strings.HasSuffix(out, "Updated to 0.6.0.\n") || rig.stopped() {
			t.Errorf("err %v, out %q, stopped %v", err, out, rig.stopped())
		}
	})
	t.Run("none", func(t *testing.T) {
		rig := newRestartRig(t, control.StartedDetached)
		out, err := runUpdate(t, updateFor(rig, false), "0.5.0", "", "--yes")
		if err != nil || !strings.HasSuffix(out, "Updated to 0.6.0.\n") || len(rig.spawns()) != 0 {
			t.Errorf("err %v, out %q, spawned %q", err, out, rig.spawns())
		}
	})
}

// The service's core is restarted through the service when it runs the new
// binary, and nothing is spawned.
func TestUpdateRestartsServiceThroughManager(t *testing.T) {
	rig := newRestartRig(t, control.StartedService)
	rig.old.serve()
	svc := &fakeService{exe: "/usr/local/bin/nearhost", active: true, start: func() error {
		rig.next(control.StartedService)
		return nil
	}}
	f := updateFor(rig, false)
	f.detach.service = svc.lookup()
	out, err := runUpdate(t, f, "0.5.0", "", "--yes")
	if err != nil {
		t.Fatalf("err %v, out %q", err, out)
	}
	if !strings.HasSuffix(out, restartedLine) {
		t.Errorf("out %q, want it to end with %q", out, restartedLine)
	}
	if starts, _ := svc.counts(); starts != 1 || !rig.stopped() {
		t.Errorf("the service was started %d times, stopped %v; want a restart through it", starts, rig.stopped())
	}
	if got := rig.spawns(); len(got) != 0 {
		t.Errorf("spawned %q beside the service", got)
	}
}

// A service that runs another copy of nearhost would start that copy again:
// update says so, as the interface does, and restarts nothing.
func TestUpdateServiceOfAnotherExe(t *testing.T) {
	rig := newRestartRig(t, control.StartedService)
	rig.old.serve()
	svc := &fakeService{exe: "/opt/nearhost/bin/nearhost", start: func() error {
		t.Error("the service was started")
		return nil
	}}
	f := updateFor(rig, false)
	f.detach.service = svc.lookup()
	out, err := runUpdate(t, f, "0.5.0", "", "--yes")
	if err != nil {
		t.Fatalf("err %v, out %q", err, out)
	}
	if want := "Nearhost's service runs /opt/nearhost/bin/nearhost, which is 0.5.0. To have it run this copy (0.6.0), run nearhost service install from it.\n"; !strings.HasSuffix(out, want) {
		t.Errorf("out %q, want it to end with %q", out, want)
	}
	if rig.stopped() || len(rig.spawns()) != 0 {
		t.Errorf("stopped %v, spawned %q: the core was restarted", rig.stopped(), rig.spawns())
	}
}

// A core started detached beside an installed service is restarted detached:
// it is not the service's.
func TestUpdateDetachedBesideService(t *testing.T) {
	rig := newRestartRig(t, control.StartedDetached)
	rig.old.serve()
	svc := &fakeService{exe: "/usr/local/bin/nearhost", start: func() error {
		t.Error("the service was started")
		return nil
	}}
	f := updateFor(rig, false)
	f.detach.service = svc.lookup()
	out, err := runUpdate(t, f, "0.5.0", "", "--yes")
	if err != nil {
		t.Fatalf("err %v, out %q", err, out)
	}
	if !strings.HasSuffix(out, restartedLine) {
		t.Errorf("out %q, want it to end with %q", out, restartedLine)
	}
	if got := rig.spawns(); len(got) != 1 || !slices.Equal(got[0][1:4], []string{"daemon", "--started-by", "detached"}) {
		t.Errorf("spawned %q, want one core started detached", got)
	}
}

// The interface's core comes back detached, as no interface would own it, and
// the user is told the window closes.
func TestUpdateTUICoreComesBackDetached(t *testing.T) {
	rig := newRestartRig(t, control.StartedTUI)
	rig.old.serve()
	out, err := runUpdate(t, updateFor(rig, false), "0.5.0", "", "--yes")
	if err != nil {
		t.Fatalf("err %v, out %q", err, out)
	}
	if want := "Updated to 0.6.0.\nThe open nearhost window will close; open nearhost again.\n" + restartedLine; !strings.HasSuffix(out, want) {
		t.Errorf("out %q, want it to end with %q", out, want)
	}
	if got := rig.spawns(); len(got) != 1 || !slices.Equal(got[0][1:4], []string{"daemon", "--started-by", "detached"}) {
		t.Errorf("spawned %q, want one core started detached", got)
	}
}

// A Homebrew upgrade puts the new release in a new Caskroom folder: the core
// is started from bin/nearhost, the path that was checked, not the running
// binary's folder.
func TestUpdateHomebrewExe(t *testing.T) {
	rig := newRestartRig(t, control.StartedDetached)
	rig.old.serve()
	f := updateFor(rig, false)
	f.exe = "/opt/homebrew/Caskroom/nearhost/0.5.0/nearhost"
	out, err := runUpdate(t, f, "0.5.0", "", "--yes")
	if err != nil {
		t.Fatalf("err %v, out %q", err, out)
	}
	if got := rig.spawns(); len(got) != 1 || got[0][0] != "/opt/homebrew/bin/nearhost" {
		t.Errorf("spawned %q, want /opt/homebrew/bin/nearhost", got)
	}
}

// On Linux, a new binary that cannot bind the core's ports would not start:
// the core is left, after the setcap advice, to be restarted once that is
// done.
func TestUpdateLeavesACoreTheNewBinaryCannotServe(t *testing.T) {
	rig := newRestartRig(t, control.StartedDetached)
	rig.old.st.HTTPSPort, rig.old.st.HTTPPort = 443, 80
	rig.old.serve()
	f := updateFor(rig, false)
	f.goos = "linux"
	out, err := runUpdate(t, f, "0.5.0", "", "--yes")
	if err != nil {
		t.Fatalf("err %v, out %q", err, out)
	}
	want := "Updated to 0.6.0.\nLet the new binary bind ports 80 and 443 again:\n\n  sudo setcap cap_net_bind_service=+ep '/usr/local/bin/nearhost'\n\n" +
		"Nearhost in the background still runs 0.5.0: the new binary cannot serve on port 80 until you run the command above. Run it, then restart Nearhost: nearhost stop, then nearhost -d.\n"
	if !strings.HasSuffix(out, want) {
		t.Errorf("out %q, want it to end with %q", out, want)
	}
	if rig.stopped() {
		t.Error("the core was stopped")
	}

	// With the capability, or on its own ports, it is restarted.
	rig = newRestartRig(t, control.StartedDetached)
	rig.old.st.HTTPSPort, rig.old.st.HTTPPort = 8443, 8080
	rig.old.serve()
	f = updateFor(rig, false)
	f.goos = "linux"
	if out, err := runUpdate(t, f, "0.5.0", "", "--yes"); err != nil || !strings.HasSuffix(out, restartedLine) {
		t.Errorf("on ports 8443 and 8080: err %v, out %q", err, out)
	}
}

func TestRestartArgsKeepNoCapture(t *testing.T) {
	def := configHome(t)
	got := restartArgs(control.StartedDetached, control.State{ConfigPath: def, WebPort: 6327, NoHosts: true, NoCapture: true})
	want := []string{"daemon", "--started-by", "detached", "--no-hosts", "--no-capture", "--web-port", "6327"}
	if !slices.Equal(got, want) {
		t.Errorf("%q, want %q", got, want)
	}
}

// live counts the open passthroughs, and the running projects on a server
// other than this machine, which a restart closes.
func TestLive(t *testing.T) {
	var st control.State
	liveState(t, &st)
	data, err := config.Marshal(&config.Config{Projects: []config.Project{
		localProject("web", 8000, true), remoteProject("api"), remoteProject("stopped"), remoteProject("starting"),
		{Name: "bare", Path: "/tmp/bare", Alias: "bare.test", Enabled: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	st.Config = string(data)
	st.Projects["stopped"] = engine.ProjectStatus{State: engine.StateStopped}
	st.Projects["starting"] = engine.ProjectStatus{State: engine.StateStarting}
	// A project with no server named is on this machine.
	st.Projects["bare"] = engine.ProjectStatus{State: engine.StateRunning}
	if p, f := live(st); p != 2 || f != 1 {
		t.Errorf("live = %d passthroughs, %d forwards; want 2 and 1", p, f)
	}
	if p, f := live(control.State{}); p != 0 || f != 0 {
		t.Errorf("live of nothing = %d, %d", p, f)
	}
}

// liveWords says what a restart closes, leaving out a part that is none.
func TestLiveWords(t *testing.T) {
	for _, tc := range []struct {
		p, f int
		want string
	}{
		{2, 1, "2 passthroughs and 1 forward"},
		{1, 0, "1 passthrough"},
		{0, 3, "3 forwards"},
	} {
		if got := liveWords(tc.p, tc.f); got != tc.want {
			t.Errorf("liveWords(%d, %d) = %q, want %q", tc.p, tc.f, got, tc.want)
		}
	}
}

// A core is restarted only on a binary: a pnpm shim that is a Windows .cmd or
// a #! script would start a shell or node in between, with a console window
// and another pid, so the core is left for the user to restart.
func TestUpdatePnpmRestartNeedsABinary(t *testing.T) {
	const leaveLine = "Nearhost in the background still runs 0.5.0. Restart it when you are ready: nearhost stop, then nearhost -d.\n"
	darwinStore := "/Users/me/Library/pnpm/global/5/.pnpm/@nearhost+cli-darwin-arm64@0.5.0/node_modules/@nearhost/cli-darwin-arm64/bin/nearhost"
	for _, tc := range []struct {
		name    string
		set     func(f *fakeUpdate)
		restart string // the exe spawned; "" is none
	}{
		{"windows cmd shim", func(f *fakeUpdate) {
			f.goos = "windows"
			f.exe = `C:\Users\x\AppData\Local\pnpm\global\5\node_modules\.pnpm\@nearhost+cli-win32-x64@0.5.0\node_modules\@nearhost\cli-win32-x64\bin\nearhost.exe`
			f.onPath = `C:\Users\x\AppData\Local\pnpm\nearhost.CMD`
		}, ""},
		{"shell shim", func(f *fakeUpdate) {
			f.exe = darwinStore
			f.onPath = "/Users/me/Library/pnpm/nearhost"
			f.files = map[string]string{"/Users/me/Library/pnpm/nearhost": "#!/bin/sh\nexec node \"$basedir/global/5/node_modules/@nearhost/cli/bin/nearhost.js\" \"$@\"\n"}
			f.regular = map[string]bool{} // no platform package found behind it
		}, ""},
		{"binary", func(f *fakeUpdate) {
			f.exe = darwinStore
			f.onPath = "/Users/me/Library/pnpm/nearhost"
		}, "/Users/me/Library/pnpm/nearhost"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newRestartRig(t, control.StartedDetached)
			rig.old.serve()
			f := updateFor(rig, false)
			tc.set(f)
			out, err := runUpdate(t, f, "0.5.0", "", "--yes")
			if err != nil {
				t.Fatalf("err %v, out %q", err, out)
			}
			got := rig.spawns()
			if tc.restart == "" {
				if !strings.HasSuffix(out, leaveLine) || len(got) != 0 || rig.stopped() {
					t.Errorf("out %q, spawned %q, stopped %v; want the core left", out, got, rig.stopped())
				}
				return
			}
			if !strings.HasSuffix(out, restartedLine) || len(got) != 1 || got[0][0] != tc.restart {
				t.Errorf("out %q, spawned %q; want a restart from %s", out, got, tc.restart)
			}
		})
	}
}

// The service's core is restarted directly, and said to be detached, when the
// service cannot be read.
func TestUpdateServiceCoreWithAnUnreadableService(t *testing.T) {
	rig := newRestartRig(t, control.StartedService)
	rig.old.serve()
	svc := &fakeService{
		exe:       "/usr/local/bin/nearhost",
		installed: func(context.Context) error { return errors.New("launchctl print: exit status 5") },
		start: func() error {
			t.Error("the service was started")
			return nil
		},
	}
	f := updateFor(rig, false)
	f.detach.service = svc.lookup()
	out, err := runUpdate(t, f, "0.5.0", "", "--yes")
	if err != nil || !strings.HasSuffix(out, restartedLine) {
		t.Fatalf("err %v, out %q", err, out)
	}
	if got := rig.spawns(); len(got) != 1 || !slices.Equal(got[0][1:4], []string{"daemon", "--started-by", "detached"}) {
		t.Errorf("spawned %q, want one core started detached", got)
	}
}
