package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/control"
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
		spawn: func(exe string, args []string) (func() error, error) {
			f.mu.Lock()
			f.spawned = append(f.spawned, append([]string{exe}, args...))
			f.mu.Unlock()
			return child(), nil
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
	if code != 0 || stderr != "" {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	if want := "Nearhost is already running in a terminal (pid 98). To run it in the background instead, stop it with `nearhost stop` and run `nearhost -d` again.\n"; stdout != want {
		t.Errorf("stdout %q, want %q", stdout, want)
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
			want := "Nearhost stopped while starting (exit status 1), before it wrote to " + logPath + ".\n"
			if wrote != "" {
				want = "Nearhost stopped while starting. The end of " + logPath + ":\nthis run's line\n"
			}
			if code != 1 || stderr != want {
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
	want := "Nearhost stopped while starting. The end of " + logPath + ":\n" + strings.Join(lines[5:], "\n") + "\n"
	if asked != 20 || stderr != want {
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
	if want := "Nearhost stopped while starting (exit status 2), before it wrote to " + logPath + ".\n"; code != 1 || stderr != want {
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
	if want := "Nearhost is still starting. See " + logPath + " for how it is going.\n"; stderr != want {
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
