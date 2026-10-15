package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/transport"
)

func TestEmptyStateLeadsToAdding(t *testing.T) {
	f := newFake(t, &config.Config{})
	f.forms.files = &fakeFiles{dirs: map[string]bool{"/work/here": true}, drafts: map[string]Draft{
		"/work/here": {Routes: []config.Route{{Port: 3000, Role: config.RoleWeb, Label: "port (PORT)"}}},
	}}
	m := sized(f)
	wantScreen(t, m, "home", "▾ local", "Nothing to serve yet.", "Press n to add the project in the current directory.", "n add · ? help · q quit")
	press(m, "n")
	settle(m, press(m, "enter")) // Local
	wantScreen(t, m, "path      /work/here▏")
	settle(m, press(m, "enter"))
	press(m, "enter")
	settle(m, press(m, "enter"))
	settle(m, press(m, "enter"))
	if len(f.forms.saved) != 1 || f.forms.saved[0].Name != "here" || m.form != nil {
		t.Fatalf("saved %+v, form %T", f.forms.saved, m.form)
	}
}

func TestSidebarShowsServersProjectsAndStates(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.setState("website", engine.ProjectStatus{State: engine.StateRunning})
	f.setState("myapp", engine.ProjectStatus{State: engine.StateStarting})
	f.servers["mini"] = transport.StatusAuthFailed
	m := sized(f)
	wantScreen(t, m, "▾ local", "● website", "running", "▾ mini", "○ auth failed", "myapp", "starting", "○ old")
}

func TestHomeShowsTheMachine(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.trusted = false
	f.path = "/etc/nearhost/config.yaml"
	f.setState("website", engine.ProjectStatus{State: engine.StateRunning})
	m := sized(f)
	run(m, func() tea.Msg { return startedMsg{} })
	wantScreen(t, m, "proxy     ● https :443 · http :80", "local CA  ● not trusted. Run nearhost trust", "hosts     ● 2 aliases", "config    "+f.path,
		"running   1 of 3 projects on 1 of 2 servers")
}

func TestProxyThatCannotStartIsShownAndNothingStarts(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := sized(f)
	if cmd := run(m, func() tea.Msg { return startedMsg{errBoom} }); cmd != nil {
		t.Error("projects were started without a proxy")
	}
	wantScreen(t, m, "proxy     ● down", "● proxy  down", "why       boom", "The proxy could not start: boom")
}

func TestStartedEngineStartsEveryEnabledProject(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := sized(f)
	_, cmd := m.Update(startedMsg{})
	for _, c := range cmd().(tea.BatchMsg) {
		c()
	}
	if strings.Join(f.started, ",") != "website,myapp" {
		t.Errorf("started %v", f.started)
	}
}

func TestProjectPanel(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.setState("myapp", engine.ProjectStatus{State: engine.StateError, Message: "loopback port 5173 is in use", WebLocalPort: 52114})
	f.servers["mini"] = transport.StatusConnected
	m := sized(f)
	m.sel = rowKey(rowProject, "myapp")
	wantScreen(t, m, "myapp  https://myapp.test", "● error", "server mini    path /srv/myapp",
		"why       loopback port 5173 is in use", "fix       Resolve what is named above, then press enter to start it", "again.",
		" port    role        label", "80      web         web", "52114     ○", "5173    loopback    vite (VITE_PORT)", "5173      ○",
		"3306    ignore      db", "–         –",
		"enter start · o open · tab routes · e edit · d remove · / filter · s server · ? more", "myapp 2/3")
}

func TestFixFor(t *testing.T) {
	for _, c := range []struct {
		st       engine.State
		reason   string
		serverUp bool
		want     string
	}{
		{engine.StateRunning, "", true, ""},
		{engine.StateStopped, "", true, ""},
		{engine.StateStopped, "alias not resolvable", true, "Press enter and approve the password prompt"},
		{engine.StateDegraded, "", false, "run `ssh mini.lan` once"},
		{engine.StateError, "auth", false, "Run `ssh mini.lan` once in a terminal to fix access, then press enter."},
		{engine.StateError, "port", true, "Resolve what is named above"},
	} {
		if got := fixFor(c.st, c.reason, c.serverUp, "mini.lan"); !strings.Contains(got, c.want) || (c.want == "") != (got == "") {
			t.Errorf("fixFor(%s, %q, %v) = %q, want %q", c.st, c.reason, c.serverUp, got, c.want)
		}
	}
}

func TestServerPanelShowsTheErrorAndFix(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.servers["mini"] = transport.StatusAuthFailed
	f.srvErrs["mini"] = "user@mini.lan: Permission denied (publickey)."
	m := sized(f)
	m.sel = rowKey(rowServer, "mini")
	wantScreen(t, m, "mini", "○ auth failed", "ssh host  mini.lan", "why       user@mini.lan: Permission denied (publickey).",
		"fix       Run ssh mini.lan once in a terminal to fix access, then", "press r.", "projects  myapp, old", "enter collapse")
}

func TestTooSmall(t *testing.T) {
	m := New(newFake(t, sampleConfig()), "/")
	m.Update(tea.WindowSizeMsg{Width: 79, Height: 30})
	if s := screen(m); !strings.Contains(s, "Make the window larger (80×20 or more).") || strings.Contains(s, "website") {
		t.Fatalf("screen:\n%s", s)
	}
}

func TestMovingAndEnter(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := sized(f)
	press(m, "down") // Local
	press(m, "j")    // website
	if m.sel != rowKey(rowProject, "website") {
		t.Fatalf("selected %q", m.sel)
	}
	run(m, press(m, "enter"))
	f.setState("website", engine.ProjectStatus{State: engine.StateRunning})
	run(m, press(m, "enter"))
	if strings.Join(f.started, ",") != "website" || strings.Join(f.stopped, ",") != "website" {
		t.Errorf("started %v, stopped %v", f.started, f.stopped)
	}
	press(m, "up")
	press(m, "enter") // collapse Local
	if s := screen(m); !strings.Contains(s, "▸ local") || strings.Contains(s, "● website") || strings.Contains(s, "○ website") {
		t.Errorf("Local not collapsed:\n%s", s)
	}
	for i := 0; i < 10; i++ {
		press(m, "down")
	}
	if m.sel != rowKey(rowProject, "old") {
		t.Errorf("moved past the end to %q", m.sel)
	}
	press(m, "h")
	if m.sel != homeKey {
		t.Errorf("h selected %q", m.sel)
	}
}

func TestOpen(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := sized(f)
	m.sel = rowKey(rowProject, "myapp")
	run(m, press(m, "o"))
	f.openErr = errBoom
	run(m, press(m, "o"))
	if len(f.opened) != 2 || f.opened[0] != "https://myapp.test" {
		t.Errorf("opened %v", f.opened)
	}
	m.sel = homeKey
	wantScreen(t, m, "Open https://myapp.test in your browser.")
}

func TestEventsFillTheLogs(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := sized(f)
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for _, ev := range []engine.Event{
		{Time: at, Project: "myapp", Server: "mini", State: engine.StateRunning, Message: "state: running"},
		{Time: at, Server: "mini", Message: "connected"},
		{Time: at, Project: "website", Message: `{"level":"info","msg":"handled request","request":{"method":"GET","uri":"/"},"duration":0.004,"status":200}`},
		{Time: at, Message: "proxy ready\x1b[2J"},
	} {
		m.Update(eventMsg(ev))
	}
	wantScreen(t, m, "12:00:00  myapp     state: running", "12:00:00  mini      connected", "12:00:00  website   GET /", "200    4ms",
		"12:00:00  proxy     proxy ready")
	m.sel = rowKey(rowProject, "myapp")
	if s := screen(m); !strings.Contains(s, "mini      connected") || strings.Contains(s, "website   GET") {
		t.Errorf("myapp's log:\n%s", s)
	}
}

func TestChangedConfigFileIsApplied(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := sized(f)
	if cmd := m.checkConfig(); cmd != nil {
		t.Fatal("applied an unchanged config")
	}
	if err := os.WriteFile(f.path, []byte("projects: [{name: a, server: nowhere}]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.applyErr = errBoom
	// The file is applied once it has stayed the same for a tick, so a save
	// caught half way is not.
	if cmd := m.checkConfig(); cmd != nil {
		t.Fatal("applied a change on the tick that first saw it")
	}
	run(m, m.checkConfig())
	if len(f.applied) != 1 {
		t.Fatalf("applied %d times", len(f.applied))
	}
	if cmd := m.checkConfig(); cmd != nil {
		t.Error("a refused change was applied again")
	}
	wantScreen(t, m, "Ignored a change to", "config.yaml: boom")
}

func TestQuitStopsEverythingAndASecondQuitCutsItShort(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := sized(f)
	cmd := press(m, "q")
	wantScreen(t, m, "Stopping…")
	press(m, "q")
	if _, final := m.Update(cmd()); final == nil {
		t.Fatal("no quit after shutdown")
	}
	if len(f.shutdown) != 1 || f.shutdown[0].Err() != context.Canceled {
		t.Errorf("shutdown contexts %v", f.shutdown)
	}
}

func TestMouse(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := sized(f)
	m.Update(tea.MouseClickMsg{X: 3, Y: bodyTop + 4, Button: tea.MouseLeft}) // website, under a blank line, home, a blank line and local
	if m.sel != rowKey(rowProject, "website") {
		t.Fatalf("click selected %q", m.sel)
	}
	// The footer reads " enter start/stop · o open · ..."; o is at column 20.
	m.Update(tea.MouseClickMsg{X: 20, Y: 29, Button: tea.MouseLeft})
	_, cmd := m.Update(tea.MouseClickMsg{X: 20, Y: 29, Button: tea.MouseLeft})
	run(m, cmd)
	if len(f.opened) == 0 || f.opened[0] != "https://website.test" {
		t.Errorf("footer click opened %v", f.opened)
	}
}

// TestProgram runs the model in a real program: a function run through the
// Suspender gets the terminal, and q then quits after the shutdown.
func TestProgram(t *testing.T) {
	f := newFake(t, sampleConfig())
	in, typed := io.Pipe()
	t.Cleanup(func() { in.Close() }) // ends the writes nobody reads
	var out bytes.Buffer
	p := tea.NewProgram(New(f, "/"), tea.WithInput(in), tea.WithOutput(&out), tea.WithWindowSize(100, 30), tea.WithoutSignalHandler())
	var s Suspender
	if err := s.Run(context.Background(), nil); err != errNotRunning {
		t.Fatalf("Run before Attach = %v", err)
	}
	s.Attach(p)
	ended := make(chan error, 1)
	go func() {
		_, err := p.Run()
		ended <- err
	}()
	ran := false
	err := s.Run(context.Background(), func(_ io.Reader, w io.Writer) error {
		ran = true
		_, err := io.WriteString(w, "Nearhost needs your password\n")
		return err
	})
	if err != nil || !ran {
		t.Fatalf("Suspender.Run = %v, ran %v", err, ran)
	}
	// A pipe, unlike a terminal, cannot take back the read that was waiting
	// when the program handed the terminal over, so the first key typed
	// afterwards can go to it. Type q until the program ends.
	deadline := time.After(10 * time.Second)
	for done := false; !done; {
		go io.WriteString(typed, "q")
		select {
		case err := <-ended:
			if err != nil {
				t.Fatal(err)
			}
			done = true
		case <-time.After(200 * time.Millisecond):
		case <-deadline:
			t.Fatal("the program did not end after q")
		}
	}
	if len(f.shutdown) != 1 {
		t.Errorf("shut down %d times", len(f.shutdown))
	}
	if !strings.Contains(out.String(), "Nearhost needs your password") {
		t.Errorf("the suspended function's output is missing")
	}
}

// When the selected project leaves the config, Home is selected; a project
// that arrives is selected, so a project just added is in view.
func TestSelectionFollowsConfigChanges(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := sized(f)
	m.sel = rowKey(rowProject, "old")
	cfg := sampleConfig()
	cfg.Projects = cfg.Projects[:2]
	f.cfg = cfg
	m.Update(appliedMsg{})
	if m.sel != homeKey {
		t.Errorf("selected %q after its project went", m.sel)
	}
	m.sel = rowKey(rowServer, "mini")
	cfg = sampleConfig()
	f.cfg = cfg
	m.Update(appliedMsg{})
	if m.sel != rowKey(rowProject, "old") {
		t.Errorf("selected %q after a project arrived", m.sel)
	}
	m.Update(appliedMsg{})
	if m.sel != rowKey(rowProject, "old") {
		t.Errorf("selection moved to %q with nothing changed", m.sel)
	}
}

// Names, labels and messages from outside cannot draw on the screen.
func TestOutsideTextIsCleaned(t *testing.T) {
	cfg := sampleConfig()
	cfg.Projects[0].Routes[0].Label = "app\x1b[31m\x1b[2J\nnext"
	f := newFake(t, cfg)
	f.setState("website", engine.ProjectStatus{State: engine.StateError, Message: "bad\x1b]0;title\x07 thing\r"})
	m := sized(f)
	m.sel = rowKey(rowProject, "website")
	s := m.View().Content
	if strings.Contains(s, "\x1b[2J") || strings.Contains(s, "\x1b]0;") || strings.Contains(s, "\r") {
		t.Errorf("escape sequences reached the screen: %q", s)
	}
	wantScreen(t, m, "app next", "bad thing")
}

// An editor that truncates the file before writing it leaves it empty for a
// moment. An empty file is not applied, so it never stops every project.
func TestEmptyConfigFileIsIgnored(t *testing.T) {
	f := newFake(t, sampleConfig())
	if err := os.WriteFile(f.path, []byte("projects: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := sized(f)
	if err := os.WriteFile(f.path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m.checkConfig()
	run(m, m.checkConfig())
	if len(f.applied) != 0 {
		t.Fatalf("applied an empty file: %+v", f.applied)
	}
	if log := logText(m, homeKey); !strings.Contains(log, "Ignored a change to "+f.path+": the file is empty") {
		t.Errorf("log:\n%s", log)
	}
}

// A start or stop the engine refuses outright, such as one while the proxy
// is down, changes no state; the refusal is shown instead.
func TestRefusedStartIsShown(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.startErr2 = errors.New("engine is not started")
	m := sized(f)
	m.sel = rowKey(rowProject, "website")
	run(m, press(m, "enter"))
	wantScreen(t, m, "website   engine is not started")
	m.sel = homeKey
	wantScreen(t, m, "website   engine is not started")
}

// Whether the CA is trusted and the state of the hosts block are read on the
// clock, not on every redraw.
func TestHomeReadsTheMachineOnTheTick(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := sized(f)
	run(m, func() tea.Msg { return startedMsg{} })
	before := f.machineReads()
	for i := 0; i < 20; i++ {
		m.View()
	}
	if after := f.machineReads(); after != before {
		t.Errorf("redrawing read the machine %d times", after-before)
	}
	f.trusted = false
	m.firstRunShown = true // this test is about Home, not the first-run screen
	wantScreen(t, m, "local CA  ● trusted")
	run(m, m.readMachine())
	wantScreen(t, m, "local CA  ● not trusted")
}

// With the proxy down, Home says how to get round it, even with no projects.
func TestProxyDownSaysHowToFixIt(t *testing.T) {
	for _, cfg := range []*config.Config{sampleConfig(), {}} {
		f := newFake(t, cfg)
		m := sized(f)
		run(m, func() tea.Msg { return startedMsg{errors.New("HTTPS port 443 is in use by nginx (pid 12)")} })
		wantScreen(t, m, "● proxy  down", "why       HTTPS port 443 is in use by nginx (pid 12)",
			"fix       Quit the program using the port, or run nearhost with --", "https-port and --http-port.")
	}
}

// A change that could not be applied says how to try again.
func TestIgnoredChangeSaysHowToRetry(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.path = "/etc/nh.yaml" // short enough for the line not to wrap
	m := sized(f)
	m.Update(appliedMsg{errBoom})
	wantScreen(t, m, "nearhost  Ignored a change to /etc/nh.yaml: boom. Save the", "file again to apply it.")
}

// Quitting while an Apply runs ends it, so the shutdown does not wait for
// the projects it is stopping.
func TestQuitEndsAnApplyInFlight(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.applyBlocks = true
	m := sized(f)
	if err := os.WriteFile(f.path, []byte("projects: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := m.reloadConfig()
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	press(m, "q")
	select {
	case msg := <-done:
		if am, ok := msg.(appliedMsg); !ok || am.err == nil {
			t.Errorf("Apply ended with %#v", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the Apply kept running after q")
	}
}

// A password request that arrives while the interface is quitting is
// answered at once, without taking the terminal.
func TestSuspendWhileQuittingIsRefused(t *testing.T) {
	m := sized(newFake(t, sampleConfig()))
	press(m, "q")
	done := make(chan error, 1)
	if _, cmd := m.Update(suspendMsg{fn: func(io.Reader, io.Writer) error { return nil }, done: done}); cmd != nil {
		t.Fatal("took the terminal while quitting")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Error("the request was answered with no error")
		}
	default:
		t.Fatal("the request was not answered")
	}
}

// manyProjects is a config with more local projects than the sidebar shows.
func manyProjects(n int) *config.Config {
	cfg := &config.Config{}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("p%02d", i)
		cfg.Projects = append(cfg.Projects, config.Project{Name: name, Server: config.LocalServer, Path: "/" + name,
			Alias: name + ".test", Enabled: true, Routes: []config.Route{{Port: 3000 + i, Role: config.RoleWeb}}})
	}
	return cfg
}

// The sidebar scrolls to keep the selected row in view, and a click picks
// the row drawn under the pointer.
func TestSidebarKeepsTheSelectionInView(t *testing.T) {
	m := sized(newFake(t, manyProjects(40)))
	for i := 0; i < 41; i++ {
		press(m, "down")
	}
	if m.sel != rowKey(rowProject, "p39") {
		t.Fatalf("selected %q", m.sel)
	}
	lines := strings.Split(screen(m), "\n")
	if !strings.Contains(lines[bodyTop+m.bodyHeight()-1], "p39") {
		t.Errorf("the selected row is not on the last sidebar line:\n%s", screen(m))
	}
	m.Update(tea.MouseClickMsg{X: 3, Y: bodyTop + m.bodyHeight() - 2, Button: tea.MouseLeft})
	if m.sel != rowKey(rowProject, "p38") {
		t.Errorf("click selected %q, want the row drawn there", m.sel)
	}
}

// A project added under a collapsed server is selected and shown: its
// server opens.
func TestNewProjectUnderACollapsedServerIsShown(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := sized(f)
	m.collapsed["mini"] = true
	cfg := sampleConfig()
	cfg.Projects = append(cfg.Projects, config.Project{Name: "fresh", Server: "mini", Path: "/srv/fresh", Alias: "fresh.test", Enabled: true,
		Routes: []config.Route{{Port: 80, Role: config.RoleWeb}}})
	f.cfg = cfg
	m.Update(appliedMsg{})
	wantScreen(t, m, "▾ mini", "https://fresh.test")
}

// Invisible format characters, such as a right-to-left override, cannot
// change how a name reads.
func TestFormatCharactersAreRemoved(t *testing.T) {
	cfg := sampleConfig()
	cfg.Projects[0].Routes[0].Label = "abc‮def​"
	m := sized(newFake(t, cfg))
	m.sel = rowKey(rowProject, "website")
	if s := screen(m); strings.ContainsAny(s, "‮​") || !strings.Contains(s, "abcdef") {
		t.Errorf("format characters reached the screen:\n%s", s)
	}
}
