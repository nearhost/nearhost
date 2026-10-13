package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/testutil"
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
		{"no projects", &config.Config{}, "", nil, []string{"no projects to start", "nearhost add <path>"}},
		{"none enabled", &config.Config{Projects: []config.Project{localProject("off", 8000, false)}}, "", nil, []string{"no enabled projects", "nearhost up off"}},
		{"unknown name", &config.Config{Projects: []config.Project{web}}, "", []string{"web", "nope"}, []string{`unknown project "nope"`, "nearhost ls"}},
		{"invalid yaml", nil, "projects: [oops\n", nil, []string{"parse", "config.yaml"}},
		{"invalid config", &config.Config{Projects: []config.Project{web, dup}}, "", nil, []string{"config.yaml", "alias", `"web"`, `"dup"`}},
		{"bad port", &config.Config{Projects: []config.Project{web}}, "", []string{"--https-port", "70000"}, []string{"--https-port"}},
		{"same ports", &config.Config{Projects: []config.Project{web}}, "", []string{"--https-port", "8443", "--http-port", "8443"}, []string{"must differ"}},
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
				notify: func(ctx context.Context) (context.Context, context.CancelFunc) {
					touched("signal handling")
					return context.WithCancel(ctx)
				},
			}
			out, err := execute(t, newUpCmd(b), append([]string{"up"}, tc.args...)...)
			if err == nil {
				t.Fatalf("up succeeded:\n%s", out)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q lacks %q", err, want)
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
	l, err := net.Listen("tcp", fmt.Sprintf(":%d", u.httpsPort))
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
	wantContains(t, out,
		"[server mini] auth failed: user@host: Permission denied (publickey).\n",
		"\napi: error: ssh mini.lan: user@host: Permission denied (publickey).\n"+
			"  fix: run `ssh mini.lan` once in a terminal to fix access, then run `nearhost up` again\n"+
			"web: running at https://web.test\n"+
			"\nServing until interrupted. Press Ctrl-C to stop.\n")
	if strings.Count(out, "  fix: ") != 1 {
		t.Errorf("the fix is printed more than once:\n%s", out)
	}
	if got := u.get("web.test"); !strings.HasPrefix(got, "web:web.test") {
		t.Fatalf("served %q through the proxy", got)
	}
	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v", err)
	}
}

func TestUpReportsAHostsFailureAndAsksOnlyOnce(t *testing.T) {
	u := newUpRun(t, &config.Config{Projects: []config.Project{
		localProject("one", testutil.Upstream(t, "one"), true),
		localProject("two", testutil.Upstream(t, "two"), true),
	}})
	u.hostsErr = errors.New("sudo hosts apply: exit status 1")
	u.start()
	u.waitFor("Nothing is being served. Press Ctrl-C to stop.\n")
	out := u.out.String()
	for _, name := range []string{"one", "two"} {
		wantContains(t, out,
			"\n"+name+": stopped: alias "+name+".test is not resolvable: the hosts file was not updated (",
			"sudo hosts apply: exit status 1")
	}
	if n := strings.Count(out, "  fix: run `nearhost up` again and approve the password prompt, or add the alias to /etc/hosts yourself and pass --no-hosts\n"); n != 2 {
		t.Errorf("expected the fix once under each project, found %d:\n%s", n, out)
	}
	if calls := u.ensureCalls(); len(calls) != 1 {
		t.Errorf("the hosts function was called %d times; a refusal must not be asked again", len(calls))
	}
	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v", err)
	}
}

func TestUpFollowsARemoteProjectThroughAReconnect(t *testing.T) {
	u := newUpRun(t, &config.Config{Servers: mini, Projects: []config.Project{remoteProject("api")}})
	u.noHosts = true
	u.start()
	u.waitFor("Serving until interrupted")
	wantContains(t, u.out.String(), "[server mini] connected\n", "[api] state: running\n", "\napi: running at https://api.test\n")

	// The master leaves when its socket disappears, as a lost connection.
	if err := os.Remove(filepath.Join(filepath.Dir(u.cfgPath), "run", "mini.sock")); err != nil {
		t.Fatal(err)
	}
	u.waitFor("[api] state: degraded: server mini is reconnecting")
	u.waitFor("  fix: none needed while nearhost reconnects; if the project stays degraded, run `ssh mini.lan` once in a terminal to fix access\n")
	before := strings.Count(u.out.String(), "[api] state: running\n")
	testutil.WaitUntil(t, 30*time.Second, func() bool {
		return strings.Count(u.out.String(), "[api] state: running\n") > before
	}, "the project to return to running")

	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
	}
	wantContains(t, u.out.String(), "[api] state: stopped\n")
}

func TestUpExitsNonZeroWhenShutdownFails(t *testing.T) {
	u := newUpRun(t, &config.Config{Servers: mini, Projects: []config.Project{remoteProject("api")}})
	u.noHosts = true
	flag := filepath.Join(t.TempDir(), "cancel-fails")
	if err := os.WriteFile(flag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	u.sshEnv = []string{"FAKE_SSH_CANCEL_FAIL=" + flag}
	u.start()
	u.waitFor("Serving until interrupted")
	err := u.stop()
	if err == nil || !strings.Contains(err.Error(), "shut down:") {
		t.Fatalf("expected the shutdown failure, got %v; output:\n%s", err, u.out.String())
	}
	if out := u.out.String(); strings.HasSuffix(out, "\nstopped\n") || strings.Contains(out, "fix:") {
		t.Errorf("claimed a clean stop, or advised on a failure of the shutdown:\n%s", out)
	}
}

// The ssh masters do not get the terminal's Ctrl-C, so a second one must not
// simply end the process: it cuts the shutdown short, and the masters are
// still ended before up returns.
func TestUpInterruptedAgainWhileShuttingDownStillEndsTheMasters(t *testing.T) {
	u := newUpRun(t, &config.Config{Servers: mini, Projects: []config.Project{remoteProject("api")}})
	u.noHosts = true
	wait := filepath.Join(t.TempDir(), "cancel-waits")
	if err := os.WriteFile(wait, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(wait) })
	u.sshEnv = []string{"FAKE_SSH_CANCEL_WAIT=" + wait}
	u.start()
	u.waitFor("Serving until interrupted")

	u.cancel() // the first signal: the shutdown now waits on the forward's cancel
	u.waitFor("shutting down")
	testutil.WaitUntil(t, 10*time.Second, func() bool {
		return strings.Contains(sshLog(t, filepath.Join(filepath.Dir(u.cfgPath), "ssh.log")), "-O cancel")
	}, "the shutdown to be cancelling the forward")
	u.interruptAgain()
	begin := time.Now()
	err := u.stop() // also checks that no ssh process, listener or goroutine is left
	if took := time.Since(begin); took > 15*time.Second {
		t.Errorf("up took %s to return after the second signal", took)
	}
	if err == nil || !strings.Contains(err.Error(), "shut down:") {
		t.Errorf("expected the cut-short shutdown to be reported, got %v", err)
	}
	out := u.out.String()
	if strings.Count(out, "\nstopping now\n") != 1 || strings.HasSuffix(out, "\nstopped\n") {
		t.Errorf("unexpected output:\n%s", out)
	}
	if log := sshLog(t, filepath.Join(filepath.Dir(u.cfgPath), "ssh.log")); !strings.Contains(log, "-O exit mini.lan") {
		t.Errorf("the master was not asked to exit:\n%s", log)
	}
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
