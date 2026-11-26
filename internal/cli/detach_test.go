package cli

import (
	"context"
	"errors"
	"fmt"
	"github.com/nearhost/nearhost/internal/report"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/service"
	"github.com/nearhost/nearhost/internal/testutil"
)

// infoCore is a control backend that answers with info.
type infoCore struct {
	// The actions are the core's, which these tests never take: nil.
	*coreOps
	info control.Info
}

func (c infoCore) Info() control.Info               { return c.info }
func (c infoCore) Status() control.Status           { return control.Status{Info: c.info} }
func (c infoCore) State() control.State             { return control.State{Info: c.info} }
func (c infoCore) Stop()                            {}
func (c infoCore) HostsChanged(ctx context.Context) {}

// fakeDetach is a detachBackend whose child is a control server the test
// starts, or nothing.
type fakeDetach struct {
	t      *testing.T
	runDir string

	mu      sync.Mutex
	spawned [][]string
	srv     *control.Server
}

func newFakeDetach(t *testing.T) *fakeDetach {
	return &fakeDetach{t: t, runDir: testutil.TempDir(t, "nhd")}
}

// answer starts a core with info on the fake's socket.
func (f *fakeDetach) answer(info control.Info) {
	srv, err := control.Listen(control.SocketPath(f.runDir), infoCore{info: info}, control.NewHub())
	if err != nil {
		// Not Fatal: answer may run on a goroutine of the fake child's.
		f.t.Error(err)
		return
	}
	f.mu.Lock()
	f.srv = srv
	f.mu.Unlock()
	f.t.Cleanup(func() { srv.Close() })
}

// backend returns the fake as a detachBackend whose spawn records the
// command and calls child, which returns the child's wait.
func (f *fakeDetach) backend(child func() func() error) detachBackend {
	return detachBackend{
		spawn: func(exe string, args []string) (int, func() error, error) {
			f.mu.Lock()
			f.spawned = append(f.spawned, append([]string{exe}, args...))
			f.mu.Unlock()
			return 0, child(), nil
		},
		dial:    func() (*control.Client, error) { return control.Dial(f.runDir) },
		logSize: func() int64 { return 0 },
		logTail: func(from int64, n int) []string { return nil },
		exe:     func() string { return "/bin/nearhost" },
	}
}

func (f *fakeDetach) calls() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.spawned
}

// running is a child that never exits.
func running() func() error {
	block := make(chan struct{})
	return func() error { <-block; return nil }
}

func TestDetachStartsTheDaemon(t *testing.T) {
	configHome(t)
	f := newFakeDetach(t)
	b := f.backend(func() func() error {
		// The core answers a little after it starts.
		go func() {
			time.Sleep(250 * time.Millisecond)
			f.answer(control.Info{API: 1, PID: 4242, StartedBy: control.StartedDetached})
		}()
		return running()
	})
	b.timeout = 10 * time.Second
	ran := false
	ui := fakeUI(t, true, &ran)
	ui.detach = b
	stdout, stderr, code := runRootSplit(t, uiRoot(ui), "-d", "--web-port", "7000", "--no-hosts")
	if code != 0 || ran || stderr != "" {
		t.Fatalf("code %d, ran %v, stderr %q", code, ran, stderr)
	}
	if want := "Nearhost is running in the background (pid 4242). Stop it with nearhost stop.\n"; stdout != want {
		t.Errorf("stdout %q, want %q", stdout, want)
	}
	want := [][]string{{"/bin/nearhost", "daemon", "--started-by", "detached", "--web-port", "7000", "--no-hosts"}}
	if got := f.calls(); !reflect.DeepEqual(got, want) {
		t.Errorf("spawned %q, want %q", got, want)
	}
}

func TestDetachPassesTheConfigAndOnlyTheFlagsGiven(t *testing.T) {
	configHome(t)
	f := newFakeDetach(t)
	b := f.backend(func() func() error {
		f.answer(control.Info{API: 1, PID: 7})
		return running()
	})
	ui := fakeUI(t, true, new(bool))
	ui.detach = b
	dir := t.TempDir()
	t.Chdir(dir)
	if _, stderr, code := runRootSplit(t, uiRoot(ui), "--detach", "--config", "my.yaml", "--https-port", "8443", "--no-capture", "--no-hosts=false"); code != 0 {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	// A relative --config is made absolute: the core does not run in the
	// directory it was started from.
	abs, _ := filepath.Abs("my.yaml")
	want := [][]string{{"/bin/nearhost", "daemon", "--started-by", "detached", "--config", abs, "--https-port", "8443", "--no-hosts=false", "--no-capture"}}
	if got := f.calls(); !reflect.DeepEqual(got, want) {
		t.Errorf("spawned %q, want %q", got, want)
	}
}

func TestDetachAlreadyRunning(t *testing.T) {
	configHome(t)
	f := newFakeDetach(t)
	f.answer(control.Info{API: 1, PID: 99, StartedBy: control.StartedService})
	b := f.backend(func() func() error {
		t.Error("spawned with a core running")
		return running()
	})
	ui := fakeUI(t, true, new(bool))
	ui.detach = b
	stdout, stderr, code := runRootSplit(t, uiRoot(ui), "-d")
	if code != 0 || stderr != "" {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	if want := "Nearhost is already running in the background (pid 99, started by service).\n"; stdout != want {
		t.Errorf("stdout %q, want %q", stdout, want)
	}
}

// A core bare nearhost started runs in the background, outside the
// interface's terminal, and stays when the interface leaves it.
func TestDetachAlreadyRunningStartedByTheInterface(t *testing.T) {
	configHome(t)
	f := newFakeDetach(t)
	f.answer(control.Info{API: 1, PID: 97, StartedBy: control.StartedTUI})
	b := f.backend(func() func() error {
		t.Error("spawned with a core running")
		return running()
	})
	ui := fakeUI(t, true, new(bool))
	ui.detach = b
	stdout, stderr, code := runRootSplit(t, uiRoot(ui), "-d")
	if code != 0 || stderr != "" {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	if want := "Nearhost is already running in the background (pid 97, started by tui).\n"; stdout != want {
		t.Errorf("stdout %q, want %q", stdout, want)
	}
}

// A core in a terminal is not in the background, and -d says how to put it
// there.
func TestDetachAlreadyRunningInATerminal(t *testing.T) {
	configHome(t)
	f := newFakeDetach(t)
	f.answer(control.Info{API: 1, PID: 98, StartedBy: control.StartedForeground})
	b := f.backend(func() func() error {
		t.Error("spawned with a core running")
		return running()
	})
	ui := fakeUI(t, true, new(bool))
	ui.detach = b
	stdout, stderr, code := runRootSplit(t, uiRoot(ui), "-d")
	if code != 0 {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	if want := "Nearhost is already running in a terminal (pid 98).\n"; stdout != want {
		t.Errorf("stdout %q, want %q", stdout, want)
	}
	// The way out is a note's steps, each command on a line of its own.
	if want := "Note: Nearhost stays in that terminal.\n\nTo run it in the background instead, stop it:\n\n  nearhost stop\n\nThen start it again:\n\n  nearhost -d\n"; stderr != want {
		t.Errorf("stderr %q, want %q", stderr, want)
	}
}

// What the log held before -d started the core is another run's: a core
// that stops while starting shows only what it wrote itself, and says so
// plainly when that is nothing.
func TestDetachChildFailsShowsOnlyItsOwnLog(t *testing.T) {
	cfgPath := configHome(t)
	logPath := filepath.Join(filepath.Dir(cfgPath), "core.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("an old run's line\nits last line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, wrote := range map[string]string{"wrote": "this run's line\n", "wrote nothing": ""} {
		t.Run(name, func(t *testing.T) {
			f := newFakeDetach(t)
			b := f.backend(func() func() error {
				if wrote != "" {
					lf, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0)
					if err != nil {
						t.Fatal(err)
					}
					lf.WriteString(wrote)
					lf.Close()
				}
				return func() error { return errors.New("exit status 1") }
			})
			sys := systemDetach()
			b.logSize, b.logTail = sys.logSize, sys.logTail
			b.timeout = 10 * time.Second
			ui := fakeUI(t, true, new(bool))
			ui.detach = b
			_, stderr, code := runRootSplit(t, uiRoot(ui), "-d")
			want := "Error: Nearhost stopped while starting.\n\nIt stopped (exit status 1) before it wrote to " + logPath + "." + "\n\nRun `nearhost doctor` to check this and the rest of what Nearhost needs.\n"
			if wrote != "" {
				want = "Error: Nearhost stopped while starting.\n\nThe end of " + logPath + ":\nthis run's line" + "\n\nRun `nearhost doctor` to check this and the rest of what Nearhost needs.\n"
			}
			if code != 1 || flat(stderr) != flat(want) {
				t.Errorf("code %d, stderr %q, want %q", code, stderr, want)
			}
		})
	}
}

func TestDetachChildFails(t *testing.T) {
	cfgPath := configHome(t)
	logPath := filepath.Join(filepath.Dir(cfgPath), "core.log")
	f := newFakeDetach(t)
	b := f.backend(func() func() error {
		return func() error { return errors.New("exit status 1") }
	})
	var lines []string
	for i := 1; i <= 25; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	asked := 0
	b.logTail = func(from int64, n int) []string {
		asked = n
		return lines[len(lines)-n:]
	}
	b.timeout = 10 * time.Second
	ui := fakeUI(t, true, new(bool))
	ui.detach = b
	stdout, stderr, code := runRootSplit(t, uiRoot(ui), "-d")
	if code != 1 || stdout != "" {
		t.Fatalf("code %d, stdout %q", code, stdout)
	}
	want := "Error: Nearhost stopped while starting.\n\nThe end of " + logPath + ":\n" + strings.Join(lines[5:], "\n") + "\n\nRun `nearhost doctor` to check this and the rest of what Nearhost needs.\n"
	if asked != 20 || flat(stderr) != flat(want) || !strings.Contains(stderr, "\n  line 6\n  line 7\n") {
		t.Errorf("asked for %d lines; stderr %q, want %q", asked, stderr, want)
	}
}

func TestDetachChildFailsWithNothingLogged(t *testing.T) {
	cfgPath := configHome(t)
	logPath := filepath.Join(filepath.Dir(cfgPath), "core.log")
	f := newFakeDetach(t)
	b := f.backend(func() func() error {
		return func() error { return errors.New("exit status 2") }
	})
	ui := fakeUI(t, true, new(bool))
	ui.detach = b
	_, stderr, code := runRootSplit(t, uiRoot(ui), "-d")
	if want := "Error: Nearhost stopped while starting.\n\nIt stopped (exit status 2) before it wrote to " + logPath + "." + "\n\nRun `nearhost doctor` to check this and the rest of what Nearhost needs.\n"; code != 1 || flat(stderr) != flat(want) {
		t.Errorf("code %d, stderr %q, want %q", code, stderr, want)
	}
}

// A child that answers and then stops at once was running: what it said
// counts, not that it has gone.
func TestDetachChildThatAnswersThenExits(t *testing.T) {
	configHome(t)
	f := newFakeDetach(t)
	b := f.backend(func() func() error {
		f.answer(control.Info{API: 1, PID: 5})
		return func() error { return nil }
	})
	ui := fakeUI(t, true, new(bool))
	ui.detach = b
	stdout, _, code := runRootSplit(t, uiRoot(ui), "-d")
	if code != 0 || !strings.Contains(stdout, "(pid 5)") {
		t.Errorf("code %d, stdout %q", code, stdout)
	}
}

func TestDetachTimesOut(t *testing.T) {
	cfgPath := configHome(t)
	logPath := filepath.Join(filepath.Dir(cfgPath), "core.log")
	f := newFakeDetach(t)
	b := f.backend(running)
	b.timeout = 300 * time.Millisecond
	ui := fakeUI(t, true, new(bool))
	ui.detach = b
	start := time.Now()
	stdout, stderr, code := runRootSplit(t, uiRoot(ui), "-d")
	if code != 1 || stdout != "" {
		t.Fatalf("code %d, stdout %q", code, stdout)
	}
	if want := "Error: Nearhost is still starting.\n\nSee how it is going in its log:\n\n  " + logPath + "\n"; stderr != want {
		t.Errorf("stderr %q, want %q", stderr, want)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %s", d)
	}
}

func TestDetachNeedsNoTerminal(t *testing.T) {
	configHome(t)
	f := newFakeDetach(t)
	b := f.backend(func() func() error {
		f.answer(control.Info{API: 1, PID: 3})
		return running()
	})
	ran := false
	ui := fakeUI(t, false, &ran)
	ui.detach = b
	stdout, stderr, code := runRootSplit(t, uiRoot(ui), "-d")
	if code != 0 || ran || strings.Contains(stderr, "needs a terminal") || !strings.Contains(stdout, "running in the background") {
		t.Errorf("code %d, ran %v, stdout %q, stderr %q", code, ran, stdout, stderr)
	}
}

func TestDetachRefusesBadFlags(t *testing.T) {
	configHome(t)
	f := newFakeDetach(t)
	b := f.backend(func() func() error {
		t.Error("spawned with bad flags")
		return running()
	})
	ui := fakeUI(t, false, new(bool))
	ui.detach = b
	if _, _, code := runRootSplit(t, uiRoot(ui), "-d", "--https-port", "80", "--http-port", "80"); code != 1 {
		t.Errorf("code %d", code)
	}
}

func TestTailLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "core.log")
	if got := tailLines(path, 0, 20); len(got) != 0 {
		t.Errorf("missing log: %q", got)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := tailLines(path, 0, 20); len(got) != 0 {
		t.Errorf("empty log: %q", got)
	}
	if err := os.WriteFile(path, []byte("a\r\nb\nc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := tailLines(path, 0, 2); !reflect.DeepEqual(got, []string{"b", "c"}) {
		t.Errorf("short log: %q", got)
	}
	// Only what was written from an offset on.
	if got := tailLines(path, 3, 20); !reflect.DeepEqual(got, []string{"b", "c"}) {
		t.Errorf("from 3: %q", got)
	}
	if got := tailLines(path, 6, 20); len(got) != 0 {
		t.Errorf("from the end: %q", got)
	}
	// A log shorter than the offset was set aside and started afresh.
	if got := tailLines(path, 100, 20); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("from past the end: %q", got)
	}
	// A long log is read from its end, without the line cut in half.
	var b strings.Builder
	for i := 0; i < 20000; i++ {
		fmt.Fprintf(&b, "line %d of the log\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	got := tailLines(path, 0, 3)
	if want := []string{"line 19997 of the log", "line 19998 of the log", "line 19999 of the log"}; !reflect.DeepEqual(got, want) {
		t.Errorf("long log: %q", got)
	}
}

// Two nearhost started at once both start a core; one core takes the lock
// and the other stops saying another is running. The nearhost whose core
// stopped waits for the other's to answer, and says it was already running.
func TestDetachLosesTheRaceToAnotherCore(t *testing.T) {
	configHome(t)
	f := newFakeDetach(t)
	b := f.backend(func() func() error {
		go func() {
			time.Sleep(300 * time.Millisecond)
			f.answer(control.Info{API: 1, PID: 77, StartedBy: control.StartedTUI})
		}()
		return func() error { return errors.New("exit status 1") }
	})
	b.held = func() bool { return true }
	b.timeout = 10 * time.Second
	ui := fakeUI(t, true, new(bool))
	ui.detach = b
	stdout, stderr, code := runRootSplit(t, uiRoot(ui), "-d")
	if code != 0 || stderr != "" {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	if want := "Nearhost is already running in the background (pid 77, started by tui).\n"; stdout != want {
		t.Errorf("stdout %q, want %q", stdout, want)
	}
}

// The core that answers first may be another nearhost's: only one whose pid
// is the child's was started here.
func TestDetachAnotherCoreAnswersFirst(t *testing.T) {
	configHome(t)
	f := newFakeDetach(t)
	b := f.backend(func() func() error {
		f.answer(control.Info{API: 1, PID: 77, StartedBy: control.StartedTUI})
		return running()
	})
	spawn := b.spawn
	b.spawn = func(exe string, args []string) (int, func() error, error) {
		_, wait, err := spawn(exe, args)
		return 1234, wait, err
	}
	ui := fakeUI(t, true, new(bool))
	ui.detach = b
	stdout, _, code := runRootSplit(t, uiRoot(ui), "-d")
	if want := "Nearhost is already running in the background (pid 77, started by tui).\n"; code != 0 || stdout != want {
		t.Errorf("code %d, stdout %q, want %q", code, stdout, want)
	}
}

// fakeService is an installed service that runs exe, whose Start calls start.
// Active is what the test sets. installed, when set, is what Installed
// returns instead.
type fakeService struct {
	exe       string
	logPath   string
	start     func() error
	installed func(ctx context.Context) error
	// startCtx, when set, is Start in place of start.
	startCtx func(ctx context.Context) error

	mu      sync.Mutex
	active  bool
	starts  int
	lookups int
}

// lookup is the fake as a detachBackend's service, looked up as the real
// one is.
func (s *fakeService) lookup() func(context.Context) (service.Manager, service.Definition) {
	return func(ctx context.Context) (service.Manager, service.Definition) {
		s.mu.Lock()
		s.lookups++
		s.mu.Unlock()
		return installedService(ctx, s)
	}
}

func (s *fakeService) Installed(ctx context.Context) (service.Definition, bool, error) {
	if s.installed != nil {
		if err := s.installed(ctx); err != nil {
			return service.Definition{}, false, err
		}
	}
	return service.Definition{Exe: s.exe, LogPath: s.logPath}, true, nil
}

func (s *fakeService) Install(context.Context, service.Definition) (bool, error) {
	return false, errors.New("no service is installed in this test")
}

func (s *fakeService) Uninstall(context.Context) error {
	return errors.New("no service is removed in this test")
}

func (s *fakeService) Start(ctx context.Context) error {
	s.mu.Lock()
	s.starts++
	start, startCtx := s.start, s.startCtx
	s.mu.Unlock()
	if startCtx != nil {
		return startCtx(ctx)
	}
	if start == nil {
		return nil
	}
	return start()
}

func (s *fakeService) Active(context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active, nil
}

func (s *fakeService) ManagerEnv(context.Context) map[string]string { return nil }
func (s *fakeService) Graphical(context.Context) bool               { return false }

// counts returns how often the service was started and looked up.
func (s *fakeService) counts() (starts, lookups int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.starts, s.lookups
}

// With a service installed, -d starts the core through it, and says so.
func TestDetachThroughService(t *testing.T) {
	configHome(t)
	f := newFakeDetach(t)
	b := f.backend(func() func() error {
		t.Error("spawned with a service installed")
		return running()
	})
	svc := &fakeService{exe: "/bin/nearhost", active: true, start: func() error {
		f.answer(control.Info{API: 1, PID: 4343, StartedBy: control.StartedService})
		return nil
	}}
	b.service = svc.lookup()
	b.timeout = 10 * time.Second
	ui := fakeUI(t, true, new(bool))
	ui.detach = b
	stdout, stderr, code := runRootSplit(t, uiRoot(ui), "-d")
	if code != 0 || stderr != "" {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	if want := "Nearhost is running in the background as a service (pid 4343). Stop it with nearhost stop.\n"; stdout != want {
		t.Errorf("stdout %q, want %q", stdout, want)
	}
	if starts, _ := svc.counts(); starts != 1 {
		t.Errorf("the service was started %d times, want once", starts)
	}

	// The core now runs: -d again leaves it be, and does not look for the
	// service.
	stdout, _, code = runRootSplit(t, uiRoot(ui), "-d")
	if want := "Nearhost is already running in the background (pid 4343, started by service).\n"; code != 0 || stdout != want {
		t.Errorf("code %d, stdout %q, want %q", code, stdout, want)
	}
	if starts, lookups := svc.counts(); starts != 1 || lookups != 1 {
		t.Errorf("the service was started %d and looked up %d times, want once each", starts, lookups)
	}
}

// The service's log folder may have gone since it was installed, and the
// manager then fails the unit before it runs: -d creates it before starting
// the service, the installed service's own folder when it says one.
func TestDetachThroughServiceCreatesLogFolder(t *testing.T) {
	for _, own := range []bool{false, true} {
		t.Run(fmt.Sprintf("own=%v", own), func(t *testing.T) {
			cfg := filepath.Join(testutil.TempDir(t, "nh"), "missing")
			t.Setenv("XDG_CONFIG_HOME", cfg)
			want := filepath.Join(cfg, "nearhost")
			f := newFakeDetach(t)
			b := f.backend(func() func() error {
				t.Error("spawned with a service installed")
				return running()
			})
			svc := &fakeService{exe: "/bin/nearhost", active: true}
			if own {
				want = filepath.Join(testutil.TempDir(t, "nh"), "gone", "logs")
				svc.logPath = filepath.Join(want, "core.log")
			}
			svc.start = func() error {
				if _, err := os.Stat(want); err != nil {
					t.Errorf("log folder at start: %v", err)
				}
				f.answer(control.Info{API: 1, PID: 4343, StartedBy: control.StartedService})
				return nil
			}
			b.service = svc.lookup()
			b.timeout = 10 * time.Second
			ui := fakeUI(t, true, new(bool))
			ui.detach = b
			if _, stderr, code := runRootSplit(t, uiRoot(ui), "-d"); code != 0 {
				t.Fatalf("code %d, stderr %q", code, stderr)
			}
			if starts, _ := svc.counts(); starts != 1 {
				t.Errorf("the service was started %d times, want once", starts)
			}
		})
	}
}

// A serve flag is the user's own setting, which the service does not have:
// the core is started directly, with a note.
func TestDetachWithFlagsSpawns(t *testing.T) {
	configHome(t)
	f := newFakeDetach(t)
	b := f.backend(func() func() error {
		f.answer(control.Info{API: 1, PID: 7, StartedBy: control.StartedDetached})
		return running()
	})
	svc := &fakeService{exe: "/bin/nearhost", start: func() error {
		t.Error("started the service with flags given")
		return nil
	}}
	b.service = svc.lookup()
	ui := fakeUI(t, true, new(bool))
	ui.detach = b
	stdout, stderr, code := runRootSplit(t, uiRoot(ui), "-d", "--web-port", "7000")
	if code != 0 {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	if want := "Note: A service is installed, but it runs with its own settings, so Nearhost was started directly with yours.\n"; flat(stderr) != flat(want) {
		t.Errorf("stderr %q, want %q", stderr, want)
	}
	if want := "Nearhost is running in the background (pid 7). Stop it with nearhost stop.\n"; stdout != want {
		t.Errorf("stdout %q, want %q", stdout, want)
	}
	want := [][]string{{"/bin/nearhost", "daemon", "--started-by", "detached", "--web-port", "7000"}}
	if got := f.calls(); !reflect.DeepEqual(got, want) {
		t.Errorf("spawned %q, want %q", got, want)
	}
}

// A service that cannot be started is no reason not to start the core: it
// is spawned as with no service, and the reason is said.
func TestServiceStartFailsFallsBack(t *testing.T) {
	configHome(t)
	f := newFakeDetach(t)
	b := f.backend(func() func() error {
		f.answer(control.Info{API: 1, PID: 8, StartedBy: control.StartedDetached})
		return running()
	})
	svc := &fakeService{exe: "/bin/nearhost", start: func() error {
		return errors.New("launchctl kickstart: exit status 113\nCould not find service")
	}}
	b.service = svc.lookup()
	ui := fakeUI(t, true, new(bool))
	ui.detach = b
	stdout, stderr, code := runRootSplit(t, uiRoot(ui), "-d")
	if code != 0 {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	if want := "Warning: Starting Nearhost through its service failed (launchctl kickstart: exit status 113), so it was started directly.\n"; flat(stderr) != flat(want) {
		t.Errorf("stderr %q, want %q", stderr, want)
	}
	if want := "Nearhost is running in the background (pid 8). Stop it with nearhost stop.\n"; stdout != want {
		t.Errorf("stdout %q, want %q", stdout, want)
	}
	want := [][]string{{"/bin/nearhost", "daemon", "--started-by", "detached"}}
	if got := f.calls(); !reflect.DeepEqual(got, want) {
		t.Errorf("spawned %q, want %q", got, want)
	}
}

// A service whose core stops while starting is said as a spawned core's is,
// with the end of the log; it is given a moment to become active first.
func TestServiceStartStops(t *testing.T) {
	cfgPath := configHome(t)
	logPath := filepath.Join(filepath.Dir(cfgPath), "core.log")
	f := newFakeDetach(t)
	b := f.backend(func() func() error {
		t.Error("spawned with a service installed")
		return running()
	})
	svc := &fakeService{exe: "/bin/nearhost"}
	b.service = svc.lookup()
	b.logTail = func(int64, int) []string { return []string{"the service's last line"} }
	b.timeout = 10 * time.Second
	ui := fakeUI(t, true, new(bool))
	ui.detach = b
	start := time.Now()
	stdout, stderr, code := runRootSplit(t, uiRoot(ui), "-d")
	took := time.Since(start)
	if code != 1 || stdout != "" {
		t.Fatalf("code %d, stdout %q", code, stdout)
	}
	if want := "Error: Nearhost stopped while starting.\n\nThe end of " + logPath + ":\nthe service's last line" + "\n\nRun `nearhost doctor` to check this and the rest of what Nearhost needs.\n"; flat(stderr) != flat(want) {
		t.Errorf("stderr %q, want %q", stderr, want)
	}
	if took < serviceGrace || took > 8*time.Second {
		t.Errorf("took %s, want the grace of %s and a little more", took, serviceGrace)
	}
}

// A core that stopped while starting is core.stopped-while-starting, with
// the end of its log as why, and suggests doctor unless the log already
// names it.
func TestChildStoppedIsAMessage(t *testing.T) {
	err := childStopped("/logs/core.log", []string{"line one", "line two"}, errors.New("exit status 1"))
	m, ok := report.As(err)
	if !ok || m.Code != report.CoreStoppedWhileStarting || m.Title != "Nearhost stopped while starting." ||
		m.Why != "The end of /logs/core.log:" || !slices.Equal(m.Verbatim, []string{"line one", "line two"}) || !m.Doctor || m.Data["log"] != "/logs/core.log" {
		t.Fatalf("message %+v", m)
	}
	m, _ = report.As(childStopped("/logs/core.log", []string{"  Run `nearhost doctor` to check this and the rest of what Nearhost needs."}, nil))
	if m.Doctor {
		t.Errorf("doctor suggested twice: %+v", m)
	}
	m, _ = report.As(childStopped("/logs/core.log", nil, errors.New("exit status 2")))
	if m.Why != "It stopped (exit status 2) before it wrote to /logs/core.log." || !m.Doctor {
		t.Errorf("with nothing logged: %+v", m)
	}
}

// A command the core logged comes out of `nearhost -d`'s error whole, as it
// was in the log, so it can be copied: a detached core that cannot bind
// logs the setcap command on a stamped line of a hundred columns.
func TestChildStoppedKeepsALoggedCommandWhole(t *testing.T) {
	fix := "2026-11-25T08:49:24Z   Fix: sudo setcap cap_net_bind_service=+ep '/home/someone/.local/bin/nearhost'"
	if len(fix) < 100 {
		t.Fatalf("the line is %d columns", len(fix))
	}
	tail := []string{
		"2026-11-25T08:49:24Z error [port.needs-permission] HTTPS port 443 needs permission to bind.",
		fix,
		"2026-11-25T08:49:24Z   Run `nearhost doctor` to check this and the rest of what Nearhost needs.",
	}
	got := rendered(childStopped("/home/someone/.config/nearhost/core.log", tail, errors.New("exit status 1")))
	if !strings.Contains(got, "\n  "+fix+"\n") {
		t.Errorf("the command is not whole:\n%s", got)
	}
	// The log's own doctor line is enough.
	if strings.Count(got, "nearhost doctor") != 1 {
		t.Errorf("doctor suggested twice:\n%s", got)
	}
}
