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
	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/transport"
)

func TestEmptyStateLeadsToAdding(t *testing.T) {
	f := newFake(t, &config.Config{})
	f.forms.files = &fakeFiles{dirs: map[string]bool{"/work/here": true}, drafts: map[string]Draft{
		"/work/here": {Routes: []config.Route{{Port: 3000, Role: config.RoleWeb, Label: "port (PORT)"}}},
	}}
	m := sized(f)
	wantScreen(t, m, "nearhost", "Nothing to serve yet.",
		"n  add a project on this machine or a server", "s  add a server you reach over SSH",
		"directory /work/here", " n add project · s add server · ? help · q quit")
	// The two keys line up, one under the other.
	col := func(sub string) int {
		for _, l := range strings.Split(screen(m), "\n") {
			if i := strings.Index(l, sub); i >= 0 {
				return len([]rune(l[:i]))
			}
		}
		return -1
	}
	if n, s := col("n  add a project"), col("s  add a server"); n < 0 || n != s {
		t.Errorf("n at column %d, s at %d", n, s)
	}
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
	wantScreen(t, m, "▾ local", "● website", "running", "▾ mini", "✕ auth failed", "myapp", "starting", "○ old")
}

func TestHomeShowsTheMachine(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.trusted = false
	f.path = "/etc/nearhost/config.yaml"
	f.setState("website", engine.ProjectStatus{State: engine.StateRunning})
	m := sized(f)
	run(m, func() tea.Msg { return startedMsg{} })
	wantScreen(t, m, "proxy     ● https :443 · http :80", "page      https://nearhost.localhost:6327", "local CA  ! not trusted. Run nearhost trust", "hosts     ● 2 names", "config    "+f.path,
		"running   1 of 3 projects on 1 of 2 servers")
	if !strings.Contains(m.View().Content, "\x1b]8;;https://nearhost.localhost:6327") {
		t.Error("the page address is not a link")
	}
}

func TestProxyThatCannotStartIsShownAndNothingStarts(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := sized(f)
	if cmd := run(m, func() tea.Msg { return startedMsg{errBoom} }); cmd != nil {
		t.Error("projects were started without a proxy")
	}
	wantScreen(t, m, "proxy     ✕ down", "✕ proxy  down", "why       boom", "The proxy could not start: boom")
	if strings.Contains(screen(m), "https://nearhost.localhost") {
		t.Error("the page is linked while the proxy is down")
	}
}

// The core starts the enabled projects: the interface that follows it
// starts none, so one the user stopped stays stopped.
func TestStartedBackendStartsNoProject(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := sized(f)
	_, cmd := m.Update(startedMsg{})
	if msg := cmd(); msg != nil {
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				c()
			}
		}
	}
	if len(f.started) != 0 {
		t.Errorf("started %v", f.started)
	}
}

func TestProjectPanel(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.setState("myapp", engine.ProjectStatus{State: engine.StateError, Message: "loopback port 5173 is in use", WebLocalPort: 52114})
	f.servers["mini"] = transport.StatusConnected
	m := sized(f)
	m.sel = rowKey(rowProject, "myapp")
	wantScreen(t, m, "myapp  https://myapp.test", "✕ error", "server mini    path /srv/myapp",
		"why       loopback port 5173 is in use", "fix       enter start it again",
		" port    role        label", "80      web         web", "52114     ○", "5173    loopback    vite (VITE_PORT)", "5173      ○",
		"1 ignored: 3306  ·  i show",
		"enter start · o open · tab routes · e edit · d remove · l log · / filter · ? more", "myapp 2/3")
	if strings.Contains(screen(m), "3306    ignore") {
		t.Errorf("the ignored route is not folded:\n%s", screen(m))
	}
	press(m, "i")
	wantScreen(t, m, "3306    ignore      db", "–         –", "i hide the 1 ignored")
	press(m, "i")
	wantScreen(t, m, "1 ignored: 3306  ·  i show")
	press(m, "tab") // every route can be selected
	wantScreen(t, m, "3306    ignore      db")
	if strings.Contains(screen(m), "ignored") {
		t.Errorf("the routes have the keys, but the fold line shows:\n%s", screen(m))
	}
	press(m, "down")
	press(m, "down")
	if !strings.Contains(screen(m), "▌3306    ignore      db") { // the cursor reaches the folded route
		t.Errorf("3306 is not the selected row:\n%s", screen(m))
	}
	if p, _ := m.cfg.Project("myapp"); p.Routes[m.routeRow].Port != 3306 {
		t.Errorf("route %d selected, want 3306", p.Routes[m.routeRow].Port)
	}
	press(m, "i") // nothing to show: every route is
	press(m, "tab")
	wantScreen(t, m, "1 ignored: 3306  ·  i show")
}

// socketPathReason is transport's message for a run directory too long for
// a control socket.
const socketPathReason = "control socket path /very/long/run/mini.sock is too long for ssh: it is 120 bytes and at most 86 bytes fit; the run directory /very/long/run is too long, set a shorter XDG_CONFIG_HOME"

func TestServerPanelShowsTheErrorAndFix(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.servers["mini"] = transport.StatusAuthFailed
	f.srvErrs["mini"] = "user@mini.lan: Permission denied (publickey)."
	m := sized(f)
	m.sel = rowKey(rowServer, "mini")
	wantScreen(t, m, "mini", "✕ auth failed", "ssh host  mini.lan", "why       user@mini.lan: Permission denied (publickey).",
		"fix       To fix access, run ssh once in a terminal:", "            ssh mini.lan", "Then reconnect the server.", "r reconnect",
		"projects  myapp, old", "enter collapse")
}

func TestTooSmall(t *testing.T) {
	m := New(newFake(t, sampleConfig()), "/")
	m.Update(tea.WindowSizeMsg{Width: 79, Height: 30})
	if s := screen(m); !strings.Contains(s, "Make the window larger (80×20 at least). q quits.") || strings.Contains(s, "website") {
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
	wantScreen(t, m, "Could not open https://myapp.test.")
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

// The interface no longer reads the config file: on the clock it takes the
// config the backend runs, which the core changes when the file does.
func TestConfigFollowsTheBackend(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := sized(f)
	// A file the interface read would be refused, and say so.
	if err := os.WriteFile(f.path, []byte("projects: [{name: a, server: nowhere}]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	next := sampleConfig()
	next.Projects = append(next.Projects, config.Project{Name: "blog", Server: config.LocalServer, Path: "/code/blog",
		Alias: "blog.test", Enabled: true, Routes: []config.Route{{Port: 4000, Role: config.RoleWeb}}})
	m.Update(tickMsg(time.Now()))
	if s := screen(m); strings.Contains(s, "blog") {
		t.Fatalf("a project the backend does not run is shown:\n%s", s)
	}
	f.mu.Lock()
	f.cfg = next
	f.mu.Unlock()
	m.Update(tickMsg(time.Now()))
	wantScreen(t, m, "○ blog")
	if m.sel != rowKey(rowProject, "blog") {
		t.Errorf("the new project is not selected: %q", m.sel)
	}
	if n := f.reloadCount(); n != 0 {
		t.Errorf("the tick reloaded %d times", n)
	}
	if log := logText(m, homeKey); strings.Contains(log, "Ignored a change") {
		t.Errorf("the interface read the file:\n%s", log)
	}
}

// Saving the add-project form has the backend reload its config once.
func TestFormSaveReloads(t *testing.T) {
	m, f, files := projectFormModel(t)
	files.drafts["/work/here"] = Draft{Routes: []config.Route{web(8080, "app")}}
	toPath(t, m, 0) // Local
	settle(m, press(m, "enter"))
	wantScreen(t, m, "Add project · routes")
	press(m, "enter")
	settle(m, press(m, "enter"))
	wantScreen(t, m, "Add project · confirm")
	cmd := press(m, "enter")
	for i := 0; cmd != nil && i < 10; i++ {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			cmd = run(m, tea.Batch(batch...))
			continue
		}
		_, cmd = m.Update(msg)
	}
	if len(f.forms.saved) != 1 {
		t.Fatalf("saved %+v", f.forms.saved)
	}
	if n := f.reloadCount(); n != 1 {
		t.Errorf("reloaded %d times, want once", n)
	}
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
	click(m, 3, bodyTop+3) // website, under home, a blank line and local
	if m.sel != rowKey(rowProject, "website") {
		t.Fatalf("click selected %q", m.sel)
	}
	// The footer reads " enter start/stop · o open · ..."; o is at column 20.
	run(m, click(m, 20, 29))
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

// A start or stop the engine refuses outright, such as one while the proxy
// is down, changes no state; the refusal is shown instead.
func TestRefusedStartIsShown(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.startErr2 = errors.New("engine is not started")
	m := sized(f)
	m.sel = rowKey(rowProject, "website")
	run(m, press(m, "enter"))
	wantText(t, m, "engine is not started")
	press(m, "enter") // the alert closes
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
	wantScreen(t, m, "local CA  ! not trusted")
}

// With the proxy down, Home says how to get round it, even with no projects.
func TestProxyDownSaysHowToFixIt(t *testing.T) {
	for _, cfg := range []*config.Config{sampleConfig(), {}} {
		f := newFake(t, cfg)
		m := sized(f)
		run(m, func() tea.Msg {
			return startedMsg{report.Errorf(report.PortInUse, "HTTPS port 443 is in use by nginx (pid 12)").
				Then("Quit the program using the port, or pass --https-port and --http-port.")}
		})
		wantScreen(t, m, "✕ proxy  down", "why       HTTPS port 443 is in use by nginx (pid 12)",
			"fix       Quit the program using the port, or pass", "http-port.")
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

// Quitting while a Reload runs ends it, so the shutdown does not wait for
// the projects it is stopping.
func TestQuitEndsAReloadInFlight(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.reloadBlocks = true
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
			t.Errorf("Reload ended with %#v", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the Reload kept running after q")
	}
}

// A form saved while a Reload ran waits to reload after it. A quit that
// cancels the running Reload drops that one too: nothing is applied while
// quitting, and the cancelled Reload is not said to have ignored a change.
func TestQuitDropsTheReloadWaitingForARunningOne(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.reloadBlocks = true
	m := sized(f)
	cmd := m.reloadConfig()
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	m.Update(projectAddedMsg{name: "website", edited: true})
	if !m.reloadAfter {
		t.Fatal("the save did not wait for the running Reload")
	}
	quit := press(m, "q")
	var applied tea.Msg
	select {
	case applied = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the Reload kept running after q")
	}
	f.mu.Lock()
	f.reloadBlocks = false // a second Reload would return at once, and count
	f.mu.Unlock()
	_, next := m.Update(applied)
	run(m, next)
	f.mu.Lock()
	calls := f.reloadCalls
	f.mu.Unlock()
	if calls != 1 {
		t.Errorf("Reload was called %d times, want once", calls)
	}
	if log := logText(m, homeKey); strings.Contains(log, "Ignored a change") {
		t.Errorf("the cancelled Reload was noted:\n%s", log)
	}
	if _, final := m.Update(quit()); final == nil {
		t.Fatal("no quit after shutdown")
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
	click(m, 3, bodyTop+m.bodyHeight()-2)
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
	cfg.Projects[0].Routes[0].Label = "abc\u202edef\u200b"
	m := sized(newFake(t, cfg))
	m.sel = rowKey(rowProject, "website")
	if s := screen(m); strings.ContainsAny(s, "\u202e\u200b") || !strings.Contains(s, "abcdef") {
		t.Errorf("format characters reached the screen:\n%s", s)
	}
}

// proxyDown is a model whose first start failed with err, Home selected.
func proxyDown(t *testing.T, f *fakeBackend, err error) *Model {
	t.Helper()
	m := sized(f)
	run(m, func() tea.Msg { return startedMsg{err} })
	return m
}

func TestRTriesTheProxyAgainAndTheItemGoes(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.startErrs = []error{nil}
	m := proxyDown(t, f, errBoom)
	wantScreen(t, m, "✕ proxy  down", "r tries again")
	run(m, run(m, press(m, "r"))) // the answer, then what it reads
	if f.startCalls != 1 {
		t.Fatalf("Start called %d times, want 1", f.startCalls)
	}
	if got := screen(m); strings.Contains(got, "down") || strings.Contains(got, "tries again") {
		t.Errorf("the proxy item stays after a good retry:\n%s", got)
	}
	wantScreen(t, m, "proxy     ● https :443 · http :80")
	// The core starts the projects, not the interface.
	if len(f.started) != 0 {
		t.Errorf("projects started %v", f.started)
	}
}

func TestRDoesNotStartAProxyThatIsUp(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := sized(f)
	run(m, func() tea.Msg { return startedMsg{} })
	run(m, press(m, "r"))
	if f.startCalls != 0 {
		t.Errorf("Start called %d times with the proxy up", f.startCalls)
	}
	if strings.Contains(screen(m), "tries again") {
		t.Error("the hint shows with the proxy up")
	}
}

func TestSecondRDuringARetryStartsNothing(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := proxyDown(t, f, errBoom)
	first := press(m, "r") // in flight: not run yet
	wantScreen(t, m, "proxy     ◐ starting")
	if strings.Contains(screen(m), "tries again") {
		t.Error("the hint shows while starting")
	}
	if second := press(m, "r"); second != nil {
		t.Error("a second r started another retry")
	}
	run(m, first)
	if f.startCalls != 1 {
		t.Errorf("Start called %d times, want 1", f.startCalls)
	}
}

func TestFailedRetryShowsTheNewReasonAndKeepsTheHint(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.startErrs = []error{errors.New("HTTP port 80 is in use by nginx (pid 7)")}
	m := proxyDown(t, f, errors.New("HTTPS port 443 is in use"))
	run(m, press(m, "r"))
	wantScreen(t, m, "proxy     ✕ down", "why       HTTP port 80 is in use by nginx (pid 7)", "r tries again")
	if strings.Contains(screen(m), "why       HTTPS port 443") {
		t.Error("the old reason stays")
	}
	if len(f.started) != 0 {
		t.Errorf("projects started %v without a proxy", f.started)
	}
	f.startErrs = []error{nil}
	run(m, press(m, "r")) // and a later retry works
	if f.startCalls != 2 || strings.Contains(screen(m), "down") {
		t.Errorf("calls %d, screen:\n%s", f.startCalls, screen(m))
	}
}

func TestRDoesNothingWhileTheWindowIsTooSmall(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := proxyDown(t, f, errBoom)
	m.Update(tea.WindowSizeMsg{Width: 10, Height: 5})
	if cmd := press(m, "r"); cmd != nil {
		t.Error("r acted in a window that is too small")
	}
	if f.startCalls != 0 {
		t.Errorf("Start called %d times", f.startCalls)
	}
}

// An error from the user's own action opens the alert, with the message's
// fix, and the log keeps it too.
func TestOwnActionErrorOpensAlert(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.startErr2 = fmt.Errorf("start website: %w", report.Errorf(report.PortInUse, "Port 8080 is in use.").
		Step("See what listens on it:", "lsof -i :8080"))
	m := sized(f)
	m.sel = rowKey(rowProject, "website")
	run(m, press(m, "enter"))
	if m.alertBox == nil {
		t.Fatalf("a refused start opened no alert:\n%s", screen(m))
	}
	wantText(t, m, "start website: Port 8080 is in use.", "See what listens on it:")
	wantScreen(t, m, "  lsof -i :8080")
	if log := logText(m, rowKey(rowProject, "website")); !strings.Contains(log, "Port 8080 is in use.") {
		t.Errorf("the log lacks the refusal:\n%s", log)
	}
}

// A problem that arrives on its own, such as a project failing in the
// background, never opens the alert: it toasts once, as it begins, and
// shows in the problems panel with its fix.
func TestBackgroundProblemDoesNotOpenAlert(t *testing.T) {
	f := newFake(t, sampleConfig())
	m := sized(f)
	problem := report.Errorf(report.PortInUse, "port 8080 is in use").
		Then("Quit the program using port 8080 on this machine, then start the project again.")
	f.setState("website", engine.ProjectStatus{State: engine.StateError, Message: problem.Title, Problem: problem})
	m.Update(eventMsg(engine.Event{Time: time.Now(), Project: "website", State: engine.StateError,
		Message: "state: error: " + problem.Title, Report: problem}))
	if m.alertBox != nil {
		t.Fatal("a background problem opened the alert")
	}
	if !m.toast.problem || !strings.Contains(m.toast.text, "port 8080 is in use") {
		t.Errorf("toast = %+v, want the problem", m.toast)
	}
	wantMain(t, m, "✕ website error", "port 8080 is in use",
		"Quit the program using port 8080 on this machine, then start the project again.")
	// The same problem again is no news: no new toast.
	m.toast.text = ""
	m.Update(eventMsg(engine.Event{Time: time.Now(), Project: "website", State: engine.StateError,
		Message: "state: error: " + problem.Title, Report: problem}))
	if m.toast.text != "" {
		t.Errorf("the same problem toasted again: %q", m.toast.text)
	}
}
