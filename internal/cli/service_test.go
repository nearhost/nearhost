package cli

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/service"
)

// recordingService is a service manager that records what it is asked.
type recordingService struct {
	mu        sync.Mutex
	calls     []string
	installed bool
	def       service.Definition
	changed   bool
	err       error // what Install returns, besides changed
	instErr   error // what Installed returns
	got       service.Definition
	// onStart runs on Start, as the service manager starting the core.
	onStart func()
	// uninstallErr is what Uninstall returns.
	uninstallErr error
}

func (r *recordingService) note(s string) {
	r.mu.Lock()
	r.calls = append(r.calls, s)
	r.mu.Unlock()
}

func (r *recordingService) log() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.calls, ",")
}

func (r *recordingService) Installed(context.Context) (service.Definition, bool, error) {
	return r.def, r.installed, r.instErr
}

func (r *recordingService) Install(_ context.Context, d service.Definition) (bool, error) {
	r.note("install")
	r.mu.Lock()
	r.got = d
	r.mu.Unlock()
	return r.changed, r.err
}

func (r *recordingService) Uninstall(context.Context) error {
	r.note("uninstall")
	return r.uninstallErr
}

func (r *recordingService) Start(context.Context) error {
	r.note("start")
	if r.onStart != nil {
		r.onStart()
	}
	return nil
}

func (r *recordingService) Active(context.Context) (bool, error) { return true, nil }
func (r *recordingService) ManagerEnv(context.Context) map[string]string {
	return map[string]string{"SSH_AUTH_SOCK": "/run/agent"}
}
func (r *recordingService) Graphical(context.Context) bool { return false }

// serviceFixture is a serviceBackend over a fake service manager and a fake
// core's socket.
type serviceFixture struct {
	f   *fakeDetach
	m   *recordingService
	b   serviceBackend
	log *[]string
}

func newServiceFixture(t *testing.T) *serviceFixture {
	configHome(t)
	f := newFakeDetach(t)
	m := &recordingService{installed: true, def: service.Definition{Exe: "/usr/bin/nearhost"}, changed: true}
	var events []string
	var mu sync.Mutex
	d := f.backend(func() func() error { t.Error("spawned a core"); return running() })
	d.timeout = 10 * time.Second
	d.gone = func(context.Context) error {
		mu.Lock()
		events = append(events, "gone")
		mu.Unlock()
		return nil
	}
	d.service = func(ctx context.Context) (service.Manager, service.Definition) {
		return installedService(ctx, m)
	}
	return &serviceFixture{f: f, m: m, log: &events, b: serviceBackend{
		manager: func() service.Manager { return m },
		exe:     func() string { return "/usr/bin/nearhost" },
		getenv: func(n string) string {
			// The manager's own agent socket, which needs no note.
			if n == "SSH_AUTH_SOCK" {
				return "/run/agent"
			}
			return ""
		},
		lookPath:    func(string) (string, error) { return "", errors.New("none") },
		goos:        "linux",
		euid:        func() int { return 1000 },
		detach:      d,
		interactive: func() bool { return true },
		home:        "/home/u",
	}}
}

func (x *serviceFixture) run(t *testing.T, input string, args ...string) (string, error) {
	t.Helper()
	return executeWithInput(t, input, newServiceCmd(x.b), args...)
}

func TestInstallRefusesRoot(t *testing.T) {
	x := newServiceFixture(t)
	x.b.euid = func() int { return 0 }
	out, err := x.run(t, "", "service", "install")
	if err == nil || !strings.Contains(out, "Install the service as yourself, not as root: it runs as the user who installs it.") {
		t.Fatalf("err %v, out %q", err, out)
	}
	if x.m.log() != "" {
		t.Errorf("manager was asked: %s", x.m.log())
	}
}

func TestInstallFresh(t *testing.T) {
	x := newServiceFixture(t)
	x.m.onStart = func() { x.f.answer(control.Info{API: 1, PID: 4343, StartedBy: control.StartedService}) }
	out, err := x.run(t, "", "service", "install")
	if err != nil {
		t.Fatal(err)
	}
	if got := x.m.log(); got != "install,start" {
		t.Errorf("calls %q", got)
	}
	want := "Installed the Nearhost service. It starts at login and runs /usr/bin/nearhost. Nearhost is running now (pid 4343).\n"
	if out != want {
		t.Errorf("out %q, want %q", out, want)
	}
	if x.m.got.Exe != "/usr/bin/nearhost" {
		t.Errorf("definition %+v", x.m.got)
	}
}

func TestInstallUpToDate(t *testing.T) {
	x := newServiceFixture(t)
	x.m.changed = false
	out, err := x.run(t, "", "service", "install")
	if err != nil || out != "The Nearhost service is installed and up to date.\n" {
		t.Fatalf("err %v, out %q", err, out)
	}
	if got := x.m.log(); got != "install" {
		t.Errorf("calls %q", got)
	}
}

func TestInstallBesideAnotherCore(t *testing.T) {
	x := newServiceFixture(t)
	x.f.answer(control.Info{API: 1, PID: 77, StartedBy: control.StartedTUI})
	out, err := x.run(t, "", "service", "install")
	if err != nil {
		t.Fatal(err)
	}
	if got := x.m.log(); got != "install" {
		t.Errorf("calls %q", got)
	}
	want := "Installed the Nearhost service. It starts at login and runs /usr/bin/nearhost. Nearhost is already running (started by tui), so the service takes over from your next login. To hand over now, run nearhost stop, then nearhost -d.\n"
	if out != want {
		t.Errorf("out %q, want %q", out, want)
	}
}

func TestReinstallAsksToRestart(t *testing.T) {
	const ask = "The service's settings changed. Restart Nearhost now to apply them? Open passthroughs close. [Y/n]"
	for _, tc := range []struct {
		name        string
		input       string
		args        []string
		interactive bool
		restart     bool
	}{
		{"yes", "y\n", nil, true, true},
		{"no", "n\n", nil, true, false},
		{"enter", "\n", nil, true, true},
		{"flag without a terminal", "", []string{"--yes"}, false, true},
		{"no terminal", "", nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := newServiceFixture(t)
			x.b.interactive = func() bool { return tc.interactive }
			x.f.answer(control.Info{API: 1, PID: 50, StartedBy: control.StartedService})
			x.m.onStart = func() {
				x.f.answer(control.Info{API: 1, PID: 51, StartedBy: control.StartedService})
			}
			// infoCore ignores Stop, so the fake core is closed by gone.
			gone := x.b.detach.gone
			x.b.detach.gone = func(ctx context.Context) error {
				x.f.mu.Lock()
				x.f.srv.Close()
				x.f.mu.Unlock()
				return gone(ctx)
			}
			out, err := x.run(t, tc.input, append([]string{"service", "install"}, tc.args...)...)
			if err != nil {
				t.Fatal(err)
			}
			if tc.restart {
				if got := x.m.log(); got != "install,start" {
					t.Errorf("calls %q", got)
				}
				if !strings.Contains(out, "Restarted Nearhost with the new settings.") {
					t.Errorf("out %q", out)
				}
				if len(*x.log) != 1 {
					t.Errorf("gone events %v", *x.log)
				}
			} else {
				if len(*x.log) != 0 {
					t.Errorf("Nearhost was stopped: gone events %v", *x.log)
				}
				if got := x.m.log(); got != "install" {
					t.Errorf("calls %q", got)
				}
				if !strings.Contains(out, "The new settings apply from Nearhost's next start.") {
					t.Errorf("out %q", out)
				}
			}
			asked := strings.Contains(out, ask)
			if asked != (tc.interactive && len(tc.args) == 0) {
				t.Errorf("asked %v, out %q", asked, out)
			}
		})
	}
}

func TestInstallPrintsNotes(t *testing.T) {
	x := newServiceFixture(t)
	x.m.changed = false
	x.b.exe = func() string { return "/home/u/.nvm/versions/node/v26.13.0/bin/nearhost" }
	out, err := x.run(t, "", "service", "install")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "The service runs nearhost from Node v26.13.0's folder.") {
		t.Errorf("out %q", out)
	}
}

func TestUninstallStopsOnlyTheServiceCore(t *testing.T) {
	for _, tc := range []struct {
		by   control.StartedBy
		stop bool
	}{{control.StartedService, true}, {control.StartedForeground, false}} {
		t.Run(string(tc.by), func(t *testing.T) {
			x := newServiceFixture(t)
			x.f.answer(control.Info{API: 1, PID: 9, StartedBy: tc.by})
			out, err := x.run(t, "", "service", "uninstall")
			if err != nil {
				t.Fatal(err)
			}
			want := "Removed the Nearhost service.\n"
			if tc.stop {
				want = "Removed the Nearhost service. Stopped Nearhost, which it ran.\n"
			}
			if out != want {
				t.Errorf("out %q, want %q", out, want)
			}
			if got := len(*x.log) == 1; got != tc.stop {
				t.Errorf("gone events %v, stop %v", *x.log, tc.stop)
			}
			if x.m.log() != "uninstall" {
				t.Errorf("calls %q", x.m.log())
			}
		})
	}
}

func TestUninstallNone(t *testing.T) {
	x := newServiceFixture(t)
	x.m.installed = false
	out, err := x.run(t, "", "service", "uninstall")
	if err != nil || out != "No Nearhost service is installed.\n" {
		t.Fatalf("err %v, out %q", err, out)
	}
	if x.m.log() != "" {
		t.Errorf("calls %q", x.m.log())
	}
}

func TestNoUserManagerMessage(t *testing.T) {
	x := newServiceFixture(t)
	x.m.err = service.ErrNoUserManager
	out, err := x.run(t, "", "service", "install")
	var ee exitError
	if !errors.As(err, &ee) || ee.code != 1 || !strings.Contains(out, service.ErrNoUserManager.Error()) {
		t.Fatalf("err %v, out %q", err, out)
	}
}

func TestInstallShowsManagerOutputPrintably(t *testing.T) {
	x := newServiceFixture(t)
	x.m.err = errors.New("systemctl failed:\n\x1b[31mboom\x1b[0m \n")
	out, err := x.run(t, "", "service", "install")
	if err == nil {
		t.Fatal("no error")
	}
	var buf strings.Builder
	exitCode(err, &buf)
	got := out + buf.String()
	if !strings.Contains(got, "boom") || strings.Contains(got, "\x1b") {
		t.Errorf("out %q", got)
	}
}

func TestStatusMentionsService(t *testing.T) {
	for _, tc := range []struct {
		name      string
		installed bool
		err       error
		want      bool
	}{{"installed", true, nil, true}, {"none", false, nil, false}, {"error", true, errors.New("x"), false}} {
		t.Run(tc.name, func(t *testing.T) {
			configHome(t)
			m := &recordingService{installed: tc.installed, instErr: tc.err}
			out, err := execute(t, newStatusCmd(func() service.Manager { return m }), "status")
			var ee exitError
			if !errors.As(err, &ee) || ee.code != statusNotRunningCode {
				t.Fatalf("err %v", err)
			}
			if got := strings.Contains(out, "A service is installed: start it with nearhost -d."); got != tc.want {
				t.Errorf("out %q", out)
			}
		})
	}
}

func TestStatusRunningByServiceAddsNothing(t *testing.T) {
	st := testStatus
	st.Info.StartedBy = control.StartedService
	serveStatus(t, st)
	m := &recordingService{installed: true}
	out, err := execute(t, newStatusCmd(func() service.Manager { return m }), "status")
	if err != nil || strings.Contains(out, "A service is installed") {
		t.Fatalf("err %v, out %q", err, out)
	}
}

// A core that says the service started it, found once install has run, is
// the service's: install says it is running, not that another core is.
func TestInstallFindsTheServiceCoreStartedByTheManager(t *testing.T) {
	x := newServiceFixture(t)
	dial := x.b.detach.dial
	dials := 0
	x.b.detach.dial = func() (*control.Client, error) {
		dials++
		if dials == 2 {
			x.f.answer(control.Info{API: 1, PID: 88, StartedBy: control.StartedService})
		}
		return dial()
	}
	out, err := x.run(t, "", "service", "install")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Nearhost is running now (pid 88).") || strings.Contains(out, "already running") {
		t.Errorf("out %q", out)
	}
	if got := x.m.log(); got != "install" {
		t.Errorf("calls %q", got)
	}
}

func TestUninstallFailureSaysNearhostWasStopped(t *testing.T) {
	x := newServiceFixture(t)
	x.m.uninstallErr = errors.New("launchctl failed")
	x.f.answer(control.Info{API: 1, PID: 9, StartedBy: control.StartedService})
	out, err := x.run(t, "", "service", "uninstall")
	if err == nil {
		t.Fatal("no error")
	}
	if !strings.Contains(out, "Stopped Nearhost, which the service ran.") || !strings.Contains(out, "launchctl failed") {
		t.Errorf("out %q", out)
	}
}
