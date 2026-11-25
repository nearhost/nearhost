package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/hosts"
	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/testutil"
)

// boomMsg makes panicModel panic in its Update.
type boomMsg struct{}

// panicModel is the interface, panicking on a boomMsg as a bug would.
type panicModel struct{ tea.Model }

func (p panicModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(boomMsg); ok {
		panic("boom in Update")
	}
	m, cmd := p.Model.Update(msg)
	return panicModel{m}, cmd
}

// crashRun runs bare `nearhost` against a steered core that setup prepares,
// in a real program, and calls trigger once the interface follows the core.
// It returns what the run printed on standard error and its exit code, and
// the folder crash files go to.
func crashRun(t *testing.T, setup func(f *steeredCore, b *uiBackend), trigger func(p *tea.Program)) (stderr string, code int, dir string) {
	t.Helper()
	dir = t.TempDir()
	was := report.CrashDir
	report.CrashDir = dir
	t.Cleanup(func() { report.CrashDir = was })
	f := newSteeredCore(t, configHome(t))
	b := fakeUI(t, true, new(bool))
	if setup != nil {
		setup(f, &b)
	}
	f.serve()
	b.detach = f.detach(nil)
	b.run = func(m tea.Model, attach func(*tea.Program)) error {
		p := uiProgram(m, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithWindowSize(100, 30))
		attach(p)
		if trigger != nil {
			ended := make(chan struct{})
			defer close(ended)
			go func() {
				if !testutil.Eventually(10*time.Second, func() bool { return f.hub.Clients() == 1 }) {
					select {
					case <-ended:
					default:
						t.Error("timed out waiting for the interface to follow the core")
					}
					return
				}
				trigger(p)
			}()
		}
		_, err := p.Run()
		return err
	}
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
		return r.stderr, r.code, dir
	case <-time.After(30 * time.Second):
		t.Fatal("the interface did not end after the panic")
	}
	return "", 0, dir
}

// wantCrashReport fails unless the run ended with exit 1 and the crash
// message alone on standard error, naming a crash file in dir that holds
// the panic and its stack.
func wantCrashReport(t *testing.T, stderr string, code int, dir, panicked string) {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "crash-*.log"))
	if len(files) != 1 {
		t.Fatalf("crash files %v, want one in %s", files, dir)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), panicked) || !strings.Contains(string(data), "goroutine ") {
		t.Errorf("the crash file lacks the panic or its stack:\n%s", data)
	}
	want := "Error: Nearhost stopped unexpectedly. The details are in " + files[0] + ". " +
		"Report it at https://github.com/nearhost/nearhost/issues with that file attached."
	if code != 1 || flat(stderr) != want {
		t.Errorf("exit %d, stderr %q, want exit 1 and %q", code, stderr, want)
	}
	for _, not := range []string{"goroutine ", "Caught panic", panicked} {
		if strings.Contains(stderr, not) {
			t.Errorf("standard error shows %q: %q", not, stderr)
		}
	}
}

// A panic in the interface's Update ends the program, which restores the
// terminal, and the run says Nearhost stopped unexpectedly, with the crash
// file that holds the stack, and exits 1.
func TestPanicInUpdateRestoresAndReports(t *testing.T) {
	stderr, code, dir := crashRun(t, func(_ *steeredCore, b *uiBackend) {
		b.wrapModel = func(m tea.Model) tea.Model { return panicModel{m} }
	}, func(p *tea.Program) { p.Send(boomMsg{}) })
	wantCrashReport(t, stderr, code, dir, "boom in Update")
}

// A panic in a goroutine the backend started itself, the hosts fixer here,
// while the interface is open ends it the same way: the terminal restored,
// the crash message, no stack on screen.
func TestPanicInBackendGoroutineReports(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(path, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	was := hosts.DefaultPath
	hosts.DefaultPath = path
	t.Cleanup(func() { hosts.DefaultPath = was })
	stderr, code, dir := crashRun(t, func(f *steeredCore, b *uiBackend) {
		f.set(func(st *control.State) {
			st.Projects["web"] = hostsWaitStatus()
		})
		b.root.needed = func() bool { panic("boom in the hosts fixer") }
	}, nil)
	wantCrashReport(t, stderr, code, dir, "boom in the hosts fixer")
}
