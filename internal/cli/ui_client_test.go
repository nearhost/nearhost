package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/hosts"
	"github.com/nearhost/nearhost/internal/testutil"
	"github.com/nearhost/nearhost/internal/tui"
)

// These tests run bare `nearhost` against a core in this process: a real
// daemon, which the fake detach starts as `nearhost -d` would start one in
// the background, or a fake core that a test steers.

// clientRun is bare `nearhost` whose core is a daemon in this process.
type clientRun struct {
	t *testing.T
	// u holds the daemon's config, ports and backend.
	u *upRun

	mu      sync.Mutex
	spawned [][]string
}

// newClientRun gives the test its own config home holding cfg, and ports, for
// a daemon that serves no hosts file.
func newClientRun(t *testing.T, cfg *config.Config) *clientRun {
	t.Helper()
	u := newUpRun(t, cfg)
	u.noHosts = true
	return &clientRun{t: t, u: u}
}

// flags are the serve flags bare `nearhost` is given, which the daemon it
// starts is given too.
func (r *clientRun) flags() []string {
	return []string{"--https-port", strconv.Itoa(r.u.httpsPort), "--http-port", strconv.Itoa(r.u.httpPort),
		"--web-port", strconv.Itoa(r.u.webPort), "--no-hosts"}
}

// detach starts the daemon in this process with the arguments it is given,
// and reaches it on the run directory's socket. A daemon it started is
// stopped when the test ends.
func (r *clientRun) detach() detachBackend {
	sys := systemDetach()
	return detachBackend{
		spawn: func(exe string, args []string) (func() error, error) {
			r.mu.Lock()
			r.spawned = append(r.spawned, append([]string{exe}, args...))
			r.mu.Unlock()
			var out syncBuffer
			done := make(chan error, 1)
			go func() { done <- executeInto(context.Background(), &out, newDaemonCmd(r.u.backend()), args...) }()
			r.t.Cleanup(func() { r.stopCore(done) })
			return func() error {
				err := <-done
				done <- err
				return err
			}, nil
		},
		dial:    sys.dial,
		logSize: sys.logSize,
		logTail: sys.logTail,
		exe:     func() string { return "/bin/nearhost" },
		gone:    sys.gone,
		timeout: 30 * time.Second,
	}
}

// stopCore stops a core the test left running, and waits for the daemon in
// done to return.
func (r *clientRun) stopCore(done chan error) {
	if c, err := control.Dial(r.u.runDir()); err == nil {
		c.Stop(context.Background())
	}
	select {
	case err := <-done:
		done <- err
	case <-time.After(60 * time.Second):
		r.t.Error("the core did not stop")
	}
}

func (r *clientRun) calls() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.spawned)
}

// ui is a backend for bare `nearhost` that reaches the daemon, and runs the
// interface with run.
func (r *clientRun) ui(run func(m *tui.Model, attach func(*tea.Program)) error) uiBackend {
	b := fakeUI(r.t, true, new(bool))
	b.detach = r.detach()
	b.run = run
	return b
}

// runProgram runs the interface in a real program, with no terminal, and
// gives the program to started once it is made.
func runProgram(started func(p *tea.Program)) func(m *tui.Model, attach func(*tea.Program)) error {
	return func(m *tui.Model, attach func(*tea.Program)) error {
		p := tea.NewProgram(m, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithWindowSize(100, 30), tea.WithoutSignalHandler())
		attach(p)
		go started(p)
		_, err := p.Run()
		return err
	}
}

// drawn is what the model draws in a window 100 by 30, before its program
// runs.
func drawn(m *tui.Model) string {
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m.View().Content
}

// keyQ is the q key.
var keyQ = tea.KeyPressMsg{Code: 'q', Text: "q"}

// With no core running, bare nearhost starts one in the background, said to
// be started by the interface, with the serve flags it was given, and the
// interface shows what that core serves.
func TestNearhostStartsTheCore(t *testing.T) {
	r := newClientRun(t, &config.Config{Projects: []config.Project{localProject("shop", testutil.Upstream(t, "shop"), true)}})
	var screen string
	b := r.ui(func(m *tui.Model, _ func(*tea.Program)) error {
		screen = drawn(m)
		return nil
	})
	if _, stderr, code := runRootSplit(t, uiRoot(b), r.flags()...); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	want := append([]string{"/bin/nearhost", "daemon", "--started-by", "tui"}, r.flags()...)
	if got := r.calls(); len(got) != 1 || !slices.Equal(got[0], want) {
		t.Errorf("spawned %q, want %q", got, want)
	}
	if !strings.Contains(screen, "shop") {
		t.Errorf("the interface does not show the core's project:\n%s", screen)
	}
	c, err := control.Dial(r.u.runDir())
	if err != nil {
		t.Fatalf("no core answers: %v", err)
	}
	if info, err := c.Info(context.Background()); err != nil || info.StartedBy != control.StartedTUI {
		t.Errorf("the core says %+v, %v; want started by tui", info, err)
	}
}

// A config file named that does not exist yet is no reason to refuse: the
// interface opens on an empty config, and its core waits for one.
func TestNearhostOpensWithNoConfigFile(t *testing.T) {
	r := newClientRun(t, &config.Config{})
	ran := false
	b := r.ui(func(*tui.Model, func(*tea.Program)) error {
		ran = true
		return nil
	})
	missing := filepath.Join(t.TempDir(), "config.yaml")
	if _, stderr, code := runRootSplit(t, uiRoot(b), append([]string{"--config", missing}, r.flags()...)...); code != 0 || !ran {
		t.Fatalf("exit %d, ran %v, stderr %q", code, ran, stderr)
	}
}

// A core already running in the background is the interface's: nothing is
// started, and quitting leaves the core running without asking, as the
// interface did not start it.
func TestNearhostAttachesToARunningCore(t *testing.T) {
	r := newClientRun(t, &config.Config{})
	r.u.startDaemon("--started-by", "detached")
	testutil.WaitUntil(t, 30*time.Second, func() bool { _, err := control.Dial(r.u.runDir()); return err == nil }, "the core to answer")
	var back *clientBackend
	var choice tui.QuitChoice
	b := r.ui(runProgram(func(p *tea.Program) { p.Send(keyQ) }))
	b.clientMade = func(c *clientBackend) { back = c }
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, stderr, code := runRootSplit(t, uiRoot(b), r.flags()...); code != 0 {
			t.Errorf("exit %d, stderr %q", code, stderr)
		}
		choice = back.QuitChoice()
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("q did not quit the interface")
	}
	if got := r.calls(); len(got) != 0 {
		t.Errorf("spawned %q with a core running", got)
	}
	if choice != tui.QuitLeave {
		t.Errorf("QuitChoice = %v, want QuitLeave", choice)
	}
	c, err := control.Dial(r.u.runDir())
	if err != nil {
		t.Fatalf("the core does not answer after the interface quit: %v", err)
	}
	if _, err := c.Info(context.Background()); err != nil {
		t.Errorf("the core does not answer after the interface quit: %v", err)
	}
}

// Quitting asks whether to stop the core only when this process started it
// and is its one client; keep_running answers for the user once saved.
func TestQuitChoice(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name    string
		started bool
		keep    *bool
		clients int
		want    tui.QuitChoice
	}{
		{"started here, keep_running absent", true, nil, 1, tui.QuitAsk},
		{"started here, keep_running true", true, &yes, 1, tui.QuitLeave},
		{"started here, keep_running false", true, &no, 1, tui.QuitStop},
		{"started here, another client", true, nil, 2, tui.QuitLeave},
		{"started here, keep_running false, another client", true, &no, 2, tui.QuitLeave},
		{"started elsewhere", false, nil, 1, tui.QuitLeave},
		{"started elsewhere, keep_running false", false, &no, 1, tui.QuitLeave},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := config.Marshal(&config.Config{KeepRunning: tc.keep})
			if err != nil {
				t.Fatal(err)
			}
			b := newClientBackend(nil, control.State{Config: string(data), Clients: tc.clients}, &localOps{}, &tui.Proposals{}, tc.started)
			if got := b.QuitChoice(); got != tc.want {
				t.Errorf("QuitChoice = %v, want %v", got, tc.want)
			}
		})
	}
}

// Shutdown stops the core and returns once it has let go of the instance
// lock, as `nearhost stop` waits: a Nearhost started then finds it free.
func TestShutdownStopsTheCoreAndWaits(t *testing.T) {
	r := newClientRun(t, &config.Config{})
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
	b := newClientBackend(c, st, &localOps{}, &tui.Proposals{}, true)
	b.gone = systemDetach().gone
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := b.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if held, err := engine.InstanceRunning(r.u.runDir()); err != nil || held {
		t.Errorf("the instance lock is held (%v, %v) once Shutdown has returned", held, err)
	}
	if err := r.u.finish("after Shutdown"); err != nil {
		t.Errorf("the core returned %v", err)
	}
}

// steeredCore is a mirrorCore that a test serves on the control socket of the
// config home's run directory, where the CLI looks for the core.
type steeredCore struct {
	*mirrorCore
	t      *testing.T
	runDir string
	hub    *control.Hub

	mu  sync.Mutex
	srv *control.Server
}

// newSteeredCore makes a core of this release that serves the config file
// cfgPath, holding the local project web, and stops as Stop asks, writing
// what a core writes to its log as it begins to.
func newSteeredCore(t *testing.T, cfgPath string) *steeredCore {
	t.Helper()
	runDir, err := config.RunDir()
	if err != nil {
		t.Fatal(err)
	}
	f := &steeredCore{
		t:      t,
		runDir: runDir,
		hub:    control.NewHub(),
		mirrorCore: &mirrorCore{
			info: &control.Info{API: control.APIVersion, Version: Version, PID: 4242, StartedBy: control.StartedDetached, Reloads: true},
			st: control.State{
				ConfigPath: cfgPath,
				Config:     webConfig(t),
				HTTPSPort:  8443, HTTPPort: 8080, WebPort: 6327,
				Projects: map[string]engine.ProjectStatus{"web": {State: engine.StateRunning}},
				Servers:  map[string]control.ServerState{},
			},
		},
	}
	f.onStop = func() {
		f.log("shutting down")
		f.close()
	}
	return f
}

// serve answers on the socket.
func (f *steeredCore) serve() {
	f.t.Helper()
	if err := os.MkdirAll(f.runDir, 0o700); err != nil {
		f.t.Fatal(err)
	}
	srv, err := control.Listen(control.SocketPath(f.runDir), f.mirrorCore, f.hub)
	if err != nil {
		f.t.Fatal(err)
	}
	f.mu.Lock()
	f.srv = srv
	f.mu.Unlock()
	f.t.Cleanup(f.close)
}

// close stops answering, as a core that has gone.
func (f *steeredCore) close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.srv != nil {
		f.srv.Close()
		f.srv = nil
	}
}

// log adds lines to the core log, stamped as a core stamps them.
func (f *steeredCore) log(lines ...string) {
	path, err := config.LogPath()
	if err != nil {
		f.t.Error(err)
		return
	}
	lf, err := openCoreLog(path, false)
	if err != nil {
		f.t.Error(err)
		return
	}
	defer lf.Close()
	for _, l := range lines {
		fmt.Fprintln(tsWriter(lf), l)
	}
}

// detach reaches the fake core, and starts it with spawn, which gets the
// arguments the core is started with; nil starts nothing.
func (f *steeredCore) detach(spawn func(args []string)) detachBackend {
	sys := systemDetach()
	return detachBackend{
		spawn: func(exe string, args []string) (func() error, error) {
			if spawn == nil {
				f.t.Errorf("spawned %q with a core running", args)
				return nil, errors.New("no core can be started in this test")
			}
			spawn(args)
			block := make(chan struct{})
			return func() error { <-block; return nil }, nil
		},
		dial:    sys.dial,
		logSize: sys.logSize,
		logTail: sys.logTail,
		exe:     func() string { return "/bin/nearhost" },
		gone:    sys.gone,
		timeout: 10 * time.Second,
	}
}

// A --config that names another file than the core serves is refused: the
// interface edits the core's file.
func TestOtherConfigRefused(t *testing.T) {
	cfgPath := configHome(t)
	f := newSteeredCore(t, cfgPath)
	f.serve()
	ran := false
	b := fakeUI(t, true, &ran)
	b.detach = f.detach(nil)
	t.Chdir(t.TempDir())
	other, err := filepath.Abs("other.yaml")
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runRootSplit(t, uiRoot(b), "--config", "other.yaml")
	want := "Nearhost in the background serves " + cfgPath + ". Stop it with nearhost stop to use " + other + ", or open nearhost without --config.\n"
	if code != 1 || ran || stdout != "" || stderr != want {
		t.Errorf("exit %d, ran %v, stdout %q, stderr %q, want %q", code, ran, stdout, stderr, want)
	}

	// The file the core serves, named, is no other.
	if _, stderr, code := runRootSplit(t, uiRoot(b), "--config", cfgPath); code != 0 || !ran {
		t.Errorf("the core's own file: exit %d, ran %v, stderr %q", code, ran, stderr)
	}
}

// A serve flag given while a core runs changes nothing, and the interface's
// log says so first. A port the core already serves on is no change.
func TestServeFlagsIgnoredNote(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--web-port", "7000"}, "--web-port applies only when Nearhost starts; the running Nearhost keeps its settings."},
		{[]string{"--https-port", "9443", "--web-port", "7000", "--no-capture"}, "--https-port, --web-port and --no-capture apply only when Nearhost starts; the running Nearhost keeps its settings."},
		{[]string{"--web-port", "6327", "--https-port", "8443"}, ""},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			f := newSteeredCore(t, configHome(t))
			f.serve()
			var events <-chan engine.Event
			b := fakeUI(t, true, new(bool))
			b.detach = f.detach(nil)
			b.clientMade = func(c *clientBackend) {
				ch, unsubscribe := c.Events().Subscribe(16)
				t.Cleanup(unsubscribe)
				events = ch
			}
			if _, stderr, code := runRootSplit(t, uiRoot(b), tc.args...); code != 0 {
				t.Fatalf("exit %d, stderr %q", code, stderr)
			}
			var first string
			select {
			case ev := <-events:
				first = ev.Message
			default:
			}
			if first != tc.want {
				t.Errorf("the log's first line is %q, want %q", first, tc.want)
			}
		})
	}
}

// A core of another release is offered a restart before the interface
// opens: yes, or just enter, stops it and starts this one's; no opens the
// interface on the old core. A core that speaks another API is refused.
func TestVersionSkewAsks(t *testing.T) {
	prompt := "Nearhost in the background is 0.0.1; this is " + Version + ". Restart it now? [Y/n] "
	for _, tc := range []struct {
		name, in string
		restart  bool
	}{
		{"yes", "y\n", true},
		{"enter", "\n", true},
		{"no", "n\n", false},
		{"something else, then no", "maybe\nn\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfgPath := configHome(t)
			old := newSteeredCore(t, cfgPath)
			old.info.Version = "0.0.1"
			old.serve()
			var spawned [][]string
			ran := false
			b := fakeUI(t, true, &ran)
			b.detach = old.detach(func(args []string) {
				spawned = append(spawned, args)
				newSteeredCore(t, cfgPath).serve()
			})
			root := uiRoot(b)
			root.SetIn(strings.NewReader(tc.in))
			stdout, stderr, code := runRootSplit(t, root)
			if code != 0 || !ran {
				t.Fatalf("exit %d, ran %v, stderr %q", code, ran, stderr)
			}
			wantOut := prompt
			if tc.name == "something else, then no" {
				wantOut = prompt + prompt
			}
			if stdout != wantOut {
				t.Errorf("stdout %q, want %q", stdout, wantOut)
			}
			stopped := slices.Contains(old.asked(), "Stop")
			if stopped != tc.restart || (len(spawned) == 1) != tc.restart {
				t.Errorf("the old core stopped: %v; spawned %q; want a restart: %v", stopped, spawned, tc.restart)
			}
			if tc.restart && !slices.Equal(spawned[0][:3], []string{"daemon", "--started-by", "tui"}) {
				t.Errorf("spawned %q, want a core started by the interface", spawned[0])
			}
		})
	}

	t.Run("another API", func(t *testing.T) {
		old := newSteeredCore(t, configHome(t))
		old.info.API = control.APIVersion + 1
		old.serve()
		ran := false
		b := fakeUI(t, true, &ran)
		b.detach = old.detach(nil)
		stdout, stderr, code := runRootSplit(t, uiRoot(b))
		want := "Nearhost in the background speaks a different version of its API. Run nearhost stop, then nearhost.\n"
		if code != 1 || ran || stdout != "" || stderr != want {
			t.Errorf("exit %d, ran %v, stdout %q, stderr %q, want %q", code, ran, stdout, stderr, want)
		}
	})
}

// hostsRoot stands for sudo and the password box, for a fix of the hosts
// file made on the backend's own goroutines.
type hostsRoot struct {
	mu     sync.Mutex
	asked  []string
	askErr error // what the box answers
}

func (h *hostsRoot) root() asRoot {
	return asRoot{
		width: func() int { return 60 },
		sudo: rootBackend{
			needed:        func() bool { return true },
			takesPassword: func() bool { return true },
			cached:        func(context.Context) bool { return false },
			validate:      func(context.Context, string) error { return nil },
			forget:        func(context.Context) {},
		},
		ask: func(ctx context.Context, why string, check func(context.Context, string) error) error {
			h.mu.Lock()
			h.asked = append(h.asked, why)
			err := h.askErr
			h.mu.Unlock()
			if err != nil {
				return err
			}
			return check(ctx, "secret")
		},
		suspend: func(_ context.Context, fn func(io.Reader, io.Writer) error) error {
			return fn(strings.NewReader(""), io.Discard)
		},
	}
}

func (h *hostsRoot) times() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.asked)
}

// hostsHarness is a client of a core that left the project web waiting on
// the hosts file, whose block the client writes to a file of the test's.
type hostsHarness struct {
	*mirrorHarness
	root *hostsRoot
	path string
	b    *clientBackend
}

func newHostsHarness(t *testing.T, askErr error) *hostsHarness {
	t.Helper()
	h := &hostsHarness{mirrorHarness: newMirrorHarness(t), root: &hostsRoot{askErr: askErr}}
	h.path = filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(h.path, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	was := hosts.DefaultPath
	hosts.DefaultPath = h.path
	t.Cleanup(func() { hosts.DefaultPath = was })
	h.core.set(func(st *control.State) {
		st.Projects["web"] = engine.ProjectStatus{State: engine.StateStopped, Message: hostsNotFixed("web", errHostsNeedTerminal).Error()}
	})
	h.core.onHosts = func(st *control.State) {
		st.Projects["web"] = engine.ProjectStatus{State: engine.StateRunning}
	}
	local := &localOps{
		b: uiBackend{applier: func(context.Context, bool) hosts.Applier {
			return func(aliases []string) error { return hosts.Apply(h.path, aliases) }
		}},
		root: h.root.root(),
	}
	h.b = h.backend(t, h.state(t), local, nil)
	return h
}

// The interface fixes the hosts file for a project the core left waiting on
// it: it asks for the password once, writes the block, tells the core, and
// the project runs.
func TestHostsFixedFromTheInterface(t *testing.T) {
	h := newHostsHarness(t, nil)
	testutil.WaitUntil(t, 10*time.Second, func() bool { return h.b.Status("web").State == engine.StateRunning }, "web to run")
	if n := h.root.times(); n != 1 {
		t.Errorf("asked for the password %d times, want once", n)
	}
	if want := "Nearhost needs your password to add web.test to " + h.path + "."; h.root.asked[0] != want {
		t.Errorf("asked %q, want %q", h.root.asked[0], want)
	}
	data, err := os.ReadFile(h.path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "127.0.0.1 web.test") {
		t.Errorf("the hosts file:\n%s", data)
	}
	if got := h.core.asked(); !slices.Contains(got, "HostsChanged") {
		t.Errorf("the core was asked %q, not told the hosts file changed", got)
	}
}

// A cancelled password is not asked for again as the core's state changes:
// the project says why it waits. Starting the project asks again.
func TestHostsCancelledIsNotAskedAgainUntilAStart(t *testing.T) {
	h := newHostsHarness(t, tui.ErrPasswordCancelled)
	testutil.WaitUntil(t, 10*time.Second, func() bool {
		return strings.Contains(h.b.Status("web").Message, "you cancelled the password, so web.test was not added to "+h.path)
	}, "web to say the password was cancelled")
	if st := h.b.Status("web"); st.State != engine.StateStopped || !strings.Contains(st.Message, "hosts are not resolvable") {
		t.Errorf("web is %+v", st)
	}
	// The state is fetched again on the clock, and on each event.
	ev := engine.Event{Message: "something happened"}
	h.hub.Publish(control.Envelope{Kind: control.KindEvent, Event: &ev})
	time.Sleep(3 * refreshEvery)
	if n := h.root.times(); n != 1 {
		t.Fatalf("asked for the password %d times after a cancel, want once", n)
	}

	h.root.mu.Lock()
	h.root.askErr = nil
	h.root.mu.Unlock()
	if err := h.b.StartProject(context.Background(), "web"); err != nil {
		t.Errorf("StartProject = %v", err)
	}
	if n := h.root.times(); n != 2 {
		t.Errorf("asked for the password %d times, want once more for the start", n)
	}
	if st := h.b.Status("web"); st.State != engine.StateRunning {
		t.Errorf("web is %+v after the start", st)
	}
}

// A start the user asks for while the password is refused again says why,
// as the engine's own start says it.
func TestHostsStartSaysTheRefusal(t *testing.T) {
	h := newHostsHarness(t, tui.ErrPasswordCancelled)
	testutil.WaitUntil(t, 10*time.Second, func() bool { return h.root.times() == 1 }, "the first ask")
	err := h.b.StartProject(context.Background(), "web")
	want := `project "web"'s hosts are not resolvable: the hosts file was not updated (you cancelled the password, so web.test was not added to ` + h.path + "); start the project again to retry"
	if err == nil || err.Error() != want {
		t.Errorf("StartProject = %v, want %q", err, want)
	}
	if n := h.root.times(); n != 2 {
		t.Errorf("asked %d times, want twice", n)
	}
}

// coreGone runs bare nearhost on a fake core until the interface follows it,
// then has end end the core, and returns what the run printed.
func coreGone(t *testing.T, end func(f *steeredCore)) (stderr string, code int) {
	t.Helper()
	f := newSteeredCore(t, configHome(t))
	f.log("an earlier line", "the core's last words")
	f.serve()
	b := fakeUI(t, true, new(bool))
	b.detach = f.detach(nil)
	b.run = runProgram(func(*tea.Program) {
		testutil.WaitUntil(t, 10*time.Second, func() bool { return f.hub.Clients() == 1 }, "the interface to follow the core")
		end(f)
	})
	type result struct {
		stderr string
		code   int
	}
	done := make(chan result, 1)
	go func() {
		_, stderr, code := runRootSplit(t, uiRoot(b))
		done <- result{stderr, code}
	}()
	select {
	case r := <-done:
		return r.stderr, r.code
	case <-time.After(30 * time.Second):
		t.Fatal("the interface did not end when the core went")
	}
	return "", 0
}

// A core that goes while the interface runs ends it, saying so with the end
// of the core's log, where the core said why.
func TestCoreGoneEndsTheInterface(t *testing.T) {
	stderr, code := coreGone(t, func(f *steeredCore) { f.close() })
	logPath, err := config.LogPath()
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 || !strings.HasPrefix(stderr, "Nearhost in the background stopped. The end of "+logPath+":\n") ||
		!strings.HasSuffix(stderr, " the core's last words\n") || !strings.Contains(stderr, "an earlier line") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
}

// A core stopped through `nearhost stop` ends the interface too, with no
// log: it was asked to stop.
func TestCoreStoppedEndsTheInterfaceWithNoLog(t *testing.T) {
	stderr, code := coreGone(t, func(f *steeredCore) {
		var out syncBuffer
		if err := executeInto(context.Background(), &out, newStopCmd(), "stop"); err != nil || out.String() != "Stopped.\n" {
			t.Errorf("nearhost stop: %v, printed %q", err, out.String())
		}
	})
	if want := "Nearhost in the background stopped.\n"; code != 1 || stderr != want {
		t.Errorf("exit %d, stderr %q, want %q", code, stderr, want)
	}
}

// Once the interface has quit, a start still under way asks for nothing.
func TestHostsNotAskedOnceClosed(t *testing.T) {
	h := newHostsHarness(t, tui.ErrPasswordCancelled)
	testutil.WaitUntil(t, 10*time.Second, func() bool { return h.root.times() == 1 }, "the first ask")
	h.b.Close()
	if err := h.b.StartProject(context.Background(), "web"); err == nil {
		t.Error("a start after Close fixed the hosts file")
	}
	if n := h.root.times(); n != 1 {
		t.Errorf("asked %d times, want no more once closed", n)
	}
}
