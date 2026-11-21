package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/testutil"
)

// runDir is the run directory of the up the test runs, where its control
// socket listens.
func (u *upRun) runDir() string { return filepath.Join(filepath.Dir(u.cfgPath), "run") }

// control dials the control socket of the up the test runs.
func (u *upRun) control() *control.Client {
	u.t.Helper()
	c, err := control.Dial(u.runDir())
	if err != nil {
		u.t.Fatalf("dial the control socket: %v; output:\n%s", err, u.out.String())
	}
	return c
}

// startCore runs the core as up does, but in the mode mode gives it, until
// the test ends or stop is called.
func (u *upRun) startCore(mode func(cmd *cobra.Command) coreMode, extra ...string) {
	u.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	u.cancel = cancel
	u.done = make(chan error, 1)
	b := u.backend()
	var flags serveFlags
	cmd := &cobra.Command{Use: "up", RunE: func(cmd *cobra.Command, args []string) error {
		return runCore(cmd, b, flags, args, mode(cmd))
	}}
	flags.bind(cmd)
	flags.bindCapture(cmd)
	go func() { u.done <- executeInto(ctx, &u.out, cmd, u.args(extra...)...) }()
	u.t.Cleanup(func() {
		if u.done != nil {
			u.stop()
		}
	})
}

// fastWatch makes the config watcher poll quickly for the rest of the test.
func fastWatch(t *testing.T) {
	t.Helper()
	before := coreWatchInterval
	coreWatchInterval = 50 * time.Millisecond
	t.Cleanup(func() { coreWatchInterval = before })
}

func TestUpAnswersTheControlSocket(t *testing.T) {
	u := newUpRun(t, &config.Config{Projects: []config.Project{localProject("web", testutil.Upstream(t, "web"), true)}})
	u.noHosts = true
	u.start()
	u.waitFor("Serving until interrupted")
	info, err := u.control().Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.API != control.APIVersion || info.StartedBy != control.StartedForeground || info.Version != Version || info.PID != os.Getpid() || !info.Reloads {
		t.Errorf("Info = %+v; want API %d, started by foreground, version %q, pid %d and reloads", info, control.APIVersion, Version, os.Getpid())
	}
	if info.Started.IsZero() || time.Since(info.Started) > time.Minute {
		t.Errorf("Started = %v", info.Started)
	}
	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
	}
	// The socket goes with the core.
	if _, err := control.Dial(u.runDir()); err != control.ErrNotRunning {
		t.Errorf("after up returned, Dial = %v, want ErrNotRunning", err)
	}
}

func TestUpStatusListsProjects(t *testing.T) {
	u := newUpRun(t, &config.Config{Projects: []config.Project{
		localProject("web", testutil.Upstream(t, "web"), true),
		localProject("api", testutil.Upstream(t, "api"), true),
		localProject("off", testutil.Upstream(t, "off"), false),
	}})
	u.noHosts = true
	u.start()
	u.waitFor("Serving until interrupted")
	st, err := u.control().Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Info.StartedBy != control.StartedForeground {
		t.Errorf("Status().Info = %+v", st.Info)
	}
	want := map[string]control.ProjectInfo{}
	for _, name := range []string{"web", "api"} {
		want[name] = control.ProjectInfo{
			Name: name, State: string(engine.StateRunning),
			URL: fmt.Sprintf("https://%s.test:%d", name, u.httpsPort),
		}
	}
	if len(st.Projects) != len(want) {
		t.Errorf("Projects = %+v, want web and api", st.Projects)
	}
	for _, p := range st.Projects {
		if w, ok := want[p.Name]; !ok || p != w {
			t.Errorf("project %+v, want %+v", p, w)
		}
	}
	// This machine is a server too, as the interface lists it.
	if len(st.Servers) != 1 || st.Servers[0] != (control.ServerInfo{Name: config.LocalServer, State: "connected"}) {
		t.Errorf("Servers = %+v", st.Servers)
	}
	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
	}
}

func TestUpStopsOnControlStop(t *testing.T) {
	u := newUpRun(t, &config.Config{Servers: mini, Projects: []config.Project{remoteProject("api")}})
	u.noHosts = true
	u.start()
	u.waitFor("Serving until interrupted")
	if err := u.control().Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := u.exited(); err != nil {
		t.Fatalf("up returned %v, want nil; output:\n%s", err, u.out.String())
	}
	out := u.out.String()
	wantContains(t, out, "\nshutting down\n", "[api] state: stopped\n")
	if !strings.HasSuffix(out, "\nstopped\n") {
		t.Errorf("output does not end with the stopped line:\n%s", out)
	}
	u.wantSSHEnded()
}

// A Stop that arrives while the projects are still starting ends up as an
// interrupt then would.
func TestUpStopsOnControlStopWhileStarting(t *testing.T) {
	u := newUpRun(t, &config.Config{Servers: mini, Projects: []config.Project{remoteProject("api")}})
	u.noHosts = true
	u.sshEnv = []string{"FAKE_SSH_HANG=1"}
	u.start()
	u.waitFor("[api] state: starting")
	if err := u.control().Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := u.exited(); err != nil {
		t.Fatalf("up returned %v, want nil; output:\n%s", err, u.out.String())
	}
	if out := u.out.String(); strings.Contains(out, "Ctrl-C") || strings.Contains(out, "fix:") || !strings.HasSuffix(out, "\nstopped\n") {
		t.Errorf("unexpected output:\n%s", out)
	}
}

func TestUpReloadsTheConfig(t *testing.T) {
	fastWatch(t)
	cfg := &config.Config{Projects: []config.Project{
		localProject("a", testutil.Upstream(t, "a"), true),
		localProject("b", testutil.Upstream(t, "b"), false),
	}}
	u := newUpRun(t, cfg)
	u.noHosts = true
	u.start()
	u.waitFor("Serving until interrupted")
	cfg.Projects[1].Enabled = true
	saveConfig(t, u.cfgPath, cfg)
	u.waitFor("[b] state: running")
	u.waitFor("config reloaded\n")
	if got := u.get("b.test"); !strings.HasPrefix(got, "b:b.test") {
		t.Fatalf("served %q through the proxy", got)
	}
	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
	}
	wantContains(t, u.out.String(), "[b] state: stopped\n", "[a] state: stopped\n")
}

func TestUpWithNamedProjectsDoesNotReload(t *testing.T) {
	fastWatch(t)
	cfg := &config.Config{Projects: []config.Project{
		localProject("a", testutil.Upstream(t, "a"), true),
		localProject("b", testutil.Upstream(t, "b"), false),
	}}
	u := newUpRun(t, cfg)
	u.noHosts = true
	u.start("a")
	u.waitFor("Serving until interrupted")
	info, err := u.control().Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Reloads {
		t.Error("Info().Reloads is true for up with named projects")
	}
	cfg.Projects[1].Enabled = true
	saveConfig(t, u.cfgPath, cfg)
	time.Sleep(500 * time.Millisecond) // ten polls of a watcher, were there one
	if out := u.out.String(); strings.Contains(out, "[b]") || strings.Contains(out, "reloaded") {
		t.Errorf("up a applied the changed config:\n%s", out)
	}
	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
	}
}

func TestUpLogsAnInvalidConfigAndKeepsServing(t *testing.T) {
	fastWatch(t)
	u := newUpRun(t, &config.Config{Projects: []config.Project{localProject("a", testutil.Upstream(t, "a"), true)}})
	u.noHosts = true
	u.start()
	u.waitFor("Serving until interrupted")
	if err := os.WriteFile(u.cfgPath, []byte("projects: [oops\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const kept = "; still serving the last good config\n"
	u.waitFor(kept)
	time.Sleep(300 * time.Millisecond) // the watcher reports a change once
	out := u.out.String()
	if n := strings.Count(out, kept); n != 1 {
		t.Errorf("the bad config was reported %d times:\n%s", n, out)
	}
	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasSuffix(l+"\n", kept) {
			line = l
		}
	}
	if !strings.Contains(line, u.cfgPath) || !strings.Contains(line, "parse") || strings.Count(line, u.cfgPath) != 1 {
		t.Errorf("the line does not name the file once and the error: %q", line)
	}
	if got := u.get("a.test"); !strings.HasPrefix(got, "a:a.test") {
		t.Fatalf("served %q through the proxy", got)
	}
	st, err := u.control().Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Projects) != 1 || st.Projects[0].State != string(engine.StateRunning) {
		t.Errorf("Projects = %+v, want a running", st.Projects)
	}
	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
	}
}

// A core that keeps running outlives a first round in which the hosts file
// stopped every project, and HostsChanged starts them again.
func TestCoreHostsChangedRetriesTheProjectsTheHostsFileStopped(t *testing.T) {
	u := newUpRun(t, &config.Config{Projects: []config.Project{localProject("one", testutil.Upstream(t, "one"), true)}})
	var refuse atomic.Bool
	refuse.Store(true)
	calls := 0
	u.startCore(func(cmd *cobra.Command) coreMode {
		return coreMode{
			out: cmd.OutOrStdout(), startedBy: control.StartedDetached, keepRunning: true,
			hosts: func(context.Context, []string) error {
				calls++ // the engine never makes two calls at once
				if refuse.Load() {
					return errors.New("the hosts file needs updating")
				}
				return nil
			},
		}
	})
	u.waitFor("[one] state: stopped: ")
	u.waitFor("Press Ctrl-C to stop.\n")
	time.Sleep(200 * time.Millisecond)
	u.stillRunning()
	if out := u.out.String(); strings.Contains(out, nothingServedLine) {
		t.Errorf("a core that keeps running said it was stopping:\n%s", out)
	}
	c := u.control()
	if info, err := c.Info(context.Background()); err != nil || info.StartedBy != control.StartedDetached || info.Reloads {
		t.Errorf("Info = %+v, %v", info, err)
	}
	refuse.Store(false)
	if err := c.HostsChanged(context.Background()); err != nil {
		t.Fatal(err)
	}
	u.waitFor("[one] state: running")
	if got := u.get("one.test"); !strings.HasPrefix(got, "one:one.test") {
		t.Fatalf("served %q through the proxy", got)
	}
	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
	}
	if calls != 2 {
		t.Errorf("the hosts function was called %d times, want 2", calls)
	}
	if len(u.ensureCalls()) != 0 {
		t.Error("the backend's hosts function was called in place of the mode's")
	}
}
