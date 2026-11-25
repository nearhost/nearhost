package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/hosts"
	"github.com/nearhost/nearhost/internal/netbind"
	"github.com/nearhost/nearhost/internal/testutil"
	"github.com/nearhost/nearhost/internal/transport"
)

func TestUpServesEnabledProjectsUntilInterrupted(t *testing.T) {
	u := newUpRun(t, &config.Config{Projects: []config.Project{
		localProject("web", testutil.Upstream(t, "web"), true),
		localProject("off", testutil.Upstream(t, "off"), false),
	}})
	u.start()
	u.waitFor("Serving until interrupted")
	out := u.out.String()
	wantContains(t, out, "[proxy] proxy ready\n", "[web] state: starting\n", "[web] state: running\n", "\nweb: running at https://web.test\n")
	if strings.Contains(out, "off") {
		t.Errorf("a disabled project was started:\n%s", out)
	}
	if strings.Index(out, "[web] state: running") > strings.Index(out, "\nweb: running at") {
		t.Errorf("the summary came before the state changes it sums up:\n%s", out)
	}
	if got := u.get("web.test"); !strings.HasPrefix(got, "web:web.test") {
		t.Fatalf("served %q through the proxy", got)
	}
	// Every configured alias is made resolvable, once for the one start.
	if calls := u.ensureCalls(); len(calls) != 1 || strings.Join(calls[0], ",") != "off.test,web.test" {
		t.Fatalf("hosts function calls = %v", calls)
	}
	// The request shows up as a log line of the project.
	u.waitFor("[web] GET /")

	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
	}
	out = u.out.String()
	wantContains(t, out, "\nshutting down\n", "[web] state: stopped\n")
	if !strings.HasSuffix(out, "\nstopped\n") {
		t.Errorf("output does not end with the stopped line:\n%s", out)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("output contains escape codes:\n%q", out)
	}
}

const untrustedLine = "\nThe Nearhost Local CA is not trusted on this machine, so browsers will show a certificate warning. Run `nearhost trust` to fix that.\n"

func TestUpSaysWhenTheLocalCAIsNotTrusted(t *testing.T) {
	u := newUpRun(t, &config.Config{Projects: []config.Project{localProject("web", testutil.Upstream(t, "web"), true)}})
	u.trust.trusted = false
	u.start()
	u.waitFor("Serving until interrupted")
	u.waitFor(untrustedLine)
	// The proxy runs all the same.
	if got := u.get("web.test"); !strings.HasPrefix(got, "web:web.test") {
		t.Fatalf("served %q through the proxy", got)
	}
	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
	}
	out := u.out.String()
	if n := strings.Count(out, "nearhost trust"); n != 1 {
		t.Errorf("the trust hint was printed %d times:\n%s", n, out)
	}
	if summary := strings.Index(out, "\nweb: running at https://web.test\n"); summary < 0 || strings.Index(out, untrustedLine) < summary {
		t.Errorf("the summary is missing, or the trust hint came before it:\n%s", out)
	}
	// Only the system store is asked, and nothing is installed.
	if u.trust.nssChecks != 0 || u.trust.installs != 0 || u.trust.nssInstalls != 0 || len(u.trust.ensured) != 0 {
		t.Errorf("up did more than ask the system store: %+v", u.trust)
	}
}

func TestUpSaysNothingAboutATrustedCA(t *testing.T) {
	u := newUpRun(t, &config.Config{Projects: []config.Project{localProject("web", testutil.Upstream(t, "web"), true)}})
	u.start()
	u.waitFor("Serving until interrupted")
	u.get("web.test")
	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
	}
	if out := u.out.String(); strings.Contains(out, "trust") {
		t.Errorf("a trusted CA was mentioned:\n%s", out)
	}
}

// When the root cannot be read, whether it is trusted is unknown: up says
// nothing about it and serves as usual.
func TestUpRunsWhenTheTrustCheckFails(t *testing.T) {
	u := newUpRun(t, &config.Config{Projects: []config.Project{localProject("web", testutil.Upstream(t, "web"), true)}})
	u.trust.trusted = false
	u.trust.rootErr = errors.New("root.crt is not PEM")
	u.start()
	u.waitFor("Serving until interrupted")
	if got := u.get("web.test"); !strings.HasPrefix(got, "web:web.test") {
		t.Fatalf("served %q through the proxy", got)
	}
	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
	}
	if out := u.out.String(); strings.Contains(out, "trust") || strings.Contains(out, "root.crt") || !strings.HasSuffix(out, "\nstopped\n") {
		t.Errorf("unexpected output:\n%s", out)
	}
}

func TestUpStartsOnlyTheNamedProjects(t *testing.T) {
	u := newUpRun(t, &config.Config{Projects: []config.Project{
		localProject("web", testutil.Upstream(t, "web"), true),
		localProject("off", testutil.Upstream(t, "off"), false),
	}})
	u.noHosts = true
	u.start("off", "off")
	u.waitFor("Serving until interrupted")
	if got := u.get("off.test"); !strings.HasPrefix(got, "off:off.test") {
		t.Fatalf("served %q through the proxy", got)
	}
	if err := u.stop(); err != nil {
		t.Fatal(err)
	}
	out := u.out.String()
	if strings.Contains(out, "web") || strings.Count(out, "\noff: running at https://off.test\n") != 1 {
		t.Errorf("expected only the named project, once:\n%s", out)
	}
	if calls := u.ensureCalls(); len(calls) != 0 {
		t.Errorf("--no-hosts still called the hosts function: %v", calls)
	}
}

// With `nearhost up | tee up.log`, Ctrl-C ends tee as well and every later
// write fails. The shutdown must run to its end all the same.
func TestUpShutsDownCleanlyWhenItsOutputIsGone(t *testing.T) {
	u := newUpRun(t, &config.Config{Servers: mini, Projects: []config.Project{remoteProject("api")}})
	u.noHosts = true
	u.start()
	u.waitFor("Serving until interrupted")
	before := u.out.String()

	u.out.breakPipe()
	if err := u.stop(); err != nil { // also checks that no ssh process, listener or goroutine is left
		t.Errorf("up with its output gone returned %v, want a clean stop", err)
	}
	if out := u.out.String(); out != before {
		t.Errorf("the output was written to after it broke:\n%s", out[len(before):])
	}
	u.wantSSHEnded()
}

func TestUpRefusesBeforeStartingAnything(t *testing.T) {
	web := localProject("web", 8000, true)
	dup := localProject("dup", 8001, true)
	dup.Alias = "WEB.test"
	cases := []struct {
		name string
		cfg  *config.Config
		raw  string
		args []string
		want []string
	}{
		{"no projects", &config.Config{}, "", nil, []string{"There are no projects to start.", "nearhost add <path>"}},
		{"none enabled", &config.Config{Projects: []config.Project{localProject("off", 8000, false)}}, "", nil, []string{"There are no enabled projects to start.", "Start one by naming it: nearhost up off"}},
		{"none enabled, a name of two words", &config.Config{Projects: []config.Project{{
			Name: "my app", Server: config.LocalServer, Path: "/tmp/my app", Alias: "my-app.test", Routes: []config.Route{{Port: 8000, Role: config.RoleWeb}},
		}}}, "", nil, []string{"There are no enabled projects to start.", "nearhost up 'my app'"}},
		{"unknown name", &config.Config{Projects: []config.Project{web}}, "", []string{"web", "nope"}, []string{`There is no project called "nope".`, "List the projects in the config: nearhost ls"}},
		{"invalid yaml", nil, "projects: [oops\n", nil, []string{"parse", "config.yaml"}},
		{"invalid config", &config.Config{Projects: []config.Project{web, dup}}, "", nil, []string{"config.yaml", "alias", `"web"`, `"dup"`}},
		{"bad port", &config.Config{Projects: []config.Project{web}}, "", []string{"--https-port", "70000"}, []string{"--https-port"}},
		{"same ports", &config.Config{Projects: []config.Project{web}}, "", []string{"--https-port", "8443", "--http-port", "8443"}, []string{"must differ"}},
		{"bad web port", &config.Config{Projects: []config.Project{web}}, "", []string{"--web-port", "-1"}, []string{"--web-port", "0 for none"}},
		{"web port on the https port", &config.Config{Projects: []config.Project{web}}, "", []string{"--https-port", "8443", "--web-port", "8443"}, []string{"--web-port must differ"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfgPath := configHome(t)
			if tc.cfg != nil {
				saveConfig(t, cfgPath, tc.cfg)
			} else {
				if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(cfgPath, []byte(tc.raw), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			touched := func(what string) {
				t.Errorf("%s was reached although nothing may start", what)
			}
			b := upBackend{
				ensureHosts: func(context.Context, []string) error { touched("the hosts function"); return nil },
				notify: func(ctx context.Context) (context.Context, <-chan struct{}, func()) {
					touched("signal handling")
					return ctx, nil, func() {}
				},
			}
			out, err := execute(t, newUpCmd(b), append([]string{"up"}, tc.args...)...)
			if err == nil {
				t.Fatalf("up succeeded:\n%s", out)
			}
			for _, want := range tc.want {
				if !strings.Contains(errText(err), want) {
					t.Errorf("error %q lacks %q", errText(err), want)
				}
			}
			if out != "" {
				t.Errorf("printed %q before refusing", out)
			}
			if _, statErr := os.Stat(filepath.Join(filepath.Dir(cfgPath), "caddy")); statErr == nil {
				t.Error("the proxy storage was created")
			}
		})
	}
}

func TestUpRefusesWhenAProxyPortIsTaken(t *testing.T) {
	u := newUpRun(t, &config.Config{Projects: []config.Project{localProject("web", testutil.Upstream(t, "web"), true)}})
	// Held where the proxy binds: the wildcard address on Unix, loopback on
	// Windows, where a hold on the wildcard address does not stop a bind to
	// loopback.
	l, err := netbind.Listen("tcp", netbind.ServeAddrs(u.httpsPort)[0])
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	out, err := execute(t, newUpCmd(u.backend()), u.args()...)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("HTTPS port %d is in use", u.httpsPort)) {
		t.Fatalf("expected an error naming the port, got %v\n%s", err, out)
	}
	if strings.Contains(out, "state:") || len(u.ensureCalls()) != 0 {
		t.Errorf("a project was started although the proxy could not be:\n%s", out)
	}
	if portOpen(u.httpPort) {
		t.Error("the HTTP port was opened")
	}
}

func TestUpReportsAFailedProjectAndKeepsServingTheOthers(t *testing.T) {
	u := newUpRun(t, &config.Config{Servers: mini, Projects: []config.Project{
		remoteProject("api"),
		localProject("web", testutil.Upstream(t, "web"), true),
	}})
	u.sshEnv = []string{"FAKE_SSH_AUTH_FAIL=1"}
	u.start()
	u.waitFor("Serving until interrupted")
	out := u.out.String()
	// The refusal ends with when the server is tried next, in local time.
	const refused = `user@host: Permission denied \(publickey\)\. Nearhost tries again at \d\d:\d\d\.\n`
	for _, want := range []string{
		`\[server mini\] auth failed: ` + refused,
		`\napi: error: ssh mini\.lan: ` + refused +
			"  fix: To fix access, run ssh once in a terminal:\n        ssh mini\\.lan\n" +
			`web: running at https://web\.test\n` +
			`\nServing until interrupted\. Press Ctrl-C to stop\.\n`,
	} {
		if !regexp.MustCompile(want).MatchString(out) {
			t.Errorf("output does not match %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "  fix: ") != 1 {
		t.Errorf("the fix is printed more than once:\n%s", out)
	}
	if got := u.get("web.test"); !strings.HasPrefix(got, "web:web.test") {
		t.Fatalf("served %q through the proxy", got)
	}
	// One project serves, so up keeps going.
	u.stillRunning()
	if strings.Contains(u.out.String(), nothingServedLine) {
		t.Errorf("up said it was stopping while it served a project:\n%s", u.out.String())
	}
	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v", err)
	}
}

const nothingServedLine = "\nNothing can be served, so nearhost up is stopping.\n"

// wantStoppedForNothing checks that up, which has returned err, stopped
// because nothing could be served: the line after the summary, the same
// shutdown as Ctrl-C, and exit status 1 with nothing more said by Main.
func wantStoppedForNothing(t *testing.T, out string, err error) {
	t.Helper()
	var stderr bytes.Buffer
	if code := exitCode(err, &stderr); code != 1 || stderr.Len() != 0 {
		t.Errorf("up returned %v: exit status %d, Main said %q; want 1 and nothing", err, code, stderr.String())
	}
	if !strings.Contains(out, nothingServedLine+"shutting down\n") || !strings.HasSuffix(out, "\nstopped\n") {
		t.Errorf("expected the stopping line, then the shutdown:\n%s", out)
	}
	if strings.Contains(out, "Ctrl-C") {
		t.Errorf("up said to press Ctrl-C while stopping by itself:\n%s", out)
	}
}

// A project still connecting holds up until its start ends; and a start
// that met a refused login waits on for the session's own next attempt, so
// up keeps going then too. (Up stops when nothing can be served: see
// TestUpWithoutATerminalForSudoSaysSoOnce.)
func TestUpKeepsGoingWhileRefusedServersRetry(t *testing.T) {
	hold := filepath.Join(t.TempDir(), "hold")
	failAll := filepath.Join(t.TempDir(), "fail")
	for _, f := range []string{hold, failAll} {
		if err := os.WriteFile(f, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	web := remoteProject("web")
	web.Server = "other"
	u := newUpRun(t, &config.Config{
		Servers:  append([]config.Server{{Name: "other", SSHHost: "other.lan"}}, mini...),
		Projects: []config.Project{remoteProject("api"), web},
	})
	u.noHosts = true
	// api's server answers only once hold is gone, then refuses it; web's
	// refuses it at once.
	u.sshEnv = []string{"FAKE_SSH_AUTH_FAIL_HOST=mini.lan", "FAKE_SSH_AUTH_HOLD=" + hold, "FAKE_SSH_AUTH_FAIL_FILE=" + failAll}
	shutdowns := 0
	u.adjust = func(b *upBackend) {
		b.shutdown = func(eng *engine.Engine, ctx context.Context) error {
			shutdowns++
			return eng.Shutdown(ctx)
		}
	}
	u.start()
	u.waitFor("[web] state: error")
	// api is still connecting: its first attempt has not ended.
	time.Sleep(300 * time.Millisecond)
	u.stillRunning()
	if out := u.out.String(); strings.Contains(out, nothingServedLine) || strings.Contains(out, "shutting down") {
		t.Fatalf("up stopped while a project was still connecting:\n%s", out)
	}
	if err := os.Remove(hold); err != nil {
		t.Fatal(err)
	}
	// Both servers now refuse the login, and each session tries it again
	// by itself later, so each project's start waits on: up keeps going,
	// and the shutdown runs once, at the interrupt.
	u.waitFor("Press Ctrl-C to stop.\n")
	time.Sleep(300 * time.Millisecond)
	u.stillRunning()
	out := u.out.String()
	if strings.Contains(out, nothingServedLine) {
		t.Errorf("up stopped while refused servers would try again:\n%s", out)
	}
	summary := strings.Index(out, "\napi: error: ")
	if summary < 0 || !strings.Contains(out, "\nweb: error: ") {
		t.Errorf("the summary is missing:\n%s", out)
	}
	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
	}
	if shutdowns != 1 {
		t.Errorf("the shutdown ran %d times, want once", shutdowns)
	}
}

// A project whose server is reconnecting will serve again by itself, so up
// keeps going although nothing else runs.
func TestUpKeepsGoingWhileAProjectWaitsForItsServer(t *testing.T) {
	hold := filepath.Join(t.TempDir(), "hold")
	down := filepath.Join(t.TempDir(), "down")
	if err := os.WriteFile(hold, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	web := remoteProject("web")
	web.Server = "other"
	u := newUpRun(t, &config.Config{
		Servers:  append([]config.Server{{Name: "other", SSHHost: "other.lan"}}, mini...),
		Projects: []config.Project{remoteProject("api"), web},
	})
	u.noHosts = true
	// web's server answers only once hold is gone, then refuses it; only
	// api's server goes down.
	u.sshEnv = []string{"FAKE_SSH_AUTH_FAIL_HOST=other.lan", "FAKE_SSH_AUTH_HOLD=" + hold, "FAKE_SSH_MASTER_DOWN=" + down, "FAKE_SSH_MASTER_DOWN_HOST=mini.lan"}
	u.start()
	u.waitFor("[api] state: running")
	// api's server goes away and stays away: its session keeps reconnecting.
	if err := os.WriteFile(down, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	u.dropLink()
	u.waitFor("[api] state: degraded: server mini is ")
	if err := os.Remove(hold); err != nil {
		t.Fatal(err)
	}
	u.waitFor("Press Ctrl-C to stop.\n")
	wantContains(t, u.out.String(), "\napi: degraded: ", "\nweb: error: ")
	time.Sleep(300 * time.Millisecond)
	u.stillRunning()
	if out := u.out.String(); strings.Contains(out, nothingServedLine) {
		t.Errorf("up stopped while a project waited for its server:\n%s", out)
	}
	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
	}
}

// With no terminal to ask in, the hosts file's sudo prompt is skipped: up
// says so once, whatever the number of projects, and as nothing can be
// served, stops.
func TestUpWithoutATerminalForSudoSaysSoOnce(t *testing.T) {
	u := newUpRun(t, &config.Config{Projects: []config.Project{
		localProject("one", testutil.Upstream(t, "one"), true),
		localProject("two", testutil.Upstream(t, "two"), true),
	}})
	u.hostsErr = errNoTerminal
	u.start()
	err := u.exited()
	out := u.out.String()
	const warning = "Warning: The hosts file needs sudo, and there is no terminal to ask in.\n\n" +
		"Run it once in a terminal, where sudo can ask for your password:\n\n  nearhost up\n\n" +
		"Or serve without updating the hosts file:\n\n  nearhost up --no-hosts\n"
	if n := strings.Count(out, warning); n != 1 {
		t.Errorf("the no-terminal warning was printed %d times:\n%s", n, out)
	}
	// No prompt can appear, so no project is told to approve one.
	if strings.Contains(out, hostsApprove) {
		t.Errorf("up advised approving a prompt that cannot appear:\n%s", out)
	}
	wantStoppedForNothing(t, out, err)
}

func TestWaitingStates(t *testing.T) {
	retrying := engine.ServerHealth{Health: transport.Health{Status: transport.StatusReconnecting, NextAttempt: time.Now().Add(time.Second)}}
	for st, want := range map[engine.State]bool{
		engine.StateStarting: true,
		engine.StateDegraded: true,
		engine.StateRunning:  false,
		engine.StateError:    false,
		engine.StateStopped:  false,
	} {
		if got := waiting(engine.ProjectStatus{State: st}, retrying); got != want {
			t.Errorf("waiting(%s) = %v, want %v", st, got, want)
		}
	}
	// A start that waits for its server shows the error of a refused login:
	// it waits on while the session will try again, and not once the
	// session gave up for good or the server is held.
	refused := engine.ProjectStatus{State: engine.StateError, Waiting: "mini"}
	slow := engine.ServerHealth{Health: transport.Health{Status: transport.StatusAuthFailed, NextAttempt: time.Now().Add(15 * time.Minute), Refused: true}}
	gaveUp := engine.ServerHealth{Health: transport.Health{Status: transport.StatusAuthFailed}}
	held := engine.ServerHealth{Health: transport.Health{Status: transport.StatusDisconnected}, Held: true}
	for _, tc := range []struct {
		name   string
		health engine.ServerHealth
		want   bool
	}{
		{"slow retry", slow, true},
		{"reconnecting", retrying, true},
		{"gave up for good", gaveUp, false},
		{"held", held, false},
	} {
		if got := waiting(refused, tc.health); got != tc.want {
			t.Errorf("a waiting start whose server is %s: waiting = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestUpReportsAHostsFailureAndAsksOnlyOnce(t *testing.T) {
	u := newUpRun(t, &config.Config{Projects: []config.Project{
		localProject("one", testutil.Upstream(t, "one"), true),
		localProject("two", testutil.Upstream(t, "two"), true),
	}})
	u.hostsErr = errors.New("sudo hosts apply: exit status 1")
	u.start()
	// Neither project is served, and neither will be by itself.
	err := u.exited()
	out := u.out.String()
	wantStoppedForNothing(t, out, err)
	for _, name := range []string{"one", "two"} {
		wantContains(t, out,
			"\n"+name+": stopped: project \""+name+"\"'s hosts are not resolvable: the hosts file was not updated (",
			"sudo hosts apply: exit status 1")
	}
	if n := strings.Count(out, "  fix: Start it again and "+hostsApprove+":\n        nearhost up\n  fix: Or add the project's hosts to "+hosts.DefaultPath+" yourself, then serve without updating it:\n        nearhost up --no-hosts\n"); n != 2 {
		t.Errorf("expected the fix once under each project, found %d:\n%s", n, out)
	}
	if calls := u.ensureCalls(); len(calls) != 1 {
		t.Errorf("the hosts function was called %d times; a refusal must not be asked again", len(calls))
	}
}

func TestUpFollowsARemoteProjectThroughAReconnect(t *testing.T) {
	u := newUpRun(t, &config.Config{Servers: mini, Projects: []config.Project{remoteProject("api")}})
	u.noHosts = true
	u.start()
	u.waitFor("Serving until interrupted")
	wantContains(t, u.out.String(), "[server mini] connected\n", "[api] state: running\n", "\napi: running at https://api.test\n")

	// Counted before the connection is lost: the reconnect may be over by
	// the time the lines about the loss have been seen.
	before := strings.Count(u.out.String(), "[api] state: running\n")
	u.dropLink()
	u.waitFor("[api] state: degraded: server mini is reconnecting")
	u.waitFor("  fix: Nothing is needed while Nearhost reconnects. If the project stays degraded, fix access by running ssh once in a terminal:\n        ssh mini.lan\n")
	testutil.WaitUntil(t, 30*time.Second, func() bool {
		return strings.Count(u.out.String(), "[api] state: running\n") > before
	}, "the project to return to running")

	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
	}
	wantContains(t, u.out.String(), "[api] state: stopped\n")
}

func TestUpInterruptedWhileStartingShutsDownCleanly(t *testing.T) {
	u := newUpRun(t, &config.Config{Servers: mini, Projects: []config.Project{remoteProject("api")}})
	u.noHosts = true
	u.sshEnv = []string{"FAKE_SSH_HANG=1"}
	u.start()
	u.waitFor("[api] state: starting")
	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
	}
	out := u.out.String()
	if strings.Contains(out, "Ctrl-C") || strings.Contains(out, "fix:") || !strings.HasSuffix(out, "\nstopped\n") {
		t.Errorf("unexpected output:\n%s", out)
	}
}

// An interrupt ends every start in flight, so by the time up looks, its
// starts may all have returned as well: both are ready at once. The interrupt
// wins. Without that, up summed up the starts it had just interrupted, as
// errors with a fix, and said "Press Ctrl-C to stop" while shutting down.
func TestUpInterruptedAsTheStartsReturnPrintsNoSummary(t *testing.T) {
	cfg := &config.Config{Servers: mini, Projects: []config.Project{remoteProject("api")}}
	eng := engine.New(cfg, engine.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := make(chan struct{})
	close(started)
	// Go picks among ready cases at random, so each round is a fresh draw.
	for range 64 {
		var out bytes.Buffer
		p := &upPrinter{out: &out, eng: eng}
		p.untilInterrupted(ctx, nil, started, nil, []string{"api"}, []error{context.Canceled})
		if out.Len() != 0 || p.advise {
			t.Fatalf("an interrupted run was summed up:\n%s", out.String())
		}
	}
}

// Ctrl-C while sudo waits for a password: the hosts update is given up and
// up shuts down instead of waiting for the prompt to be answered.
func TestUpInterruptedDuringTheHostsUpdateShutsDownCleanly(t *testing.T) {
	u := newUpRun(t, &config.Config{Projects: []config.Project{
		localProject("one", testutil.Upstream(t, "one"), true),
		localProject("two", testutil.Upstream(t, "two"), true),
	}})
	u.hostsBlocks = true
	u.start()
	testutil.WaitUntil(t, 10*time.Second, func() bool { return len(u.ensureCalls()) > 0 }, "the hosts update to begin")
	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
	}
	out := u.out.String()
	if strings.Contains(out, "state: running") || strings.Contains(out, "Ctrl-C") || !strings.HasSuffix(out, "\nstopped\n") {
		t.Errorf("unexpected output:\n%s", out)
	}
}

func TestAskOnceRemembersARefusalButNotAnInterruption(t *testing.T) {
	calls := 0
	var result error
	ask := askOnce(func(ctx context.Context, _ []string) error { calls++; return result })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result = context.Canceled
	if err := ask(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted call returned %v", err)
	}
	result = nil
	if err := ask(context.Background(), nil); err != nil || calls != 2 {
		t.Fatalf("an interruption was remembered: err %v after %d calls", err, calls)
	}
	result = errors.New("sudo: a password is required")
	first := ask(context.Background(), nil)
	result = nil
	if again := ask(context.Background(), nil); first == nil || again != first || calls != 3 {
		t.Fatalf("a refusal must stand without asking again: %v then %v after %d calls", first, again, calls)
	}
}

// A start asked for after a refusal asks again; the starts asked for before
// it, and those that do not say when, get the refusal.
func TestAskOnceAsksAgainForAStartAskedForAfterTheRefusal(t *testing.T) {
	// A clock that moves on a second each time it is read.
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	askClock = func() time.Time { at = at.Add(time.Second); return at }
	t.Cleanup(func() { askClock = time.Now })
	calls := 0
	result := errors.New("you cancelled the password")
	ask := askOnce(func(context.Context, []string) error { calls++; return result })

	before := askedNow(context.Background())
	first := ask(askedNow(context.Background()), nil)
	if first == nil || calls != 1 {
		t.Fatalf("the first call: %v after %d calls", first, calls)
	}
	// Asked for with the first, and waiting behind it.
	if err := ask(before, nil); err != first || calls != 1 {
		t.Fatalf("a start asked for before the refusal: %v after %d calls", err, calls)
	}
	// One that does not say when, as `nearhost up` and the engine's own.
	if err := ask(context.Background(), nil); err != first || calls != 1 {
		t.Fatalf("a start that does not say when: %v after %d calls", err, calls)
	}
	result = nil
	if err := ask(askedNow(context.Background()), nil); err != nil || calls != 2 {
		t.Fatalf("a start asked for after the refusal: %v after %d calls", err, calls)
	}
	// The refusal is over for every start after one that went through.
	if err := ask(context.Background(), nil); err != nil || calls != 3 {
		t.Fatalf("after the retry went through: %v after %d calls", err, calls)
	}
}

// The engine filters the hosts file names through the lookup it is given,
// the one instance the interface and doctor share, and through none with
// --no-hosts, which writes no hosts file.
func TestEngineOptionsUseTheGivenLookup(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	calls := 0
	lookup := func(context.Context, string) ([]netip.Addr, error) { calls++; return nil, nil }
	opts, err := engineOptions(sshClient{}, serveFlags{}, func(context.Context, []string) error { return nil }, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Lookup == nil {
		t.Fatal("no lookup")
	}
	_, _ = opts.Lookup(context.Background(), "app.example.io")
	if calls != 1 {
		t.Errorf("the given lookup was called %d times", calls)
	}
	opts, err = engineOptions(sshClient{}, serveFlags{noHosts: true}, nil, lookup)
	if err != nil || opts.Lookup != nil {
		t.Errorf("--no-hosts: lookup set (%v)", err)
	}
}

// The proxy keeps exchanges unless --no-capture says not to.
func TestCaptureFollowsTheFlag(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, off := range []bool{false, true} {
		opts, err := engineOptions(sshClient{}, serveFlags{noCapture: off}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if opts.Capture == off {
			t.Errorf("--no-capture=%v: Capture = %v", off, opts.Capture)
		}
	}
	if newUpCmd(upBackend{}).Flags().Lookup("no-capture") == nil {
		t.Error("up has no --no-capture")
	}
}

// The proxy's page at localhost shows this build's version.
func TestEngineOptionsCarryTheVersion(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	opts, err := engineOptions(sshClient{}, serveFlags{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Version != Version {
		t.Errorf("Version = %q, want %q", opts.Version, Version)
	}
}
