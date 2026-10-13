package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/hosts"
	"github.com/nearhost/nearhost/internal/testutil"
	"github.com/nearhost/nearhost/internal/transport"
)

var bg = context.Background()

func TestLocalProjectIsServedAndStopped(t *testing.T) {
	upPort := upstream(t, "ok")
	web := localProject("web", upPort)
	// A local project's loopback route needs nothing: the port is already local.
	web.Routes = append(web.Routes, config.Route{Port: testutil.FreePort(t), Role: config.RoleLoopback})
	h := newHarness(t, &config.Config{Projects: []config.Project{web}})

	if portOpen(h.httpsPort) {
		t.Fatal("proxy listens before any project is started")
	}
	if st := h.eng.Status("web"); st.State != StateStopped || st.Alias != "web.test" {
		t.Fatalf("status before start = %+v", st)
	}
	if err := h.eng.StartProject(bg, "web"); err != nil {
		t.Fatalf("StartProject: %v", err)
	}
	st := h.eng.Status("web")
	if st.State != StateRunning || st.WebLocalPort != upPort || st.Alias != "web.test" {
		t.Fatalf("status = %+v", st)
	}
	if calls := h.ensureCalls(); len(calls) != 1 || len(calls[0]) != 1 || calls[0][0] != "web.test" {
		t.Fatalf("EnsureHosts calls = %v", calls)
	}
	if code, body := h.getEventually(t, "web.test", "/"); code != 200 || !strings.HasPrefix(body, "ok:web.test") {
		t.Fatalf("through proxy: %d %q", code, body)
	}
	if sites := h.eng.Sites(); len(sites) != 1 || sites[0].Alias != "web.test" || sites[0].Upstream != "127.0.0.1:"+strconv.Itoa(upPort) {
		t.Fatalf("Sites = %+v", sites)
	}
	if log := h.log(); log != "" {
		t.Fatalf("a local project must not run ssh:\n%s", log)
	}

	if err := h.eng.StopProject(bg, "web"); err != nil {
		t.Fatal(err)
	}
	if st := h.eng.Status("web"); st.State != StateStopped || st.WebLocalPort != 0 || len(h.eng.Sites()) != 0 {
		t.Fatalf("expected stopped with no sites, got %+v", st)
	}
	if code, body := h.get(t, "web.test", "/"); code == 200 && strings.HasPrefix(body, "ok:") {
		t.Fatal("project still served after stop")
	}
	if portOpen(h.httpsPort) || portOpen(h.httpPort) {
		t.Fatal("stopping the last project must leave the proxy with no listeners")
	}
}

func TestRemoteProjectForwardsThroughSSH(t *testing.T) {
	lb := testutil.FreePort(t)
	h := newHarness(t, remoteConfig(lb))
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatalf("StartProject: %v", err)
	}
	st := h.eng.Status("myapp")
	if st.State != StateRunning || st.WebLocalPort == 0 || st.WebLocalPort == 80 {
		t.Fatalf("status = %+v", st)
	}
	log := h.log()
	if !regexp.MustCompile(`-O forward -L 127\.0\.0\.1:` + strconv.Itoa(st.WebLocalPort) + `:localhost:80 mini`).MatchString(log) {
		t.Fatalf("web forward missing:\n%s", log)
	}
	if !strings.Contains(log, fmt.Sprintf("-O forward -L 127.0.0.1:%d:localhost:%d mini", lb, lb)) {
		t.Fatalf("loopback forward missing:\n%s", log)
	}
	if strings.Contains(log, "localhost:3306") {
		t.Fatal("ignored route must not be forwarded")
	}
	if status, _ := h.eng.ServerStatus("mini"); status != transport.StatusConnected {
		t.Fatalf("server status = %s", status)
	}
	if sites := h.eng.Sites(); len(sites) != 1 || sites[0].Upstream != "127.0.0.1:"+strconv.Itoa(st.WebLocalPort) {
		t.Fatalf("Sites = %+v", sites)
	}

	if err := h.eng.StopProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	if strings.Count(h.log(), "-O cancel") != 2 {
		t.Fatalf("expected two cancels:\n%s", h.log())
	}
	if h.eng.Status("myapp").State != StateStopped || len(h.eng.Sites()) != 0 || len(h.forwards("mini")) != 0 {
		t.Fatalf("expected stopped with nothing left: %+v, forwards %v", h.eng.Status("myapp"), h.forwards("mini"))
	}
	// The master stays up for the next start; only Shutdown ends it.
	if status, _ := h.eng.ServerStatus("mini"); status != transport.StatusConnected {
		t.Fatalf("server status after stop = %s", status)
	}
}

func TestServerStatusOfLocalAndUnusedServers(t *testing.T) {
	h := newHarness(t, remoteConfig(testutil.FreePort(t)))
	if st, msg := h.eng.ServerStatus(config.LocalServer); st != transport.StatusConnected || msg != "" {
		t.Fatalf("local = %s %q", st, msg)
	}
	if st, _ := h.eng.ServerStatus("mini"); st != transport.StatusDisconnected {
		t.Fatalf("unused server = %s", st)
	}
	if st := h.eng.Status("nope"); st.State != StateStopped || st.Alias != "" {
		t.Fatalf("unknown project status = %+v", st)
	}
}

func TestStartingTwiceIsANoOp(t *testing.T) {
	lb := testutil.FreePort(t)
	h := newHarness(t, remoteConfig(lb))
	// Concurrent starts: one does the work, the rest return at once.
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = h.eng.StartProject(bg, "myapp")
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("StartProject: %v", err)
		}
	}
	testutil.WaitUntil(t, 10*time.Second, func() bool { return h.eng.Status("myapp").State == StateRunning }, "running")
	first := h.eng.Status("myapp")
	// And a start of a running project.
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatalf("second StartProject: %v", err)
	}
	if got := h.eng.Status("myapp"); got != first {
		t.Fatalf("status changed by a second start: %+v -> %+v", first, got)
	}
	want := [][2]int{{first.WebLocalPort, 80}, {lb, lb}}
	if want[0][0] > want[1][0] {
		want[0], want[1] = want[1], want[0]
	}
	if got := h.forwards("mini"); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("forwards = %v, want exactly %v", got, want)
	}
	if n := strings.Count(h.log(), "-O forward"); n != 2 {
		t.Fatalf("%d forward commands, want 2:\n%s", n, h.log())
	}
	if len(h.eng.Sites()) != 1 {
		t.Fatalf("Sites = %+v", h.eng.Sites())
	}
}

func TestLoopbackBindFailureIsReportedButWebStillServed(t *testing.T) {
	lb := testutil.FreePort(t)
	h := newHarness(t, remoteConfig(lb), "FAKE_SSH_FORWARD_FAIL="+strconv.Itoa(lb))
	events, unsub := h.eng.Events().Subscribe(256)
	defer unsub()
	err := h.eng.StartProject(bg, "myapp")
	if err == nil || !strings.Contains(err.Error(), strconv.Itoa(lb)) {
		t.Fatalf("expected error naming port %d, got %v", lb, err)
	}
	st := h.eng.Status("myapp")
	if st.State != StateError || !strings.Contains(st.Message, strconv.Itoa(lb)) || st.WebLocalPort == 0 {
		t.Fatalf("status = %+v", st)
	}
	if len(h.eng.Sites()) != 1 {
		t.Fatal("web route should still be proxied")
	}
	if f := h.forwards("mini"); len(f) != 1 || f[0] != [2]int{st.WebLocalPort, 80} {
		t.Fatalf("forwards = %v, want only the web forward", f)
	}
	// The forward error is a log line of the project.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Project == "myapp" && ev.State == "" && strings.Contains(ev.Message, strconv.Itoa(lb)) && strings.Contains(ev.Message, "Address already in use") {
				return
			}
		case <-deadline:
			t.Fatal("no project log line for the failed forward")
		}
	}
}

func TestLoopbackPortAlreadyBoundLocally(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	lb := l.Addr().(*net.TCPAddr).Port
	h := newHarness(t, remoteConfig(lb))

	err = h.eng.StartProject(bg, "myapp")
	if err == nil || !strings.Contains(err.Error(), strconv.Itoa(lb)) {
		t.Fatalf("expected error naming port %d, got %v", lb, err)
	}
	st := h.eng.Status("myapp")
	if st.State != StateError || !strings.Contains(st.Message, strconv.Itoa(lb)) || !strings.Contains(st.Message, "in use") {
		t.Fatalf("status = %+v", st)
	}
	if len(h.eng.Sites()) != 1 || st.WebLocalPort == 0 {
		t.Fatal("web route should still be proxied")
	}
	if strings.Contains(h.log(), fmt.Sprintf("-L 127.0.0.1:%d:", lb)) {
		t.Fatalf("a forward was requested for a port that is in use:\n%s", h.log())
	}

	// Once the port is free, starting again replaces the partial start: the
	// old web forward is cancelled, not left behind.
	l.Close()
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatalf("StartProject after freeing the port: %v", err)
	}
	st2 := h.eng.Status("myapp")
	if st2.State != StateRunning || st2.Message != "" {
		t.Fatalf("status = %+v", st2)
	}
	if f := h.forwards("mini"); len(f) != 2 {
		t.Fatalf("forwards = %v, want exactly the web and loopback forwards", f)
	}
	if n := strings.Count(h.log(), "-O cancel"); n != 1 {
		t.Fatalf("%d cancels, want 1 (the first web forward):\n%s", n, h.log())
	}
}

func TestLoopbackPortClashBetweenProjects(t *testing.T) {
	lb := testutil.FreePort(t)
	cfg := &config.Config{
		Servers:  []config.Server{{Name: "mini", SSHHost: "mini"}},
		Projects: []config.Project{remoteProject("one", "mini", lb), remoteProject("two", "mini", lb)},
	}
	h := newHarness(t, cfg)
	if err := h.eng.StartProject(bg, "one"); err != nil {
		t.Fatalf("StartProject one: %v", err)
	}
	err := h.eng.StartProject(bg, "two")
	if err == nil || !strings.Contains(err.Error(), strconv.Itoa(lb)) || !strings.Contains(err.Error(), `"one"`) {
		t.Fatalf("expected a clash error naming port %d and project one, got %v", lb, err)
	}
	two := h.eng.Status("two")
	if two.State != StateError || !strings.Contains(two.Message, strconv.Itoa(lb)) || two.WebLocalPort == 0 {
		t.Fatalf("status of two = %+v", two)
	}
	if one := h.eng.Status("one"); one.State != StateRunning {
		t.Fatalf("status of one = %+v", one)
	}
	if len(h.eng.Sites()) != 2 {
		t.Fatalf("both web routes should be proxied: %+v", h.eng.Sites())
	}
	if n := strings.Count(h.log(), fmt.Sprintf("-O forward -L 127.0.0.1:%d:", lb)); n != 1 {
		t.Fatalf("loopback port forwarded %d times, want once", n)
	}

	// Stopping the second project must not take the first one's forward.
	if err := h.eng.StopProject(bg, "two"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.log(), fmt.Sprintf("-O cancel -L 127.0.0.1:%d:", lb)) {
		t.Fatalf("stopping two cancelled one's loopback forward:\n%s", h.log())
	}
	if f := h.forwards("mini"); len(f) != 2 {
		t.Fatalf("forwards = %v, want one's two forwards", f)
	}

	// The port is free for the second project once the first lets go of it.
	if err := h.eng.StopProject(bg, "one"); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.StartProject(bg, "two"); err != nil {
		t.Fatalf("StartProject two after one stopped: %v", err)
	}
}

func TestAuthFailureMarksProjectError(t *testing.T) {
	h := newHarness(t, remoteConfig(testutil.FreePort(t)), "FAKE_SSH_AUTH_FAIL=1")
	ctx, cancel := context.WithTimeout(bg, 20*time.Second)
	defer cancel()
	err := h.eng.StartProject(ctx, "myapp")
	if err == nil || !strings.Contains(err.Error(), "Permission denied") {
		t.Fatalf("expected auth error, got %v", err)
	}
	st := h.eng.Status("myapp")
	if st.State != StateError || !strings.Contains(st.Message, "Permission denied") || st.WebLocalPort != 0 {
		t.Fatalf("status = %+v", st)
	}
	if status, msg := h.eng.ServerStatus("mini"); status != transport.StatusAuthFailed || !strings.Contains(msg, "Permission denied") {
		t.Fatalf("server status = %s %q", status, msg)
	}
	if len(h.eng.Sites()) != 0 {
		t.Fatal("no site should be published")
	}
	time.Sleep(300 * time.Millisecond) // a fourth attempt would show up by now
	if n := strings.Count(h.log(), "-N -M"); n != 3 {
		t.Fatalf("ssh was launched %d times, want 3 attempts and then no more:\n%s", n, h.log())
	}
	if len(h.ensureCalls()) != 0 || strings.Contains(h.log(), "-O forward") {
		t.Fatal("nothing else may be started for a project whose server cannot connect")
	}
}

func TestAuthFailureOnOneServerDoesNotBlockOthers(t *testing.T) {
	wrapper, _ := authWrapper(t)
	upPort := upstream(t, "ok")
	cfg := &config.Config{
		Servers: []config.Server{{Name: "bad", SSHHost: "badhost"}, {Name: "mini", SSHHost: "mini"}},
		Projects: []config.Project{
			remoteProject("broken", "bad", testutil.FreePort(t)),
			remoteProject("myapp", "mini", testutil.FreePort(t)),
			localProject("web", upPort),
		},
	}
	h := buildHarness(t, cfg, func(o *Options) { o.SSHPath = wrapper })
	if err := h.eng.Start(bg); err != nil {
		t.Fatal(err)
	}
	brokenErr := make(chan error, 1)
	go func() { brokenErr <- h.eng.StartProject(bg, "broken") }()
	testutil.WaitUntil(t, 5*time.Second, func() bool { return h.eng.Status("broken").State == StateStarting }, "broken to be starting")

	// While the bad server is still retrying, the others start normally.
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatalf("StartProject myapp: %v", err)
	}
	if err := h.eng.StartProject(bg, "web"); err != nil {
		t.Fatalf("StartProject web: %v", err)
	}
	select {
	case err := <-brokenErr:
		t.Fatalf("the failing project finished first (%v); the others did not start alongside it", err)
	default:
	}
	if code, body := h.getEventually(t, "web.test", "/"); code != 200 || !strings.HasPrefix(body, "ok:") {
		t.Fatalf("web through proxy: %d %q", code, body)
	}

	if err := <-brokenErr; err == nil || !strings.Contains(err.Error(), "Permission denied") {
		t.Fatalf("expected auth error for broken, got %v", err)
	}
	if st := h.eng.Status("broken"); st.State != StateError {
		t.Fatalf("broken = %+v", st)
	}
	for _, name := range []string{"myapp", "web"} {
		if st := h.eng.Status(name); st.State != StateRunning {
			t.Fatalf("%s = %+v", name, st)
		}
	}
	if st, _ := h.eng.ServerStatus("bad"); st != transport.StatusAuthFailed {
		t.Fatalf("bad server = %s", st)
	}
	if st, _ := h.eng.ServerStatus("mini"); st != transport.StatusConnected {
		t.Fatalf("mini server = %s", st)
	}
	if len(h.eng.Sites()) != 2 {
		t.Fatalf("Sites = %+v", h.eng.Sites())
	}
}

func TestStartAfterAuthFailureTriesAgain(t *testing.T) {
	wrapper, flag := authWrapper(t)
	if err := os.WriteFile(flag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h := buildHarness(t, remoteConfig(testutil.FreePort(t)), func(o *Options) { o.SSHPath = wrapper })
	if err := h.eng.Start(bg); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.StartProject(bg, "myapp"); err == nil {
		t.Fatal("expected auth error")
	}
	if err := os.Remove(flag); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatalf("StartProject after the key was fixed: %v", err)
	}
	if st := h.eng.Status("myapp"); st.State != StateRunning || st.Message != "" {
		t.Fatalf("status = %+v", st)
	}
}

func TestStartRevivesAServerThatGaveUpUnderARunningProject(t *testing.T) {
	wrapper, flag := authWrapper(t)
	h := buildHarness(t, remoteConfig(testutil.FreePort(t)), func(o *Options) { o.SSHPath = wrapper })
	if err := h.eng.Start(bg); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	running := h.eng.Status("myapp")
	// The connection drops and the key is no longer accepted.
	if err := os.WriteFile(flag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h.eng.mu.Lock()
	sock := h.eng.servers["mini"].sess.SocketPath()
	h.eng.mu.Unlock()
	if err := os.Remove(sock); err != nil {
		t.Fatal(err)
	}
	testutil.WaitUntil(t, 15*time.Second, func() bool {
		st, _ := h.eng.ServerStatus("mini")
		return st == transport.StatusAuthFailed
	}, "the server to give up")
	st := h.eng.Status("myapp")
	if st.State != StateDegraded || !strings.Contains(st.Message, "Permission denied") {
		t.Fatalf("status = %+v", st)
	}

	if err := os.Remove(flag); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatalf("StartProject: %v", err)
	}
	testutil.WaitUntil(t, 10*time.Second, func() bool { return h.eng.Status("myapp").State == StateRunning }, "recovery")
	if got := h.eng.Status("myapp"); got.WebLocalPort != running.WebLocalPort || len(h.forwards("mini")) != 2 {
		t.Fatalf("status = %+v (was %+v), forwards %v", got, running, h.forwards("mini"))
	}
}

func TestRunningProjectBecomesDegradedWhenServerDrops(t *testing.T) {
	upPort := upstream(t, "ok")
	cfg := remoteConfig(testutil.FreePort(t))
	cfg.Projects = append(cfg.Projects, localProject("web", upPort))
	h := newHarness(t, cfg, "FAKE_SSH_FLAKY_ONCE=2")
	events, unsub := h.eng.Events().Subscribe(256)
	defer unsub()
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatalf("StartProject: %v", err)
	}
	if err := h.eng.StartProject(bg, "web"); err != nil {
		t.Fatalf("StartProject web: %v", err)
	}
	testutil.WaitUntil(t, 5*time.Second, func() bool { return h.eng.Status("myapp").State == StateDegraded }, "degraded state")
	st := h.eng.Status("myapp")
	if !strings.Contains(st.Message, "Broken pipe") || st.WebLocalPort == 0 {
		t.Fatalf("degraded status = %+v", st)
	}
	if web := h.eng.Status("web"); web.State != StateRunning {
		t.Fatalf("a local project is not affected by a server dropping: %+v", web)
	}
	testutil.WaitUntil(t, 10*time.Second, func() bool { return h.eng.Status("myapp").State == StateRunning }, "recovery")
	if st := h.eng.Status("myapp"); st.Message != "" {
		t.Fatalf("recovered status = %+v", st)
	}

	// Both transitions are announced, and the supervisor's messages arrive as
	// log lines of the server.
	var states []State
	var sawSupervisor bool
	deadline := time.After(5 * time.Second)
	for len(states) < 4 || !sawSupervisor {
		select {
		case ev := <-events:
			if ev.Project == "myapp" && ev.State != "" {
				states = append(states, ev.State)
			}
			if ev.Server == "mini" && ev.Project == "" && strings.Contains(ev.Message, "reconnecting") {
				sawSupervisor = true
			}
		case <-deadline:
			t.Fatalf("events missing: states=%v supervisor=%v", states, sawSupervisor)
		}
	}
	want := []State{StateStarting, StateRunning, StateDegraded, StateRunning}
	for i := range want {
		if states[i] != want[i] {
			t.Fatalf("state events = %v, want %v", states, want)
		}
	}
}

func TestUnknownProjectAndMissingWebRoute(t *testing.T) {
	cfg := remoteConfig(testutil.FreePort(t))
	cfg.Projects[0].Routes[0].Role = config.RoleIgnore
	cfg.Projects = append(cfg.Projects, config.Project{
		Name: "lost", Server: "gone", Alias: "lost.test", Routes: []config.Route{{Port: 80, Role: config.RoleWeb}},
	})
	h := newHarness(t, cfg)
	if err := h.eng.StartProject(bg, "nope"); err == nil {
		t.Fatal("expected unknown project error")
	}
	if err := h.eng.StopProject(bg, "nope"); err == nil {
		t.Fatal("expected unknown project error on stop")
	}
	if err := h.eng.StartProject(bg, "myapp"); err == nil || !strings.Contains(err.Error(), "web") {
		t.Fatalf("expected missing web route error, got %v", err)
	}
	if st := h.eng.Status("myapp"); st.State != StateError || !strings.Contains(st.Message, "web") {
		t.Fatalf("status = %+v", st)
	}
	if err := h.eng.StartProject(bg, "lost"); err == nil || !strings.Contains(err.Error(), "gone") {
		t.Fatalf("expected unknown server error, got %v", err)
	}
	if h.log() != "" || len(h.ensureCalls()) != 0 || len(h.eng.Sites()) != 0 {
		t.Fatal("a project that cannot start must not start anything")
	}
}

func TestEventsCarryStateChangesAndLogs(t *testing.T) {
	h := newHarness(t, &config.Config{Projects: []config.Project{localProject("web", upstream(t, "ok"))}})
	events, unsub := h.eng.Events().Subscribe(256)
	defer unsub()
	if err := h.eng.StartProject(bg, "web"); err != nil {
		t.Fatal(err)
	}
	h.getEventually(t, "web.test", "/hello")
	var sawRunning, sawAccess bool
	deadline := time.After(5 * time.Second)
	for !(sawRunning && sawAccess) {
		select {
		case ev := <-events:
			if ev.Time.IsZero() {
				t.Fatalf("event without a time: %+v", ev)
			}
			if ev.Project == "web" && ev.State == StateRunning && strings.Contains(ev.Message, "running") {
				sawRunning = true
			}
			if ev.Project == "web" && ev.State == "" && strings.Contains(ev.Message, "/hello") {
				sawAccess = true
			}
		case <-deadline:
			t.Fatalf("events missing: running=%v access=%v", sawRunning, sawAccess)
		}
	}
}

func TestHostsFailureLeavesProjectStopped(t *testing.T) {
	h := newHarness(t, remoteConfig(testutil.FreePort(t)))
	h.setHostsErr(errors.New("sudo: a password is required"))
	err := h.eng.StartProject(bg, "myapp")
	if err == nil || !strings.Contains(err.Error(), "a password is required") {
		t.Fatalf("expected the hosts error, got %v", err)
	}
	st := h.eng.Status("myapp")
	if st.State != StateStopped || st.WebLocalPort != 0 {
		t.Fatalf("status = %+v, want stopped", st)
	}
	for _, want := range []string{"myapp.test", "not resolvable", "start the project again"} {
		if !strings.Contains(st.Message, want) {
			t.Fatalf("message %q does not contain %q", st.Message, want)
		}
	}
	if len(h.eng.Sites()) != 0 || len(h.forwards("mini")) != 0 || strings.Contains(h.log(), "-O forward") {
		t.Fatalf("nothing may be started when the alias cannot resolve; sites %v, log:\n%s", h.eng.Sites(), h.log())
	}

	// Retrying after the cause is fixed works.
	h.setHostsErr(nil)
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if st := h.eng.Status("myapp"); st.State != StateRunning || st.Message != "" {
		t.Fatalf("status after retry = %+v", st)
	}
}

func TestInvalidAliasFailsOnlyItsOwnProject(t *testing.T) {
	bad := localProject("bad", upstream(t, "bad"))
	bad.Alias = "Bad_Alias.dev"
	cfg := &config.Config{Projects: []config.Project{bad, localProject("web", upstream(t, "ok"))}}
	hostsFile := filepath.Join(t.TempDir(), "hosts")
	h := buildHarness(t, cfg, func(o *Options) {
		o.EnsureHosts = func(aliases []string) error {
			_, err := hosts.Ensure(hostsFile, aliases, func(a []string) error { return hosts.Apply(hostsFile, a) })
			return err
		}
	})
	if err := h.eng.Start(bg); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.StartProject(bg, "web"); err != nil {
		t.Fatalf("a project with a valid alias must start beside one with an invalid alias: %v", err)
	}
	if got, _ := os.ReadFile(hostsFile); !strings.Contains(string(got), "127.0.0.1 web.test") {
		t.Fatalf("hosts file = %q", got)
	}
	err := h.eng.StartProject(bg, "bad")
	if err == nil || !strings.Contains(err.Error(), "Bad_Alias.dev") {
		t.Fatalf("expected an invalid alias error, got %v", err)
	}
	if st := h.eng.Status("bad"); st.State != StateError {
		t.Fatalf("bad = %+v", st)
	}
	if st := h.eng.Status("web"); st.State != StateRunning {
		t.Fatalf("web = %+v", st)
	}
}

func TestStoppingOneProjectKeepsOthersServing(t *testing.T) {
	cfg := &config.Config{Projects: []config.Project{
		localProject("alpha", upstream(t, "alpha")),
		localProject("beta", upstream(t, "beta")),
	}}
	h := newHarness(t, cfg)
	for _, name := range []string{"alpha", "beta"} {
		if err := h.eng.StartProject(bg, name); err != nil {
			t.Fatalf("StartProject %s: %v", name, err)
		}
	}
	for _, name := range []string{"alpha", "beta"} {
		if code, body := h.getEventually(t, name+".test", "/"); code != 200 || !strings.HasPrefix(body, name+":") {
			t.Fatalf("%s through proxy: %d %q", name, code, body)
		}
	}
	if err := h.eng.StopProject(bg, "alpha"); err != nil {
		t.Fatal(err)
	}
	if code, body := h.get(t, "beta.test", "/"); code != 200 || !strings.HasPrefix(body, "beta:") {
		t.Fatalf("beta after alpha stopped: %d %q", code, body)
	}
	if code, body := h.get(t, "alpha.test", "/"); code == 200 && strings.HasPrefix(body, "alpha:") {
		t.Fatal("alpha still served after stop")
	}
	if sites := h.eng.Sites(); len(sites) != 1 || sites[0].Alias != "beta.test" {
		t.Fatalf("Sites = %+v", sites)
	}
}

func TestStartFailsWhenAProxyPortIsTaken(t *testing.T) {
	for _, which := range []string{"https", "http"} {
		t.Run(which, func(t *testing.T) {
			l, err := net.Listen("tcp", ":0")
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			held := l.Addr().(*net.TCPAddr).Port
			h := buildHarness(t, &config.Config{Projects: []config.Project{localProject("web", upstream(t, "ok"))}}, func(o *Options) {
				if which == "https" {
					o.HTTPSPort = held
				} else {
					o.HTTPPort = held
				}
			})
			h.httpsPort, h.httpPort = h.opts.HTTPSPort, h.opts.HTTPPort
			err = h.eng.Start(bg)
			if err == nil || !strings.Contains(err.Error(), strconv.Itoa(held)) {
				t.Fatalf("expected an error naming port %d, got %v", held, err)
			}
			if err := h.eng.StartProject(bg, "web"); err == nil {
				t.Fatal("no project may start when the engine failed to start")
			}
			if st := h.eng.Status("web"); st.State != StateStopped || len(h.eng.Sites()) != 0 || len(h.ensureCalls()) != 0 {
				t.Fatalf("status = %+v", st)
			}
			l.Close()
			// The conflict gone, the same engine starts.
			if err := h.eng.Start(bg); err != nil {
				t.Fatalf("Start after the port was freed: %v", err)
			}
			if err := h.eng.StartProject(bg, "web"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStopProjectReportsFailedCancelAndCanBeRetried(t *testing.T) {
	block := filepath.Join(t.TempDir(), "cancel-fails")
	if err := os.WriteFile(block, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, remoteConfig(testutil.FreePort(t)), "FAKE_SSH_CANCEL_FAIL="+block)
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	err := h.eng.StopProject(bg, "myapp")
	if err == nil || !strings.Contains(err.Error(), "cancel") {
		t.Fatalf("expected a cancel error, got %v", err)
	}
	if st := h.eng.Status("myapp"); st.State != StateError || !strings.Contains(st.Message, "cancel") || st.WebLocalPort != 0 {
		t.Fatalf("status = %+v", st)
	}
	if len(h.eng.Sites()) != 0 {
		t.Fatal("the site must be gone even when a forward could not be cancelled")
	}
	if err := os.Remove(block); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.StopProject(bg, "myapp"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if st := h.eng.Status("myapp"); st.State != StateStopped || len(h.forwards("mini")) != 0 {
		t.Fatalf("status = %+v, forwards %v", st, h.forwards("mini"))
	}
}

func TestShutdownStopsEverythingAndKeepsHosts(t *testing.T) {
	cfg := remoteConfig(testutil.FreePort(t))
	cfg.Projects = append(cfg.Projects, localProject("web", upstream(t, "ok")))
	hostsFile := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(hostsFile, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := buildHarness(t, cfg, func(o *Options) {
		o.EnsureHosts = func(aliases []string) error {
			_, err := hosts.Ensure(hostsFile, aliases, func(a []string) error { return hosts.Apply(hostsFile, a) })
			return err
		}
	})
	if err := h.eng.Start(bg); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"myapp", "web"} {
		if err := h.eng.StartProject(bg, name); err != nil {
			t.Fatalf("StartProject %s: %v", name, err)
		}
	}
	h.getEventually(t, "web.test", "/")
	before, _ := os.ReadFile(hostsFile)
	if !strings.Contains(string(before), "127.0.0.1 myapp.test") || !strings.Contains(string(before), "127.0.0.1 web.test") {
		t.Fatalf("hosts file = %q", before)
	}

	if err := h.eng.Shutdown(bg); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	for _, name := range []string{"myapp", "web"} {
		if st := h.eng.Status(name); st.State != StateStopped {
			t.Fatalf("%s = %+v", name, st)
		}
	}
	if !strings.Contains(h.log(), "-O exit mini") {
		t.Fatalf("the master was not asked to exit:\n%s", h.log())
	}
	if st, _ := h.eng.ServerStatus("mini"); st != transport.StatusDisconnected {
		t.Fatalf("server = %s", st)
	}
	if after, _ := os.ReadFile(hostsFile); string(after) != string(before) {
		t.Fatalf("Shutdown changed the hosts file:\n%s", after)
	}
	h.assertNothingLeft(t)

	if err := h.eng.StartProject(bg, "web"); err == nil {
		t.Fatal("StartProject after Shutdown must fail")
	}
	if err := h.eng.Start(bg); err == nil {
		t.Fatal("Start after Shutdown must fail")
	}
	if err := h.eng.Shutdown(bg); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
}

func TestShutdownReturnsTeardownErrorsAndStillStopsTheRest(t *testing.T) {
	block := filepath.Join(t.TempDir(), "cancel-fails")
	if err := os.WriteFile(block, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := remoteConfig(testutil.FreePort(t))
	cfg.Projects = append(cfg.Projects, localProject("web", upstream(t, "ok")))
	h := newHarness(t, cfg, "FAKE_SSH_CANCEL_FAIL="+block)
	for _, name := range []string{"myapp", "web"} {
		if err := h.eng.StartProject(bg, name); err != nil {
			t.Fatal(err)
		}
	}
	err := h.eng.Shutdown(bg)
	if err == nil || !strings.Contains(err.Error(), "myapp") || !strings.Contains(err.Error(), "cancel") {
		t.Fatalf("expected Shutdown to report the failed cancel of myapp, got %v", err)
	}
	if !strings.Contains(h.log(), "-O exit mini") {
		t.Fatal("the master must still be stopped")
	}
	if st := h.eng.Status("web"); st.State != StateStopped {
		t.Fatalf("web = %+v", st)
	}
	h.assertNothingLeft(t)
}

func TestShutdownInterruptsAStartInProgress(t *testing.T) {
	h := newHarness(t, remoteConfig(testutil.FreePort(t)), "FAKE_SSH_HANG=1")
	started := make(chan error, 1)
	go func() { started <- h.eng.StartProject(bg, "myapp") }()
	testutil.WaitUntil(t, 5*time.Second, func() bool {
		return h.eng.Status("myapp").State == StateStarting && strings.Contains(h.log(), "-N -M")
	}, "the start to be waiting for ssh")
	begin := time.Now()
	if err := h.eng.Shutdown(bg); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if d := time.Since(begin); d > 10*time.Second {
		t.Fatalf("Shutdown waited %s for a start that could not finish", d)
	}
	if err := <-started; err == nil {
		t.Fatal("the interrupted start must report an error")
	}
	if st := h.eng.Status("myapp"); st.State != StateStopped || len(h.eng.Sites()) != 0 {
		t.Fatalf("status = %+v", st)
	}
}

func TestShutdownLeavesAProxyTakenOverByAnotherEngine(t *testing.T) {
	cfg := &config.Config{Projects: []config.Project{localProject("web", upstream(t, "ok"))}}
	old := newHarness(t, cfg)
	h := newHarness(t, cfg) // takes over the process-wide proxy and its log sink
	events, unsub := h.eng.Events().Subscribe(256)
	defer unsub()
	if err := old.eng.Shutdown(bg); err != nil {
		t.Fatalf("Shutdown of the replaced engine: %v", err)
	}
	if err := h.eng.StartProject(bg, "web"); err != nil {
		t.Fatal(err)
	}
	if code, _ := h.getEventually(t, "web.test", "/hello"); code != 200 {
		t.Fatalf("status %d through the newer engine's proxy", code)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Project == "web" && strings.Contains(ev.Message, "/hello") {
				return
			}
		case <-deadline:
			t.Fatal("the newer engine lost its proxy log lines when the older one shut down")
		}
	}
}

func TestProjectsBeforeStartAreRefused(t *testing.T) {
	h := buildHarness(t, &config.Config{Projects: []config.Project{localProject("web", upstream(t, "ok"))}}, nil)
	if err := h.eng.StartProject(bg, "web"); err == nil {
		t.Fatal("StartProject before Start must fail")
	}
	if err := h.eng.StopProject(bg, "web"); err != nil {
		t.Fatalf("stopping a project that never ran is a no-op, got %v", err)
	}
	if st := h.eng.Status("web"); st.State != StateStopped {
		t.Fatalf("status = %+v", st)
	}
}

func TestConfigIsSnapshotted(t *testing.T) {
	cfg := &config.Config{Projects: []config.Project{localProject("web", upstream(t, "ok"))}}
	h := newHarness(t, cfg)
	cfg.Projects[0].Alias = "changed.test"
	got := h.eng.Config()
	if got.Projects[0].Alias != "web.test" {
		t.Fatalf("engine config followed a later change by the caller: %+v", got.Projects[0])
	}
	got.Projects[0].Routes[0].Port = 1
	if h.eng.Config().Projects[0].Routes[0].Port == 1 {
		t.Fatal("Config must return a copy")
	}
}

// TestConcurrentUse hammers the whole API from many goroutines. It asserts
// little by itself; the race detector and the leak checks in the harness
// cleanup do the work.
func TestConcurrentUse(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.Server{{Name: "mini", SSHHost: "mini"}, {Name: "maxi", SSHHost: "maxi"}},
		Projects: []config.Project{
			remoteProject("one", "mini", testutil.FreePort(t)),
			remoteProject("two", "mini", testutil.FreePort(t)),
			remoteProject("three", "maxi", testutil.FreePort(t)),
			localProject("alpha", upstream(t, "alpha")),
			localProject("beta", upstream(t, "beta")),
		},
	}
	h := newHarness(t, cfg)
	names := []string{"one", "two", "three", "alpha", "beta"}
	stop := make(chan struct{})
	var readers, workers sync.WaitGroup
	for i := 0; i < 3; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			events, unsub := h.eng.Events().Subscribe(8)
			defer unsub()
			for {
				select {
				case <-stop:
					return
				case <-events:
				default:
				}
				for _, n := range names {
					h.eng.Status(n)
				}
				h.eng.Sites()
				h.eng.ServerStatus("mini")
				h.eng.Config()
				time.Sleep(time.Millisecond)
			}
		}()
	}
	for _, n := range names {
		for w := 0; w < 2; w++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for i := 0; i < 3; i++ {
					h.eng.StartProject(bg, n) // may race a stop; either outcome is fine
					h.eng.StopProject(bg, n)
				}
			}()
		}
	}
	workers.Wait()
	close(stop)
	readers.Wait()

	// After the storm every project starts cleanly and holds exactly its own
	// forwards.
	for _, n := range names {
		if err := h.eng.StopProject(bg, n); err != nil {
			t.Fatalf("StopProject %s: %v", n, err)
		}
	}
	if f := append(h.forwards("mini"), h.forwards("maxi")...); len(f) != 0 {
		t.Fatalf("forwards left after stopping everything: %v", f)
	}
	for _, n := range names {
		if err := h.eng.StartProject(bg, n); err != nil {
			t.Fatalf("StartProject %s: %v", n, err)
		}
	}
	if len(h.eng.Sites()) != 5 || len(h.forwards("mini")) != 4 || len(h.forwards("maxi")) != 2 {
		t.Fatalf("sites %v, mini %v, maxi %v", h.eng.Sites(), h.forwards("mini"), h.forwards("maxi"))
	}
	for _, n := range []string{"alpha", "beta"} {
		if code, body := h.getEventually(t, n+".test", "/"); code != 200 || !strings.HasPrefix(body, n+":") {
			t.Fatalf("%s through proxy: %d %q", n, code, body)
		}
	}
}
