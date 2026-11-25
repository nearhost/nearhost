package cli

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/service"
	"github.com/nearhost/nearhost/internal/testutil"
)

// envCore is a control backend that answers with info and, for the
// environment route, with env or envErr. It counts the environment calls.
type envCore struct {
	control.UnimplementedActions
	info   control.Info
	env    []control.EnvCheck
	envErr error
	calls  *int
}

func (c envCore) Info() control.Info               { return c.info }
func (c envCore) Status() control.Status           { return control.Status{Info: c.info} }
func (c envCore) State() control.State             { return control.State{Info: c.info} }
func (c envCore) Stop()                            {}
func (c envCore) HostsChanged(ctx context.Context) {}
func (c envCore) Environment(context.Context) ([]control.EnvCheck, error) {
	*c.calls++
	return c.env, c.envErr
}

// coreMachine is a doctor backend whose core is a control server the test
// starts, or nothing, and whose service manager is a fake.
type coreMachine struct {
	*fakeMachine
	runDir string
	calls  int
}

func newCoreMachine(t *testing.T) *coreMachine {
	return &coreMachine{fakeMachine: healthy(), runDir: testutil.TempDir(t, "nhd")}
}

func (m *coreMachine) answer(t *testing.T, info control.Info, env []control.EnvCheck, envErr error) {
	t.Helper()
	srv, err := control.Listen(control.SocketPath(m.runDir), envCore{info: info, env: env, envErr: envErr, calls: &m.calls}, control.NewHub())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
}

func (m *coreMachine) backend() doctorBackend {
	b := m.fakeMachine.backend()
	b.dial = func() (*control.Client, error) { return control.Dial(m.runDir) }
	return b
}

func (m *coreMachine) doctor(t *testing.T, b doctorBackend, args ...string) (string, error) {
	t.Helper()
	doctorConfig(t)
	return execute(t, newDoctorCmd(b), append([]string{"doctor"}, args...)...)
}

func thisCore(by control.StartedBy) control.Info {
	return control.Info{API: control.APIVersion, Version: Version, PID: 4242, StartedBy: by}
}

func TestDoctorCoreNotRunning(t *testing.T) {
	m := newCoreMachine(t)
	out, err := m.doctor(t, m.backend())
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	for _, want := range []string{
		"info  core             not running\n      fix: Start it with nearhost -d, or open nearhost\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "core ") > strings.Index(out, "config ") {
		t.Errorf("the core line does not come before the config:\n%s", out)
	}
}

func TestDoctorCoreRunning(t *testing.T) {
	m := newCoreMachine(t)
	m.answer(t, thisCore(control.StartedDetached), []control.EnvCheck{
		{Name: "browser", Level: "ok", Detail: "xdg-open"},
		{Name: "ssh agent", Level: "warn", Detail: "no agent", Fix: "start one"},
		{Name: "docker", Level: "info", Detail: "not installed"},
	}, nil)
	out, err := m.doctor(t, m.backend())
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	for _, want := range []string{
		"ok    core             " + Version + ", started by detached (pid 4242)\n",
		"ok    core: browser    xdg-open\n",
		"warn  core: ssh agent  no agent\n      fix: Start one\n",
		"info  core: docker     not installed\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestDoctorCoreFailingEnvironmentCheckFails(t *testing.T) {
	m := newCoreMachine(t)
	m.answer(t, thisCore(control.StartedDetached), []control.EnvCheck{{Name: "ssh", Level: "fail", Detail: "not found", Fix: "install ssh"}}, nil)
	out, err := m.doctor(t, m.backend())
	if err == nil || !strings.Contains(out, "FAIL  core: ssh") {
		t.Errorf("a failing ssh in the core did not fail doctor: %v\n%s", err, out)
	}
}

func TestDoctorCoreOtherVersion(t *testing.T) {
	m := newCoreMachine(t)
	info := thisCore(control.StartedDetached)
	info.Version = "0.0.1-old"
	m.answer(t, info, nil, nil)
	out, err := m.doctor(t, m.backend())
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	want := "warn  core             0.0.1-old is running; this is " + Version + "\n" +
		"      fix: Restart it: nearhost stop, then nearhost -d (nearhost update does this after an update)\n"
	if !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
}

// What the core says of itself is one line of plain text, as checkService
// prints it: a line break in it does not start a line of doctor's own.
func TestDoctorCorePrintsItsInfoPrintably(t *testing.T) {
	for _, tc := range []struct{ version, by string }{
		{Version, "detached\ninjected\x1b[2J"},
		{"0.0.1\ninjected\x1b[2J", "detached"},
	} {
		m := newCoreMachine(t)
		info := thisCore(control.StartedBy(tc.by))
		info.Version = tc.version
		m.answer(t, info, nil, nil)
		out, _ := m.doctor(t, m.backend())
		if strings.Contains(out, "\x1b") || strings.Contains(out, "\n"+strings.Repeat(" ", 23)+"injected") {
			t.Errorf("version %q, started by %q: the core line is not one line of plain text:\n%s", tc.version, tc.by, out)
		}
	}
}

func TestDoctorCoreOtherAPI(t *testing.T) {
	m := newCoreMachine(t)
	info := thisCore(control.StartedDetached)
	info.API = control.APIVersion + 1
	m.answer(t, info, []control.EnvCheck{{Name: "browser", Level: "ok"}}, nil)
	out, err := m.doctor(t, m.backend())
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	if !strings.Contains(out, "warn  core             speaks a different version of its API\n") {
		t.Errorf("no API warning:\n%s", out)
	}
	if m.calls != 0 || strings.Contains(out, "core: ") {
		t.Errorf("the environment was asked for (%d calls):\n%s", m.calls, out)
	}
}

func TestDoctorCoreOlderRelease(t *testing.T) {
	// A core that does not answer the environment route is not a failure:
	// the exit status is that of a run without a core.
	older := newCoreMachine(t)
	older.answer(t, thisCore(control.StartedDetached), nil, control.ErrNotFound)
	out, err := older.doctor(t, older.backend())
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	if !strings.Contains(out, "info  core: environment not checked: "+control.ErrNoRoute.Error()+"\n") {
		t.Errorf("no not-checked line:\n%s", out)
	}
	none := newCoreMachine(t)
	_, errNone := none.doctor(t, none.backend())
	if (err == nil) != (errNone == nil) {
		t.Errorf("exit differs: %v against %v", err, errNone)
	}
}

// fakeManager is a service.Manager that answers with what it holds.
type fakeManager struct {
	service.Manager
	def       service.Definition
	installed bool
	instErr   error
	active    bool
	activeErr error
}

func (f *fakeManager) Installed(context.Context) (service.Definition, bool, error) {
	return f.def, f.installed, f.instErr
}
func (f *fakeManager) Active(context.Context) (bool, error) { return f.active, f.activeErr }

func TestDoctorService(t *testing.T) {
	const exe = "/opt/nearhost/bin/nearhost"
	mine := service.Definition{Exe: exe}
	logs := testutil.TempDir(t, "nh")
	gone := filepath.Join(logs, "gone")
	withLog := service.Definition{Exe: exe, LogPath: filepath.Join(logs, "core.log")}
	withGoneLog := service.Definition{Exe: exe, LogPath: filepath.Join(gone, "core.log")}
	tests := []struct {
		name     string
		m        *fakeManager
		core     control.StartedBy // "" for no core
		wantLine string
		wantFix  string
	}{
		{"not installed", &fakeManager{}, "", "info  service          not installed\n", "Nearhost service install starts Nearhost at login (optional)"},
		{"unreadable", &fakeManager{installed: true, instErr: service.Unreadable(errors.New("bad plist"))}, "", "warn  service          installed, but its definition cannot be read\n", "Nearhost service uninstall, then nearhost service install"},
		{"another exe", &fakeManager{installed: true, def: service.Definition{Exe: "/usr/bin/nearhost"}}, "", "warn  service          runs /usr/bin/nearhost, not this copy (" + exe + ")\n", "Run nearhost service install from the copy you use"},
		{"active", &fakeManager{installed: true, def: mine, active: true}, control.StartedService, "ok    service          installed and running\n", ""},
		{"active with its log folder", &fakeManager{installed: true, def: withLog, active: true}, control.StartedService, "ok    service          installed and running\n", ""},
		{"log folder missing", &fakeManager{installed: true, def: withGoneLog}, "", "warn  service          its log folder " + gone + " is missing, so the service cannot start again\n", "Run nearhost service install"},
		{"inactive, no core", &fakeManager{installed: true, def: mine}, "", "warn  service          installed but not running\n", "Start it with nearhost -d"},
		{"inactive beside a detached core", &fakeManager{installed: true, def: mine}, control.StartedDetached,
			"info  service          installed; the running Nearhost was started by detached, so the service takes over at your next login\n", ""},
		{"no user manager", &fakeManager{instErr: service.ErrNoUserManager}, "", "info  service          " + messageOf(service.ErrNoUserManager).Title + "\n", ""},
		{"unsupported", &fakeManager{instErr: service.ErrUnsupported}, "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newCoreMachine(t)
			m.exe = exe
			if tt.core != "" {
				m.answer(t, thisCore(tt.core), nil, nil)
			}
			b := m.backend()
			b.service = func() service.Manager { return tt.m }
			out, err := m.doctor(t, b)
			if err != nil {
				t.Fatalf("doctor: %v\n%s", err, out)
			}
			if tt.wantLine == "" {
				if strings.Contains(out, "service ") {
					t.Errorf("a service line for a system without a manager:\n%s", out)
				}
				return
			}
			want := tt.wantLine
			if tt.wantFix != "" {
				want += "      fix: " + tt.wantFix + "\n"
			}
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		})
	}
}

func TestDoctorJSONHasCoreAndService(t *testing.T) {
	m := newCoreMachine(t)
	m.answer(t, thisCore(control.StartedDetached), []control.EnvCheck{{Name: "browser", Level: "ok", Detail: "xdg-open"}}, nil)
	b := m.backend()
	b.service = func() service.Manager { return &fakeManager{} }
	out, err := m.doctor(t, b, "--json")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	for _, want := range []string{`"name": "core"`, `"name": "core: browser"`, `"name": "service"`} {
		if !strings.Contains(out, want) {
			t.Errorf("JSON lacks %s:\n%s", want, out)
		}
	}
}
