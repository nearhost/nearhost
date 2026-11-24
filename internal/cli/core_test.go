package cli

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/testutil"
	"github.com/nearhost/nearhost/internal/transport"
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
	if u.adjust != nil {
		u.adjust(&b)
	}
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
	st, err := u.control().State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Info != info || st.ConfigPath != u.cfgPath || st.HTTPSPort != u.httpsPort || st.HTTPPort != u.httpPort || st.WebPort != u.webPort {
		t.Errorf("State = %+v; want the core's info, config path %s and ports %d, %d and %d", st, u.cfgPath, u.httpsPort, u.httpPort, u.webPort)
	}
	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
	}
	// The socket goes with the core.
	if _, err := control.Dial(u.runDir()); err != control.ErrNotRunning {
		t.Errorf("after up returned, Dial = %v, want ErrNotRunning", err)
	}
}

// A core started with --no-capture says so in its state, so that a restart
// keeps it.
func TestCoreStateSaysNoCapture(t *testing.T) {
	for _, noCapture := range []bool{false, true} {
		t.Run(fmt.Sprint(noCapture), func(t *testing.T) {
			u := newUpRun(t, &config.Config{Projects: []config.Project{localProject("web", testutil.Upstream(t, "web"), true)}})
			u.noHosts = true
			var extra []string
			if noCapture {
				extra = append(extra, "--no-capture")
			}
			u.start(extra...)
			u.waitFor("Serving until interrupted")
			st, err := u.control().State(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if st.NoCapture != noCapture || !st.NoHosts {
				t.Errorf("NoCapture %v, NoHosts %v; want %v and true", st.NoCapture, st.NoHosts, noCapture)
			}
			if err := u.stop(); err != nil {
				t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
			}
		})
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
	// The engine says the config was applied; the watcher adds nothing.
	u.waitFor(" [proxy] config applied\n")
	if out := u.out.String(); strings.Contains(out, "reloaded") || strings.Count(out, "config applied") != 1 {
		t.Errorf("the reload was not reported once, as the engine's event:\n%s", out)
	}
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
	if out := u.out.String(); strings.Contains(out, "[b]") || strings.Contains(out, "config applied") {
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
	// It is an event line like any other, printed by the printer.
	if !eventLine.MatchString(line) {
		t.Errorf("the line is not an event line: %q", line)
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

// eventLine is the start of a line the printer prints for an event.
var eventLine = regexp.MustCompile(`^\d\d:\d\d:\d\d \[proxy\] `)

// A project the reload adds gets the same fix as one up started: the
// printer knows it from the engine's config, not the one up started with.
func TestUpHintsForAProjectTheReloadAdds(t *testing.T) {
	fastWatch(t)
	cfg := &config.Config{Projects: []config.Project{localProject("a", testutil.Upstream(t, "a"), true)}}
	u := newUpRun(t, cfg)
	u.start()
	u.waitFor("Serving until interrupted")
	u.mu.Lock()
	u.hostsErr = errors.New("sudo: a password is required")
	u.mu.Unlock()
	cfg.Projects = append(cfg.Projects, localProject("c", testutil.Upstream(t, "c"), true))
	saveConfig(t, u.cfgPath, cfg)
	u.waitFor("[c] state: stopped: ")
	u.waitFor("  fix: Run `nearhost up` again and " + hostsApprove)
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
	u.waitFor("\nNothing is being served.\n")
	time.Sleep(200 * time.Millisecond)
	u.stillRunning()
	if out := u.out.String(); strings.Contains(out, nothingServedLine) || strings.Contains(out, "Ctrl-C") {
		t.Errorf("a core that keeps running said it was stopping, or to press Ctrl-C:\n%s", out)
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

// The state lists every project and server, the config the engine runs and
// the file it was read from.
func TestCoreStateListsEverything(t *testing.T) {
	cfg := &config.Config{Servers: mini, Projects: []config.Project{localProject("web", testutil.Upstream(t, "web"), true)}}
	u := newUpRun(t, cfg)
	u.noHosts = true
	u.start()
	u.waitFor("Serving until interrupted")
	st, err := u.control().State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := st.Projects["web"]; !ok || p.State != engine.StateRunning {
		t.Errorf("Projects = %+v, want web running", st.Projects)
	}
	if _, ok := st.Servers[config.LocalServer]; !ok {
		t.Errorf("Servers = %+v, want this machine", st.Servers)
	}
	if s, ok := st.Servers["mini"]; !ok || s.Status != transport.StatusDisconnected {
		t.Errorf("Servers = %+v, want mini, not connected", st.Servers)
	}
	got, err := config.Parse([]byte(st.Config))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Projects) != 1 || got.Projects[0].Name != "web" || len(got.Servers) != 1 || got.Servers[0].Name != "mini" {
		t.Errorf("Config = %s", st.Config)
	}
	if st.ConfigPath != u.cfgPath {
		t.Errorf("ConfigPath = %s, want %s", st.ConfigPath, u.cfgPath)
	}
	// This run serves no agents, so it has no grants to read.
	if st.Grants == nil || len(st.Grants) != 0 || st.GrantsError == "" {
		t.Errorf("Grants = %v, GrantsError = %q", st.Grants, st.GrantsError)
	}
	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
	}
}

// engineOf has the up the test runs give its engine to got once it starts.
func (u *upRun) engineOf(got chan<- *engine.Engine) { u.adjust = giveEngine(got) }

// giveEngine is an adjust that has up give its engine to got once it
// starts.
func giveEngine(got chan<- *engine.Engine) func(*upBackend) {
	return func(b *upBackend) {
		start := b.start
		b.start = func(eng *engine.Engine, ctx context.Context) error {
			got <- eng
			return start(eng, ctx)
		}
	}
}

// nextEvent returns the next envelope on envs that carries an event whose
// message has the prefix prefix.
func nextEvent(t *testing.T, envs <-chan control.Envelope, prefix string) control.Envelope {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case env, ok := <-envs:
			if !ok {
				t.Fatalf("the stream ended before an event %q", prefix)
			}
			if env.Event != nil && strings.HasPrefix(env.Event.Message, prefix) {
				return env
			}
		case <-timeout:
			t.Fatalf("no event %q on the stream", prefix)
		}
	}
}

// What the engine publishes reaches the clients' stream, and a message too
// long for it is cut, so the stream goes on.
func TestCoreStreamsEngineEvents(t *testing.T) {
	u := newUpRun(t, &config.Config{Projects: []config.Project{localProject("web", testutil.Upstream(t, "web"), true)}})
	u.noHosts = true
	engs := make(chan *engine.Engine, 1)
	u.engineOf(engs)
	u.start()
	u.waitFor("Serving until interrupted")
	eng := <-engs
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	envs, _, err := u.control().Events(ctx, u.hubSeq(t))
	if err != nil {
		t.Fatal(err)
	}
	eng.Events().Publish(engine.Event{Project: "web", State: engine.StateRunning, Message: "hello from the engine"})
	env := nextEvent(t, envs, "hello")
	if env.Kind != control.KindEvent || env.Event.Project != "web" || env.Event.State != engine.StateRunning || env.Event.Message != "hello from the engine" {
		t.Errorf("envelope %+v, event %+v", env, *env.Event)
	}
	// Longer than a client reads in one envelope.
	eng.Events().Publish(engine.Event{Project: "web", Message: "long " + strings.Repeat("x", 2<<20)})
	long := nextEvent(t, envs, "long ")
	if n := len(long.Event.Message); n > maxEventMessage || !strings.HasSuffix(long.Event.Message, "…") {
		t.Errorf("a long message arrived as %d bytes", n)
	}
	eng.Events().Publish(engine.Event{Message: "after the long one"})
	nextEvent(t, envs, "after the long one")
	cancel()
	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
	}
}

// hubSeq is the newest envelope's number, which a stream opened after it
// starts from.
func (u *upRun) hubSeq(t *testing.T) uint64 {
	t.Helper()
	st, err := u.control().State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return st.Seq
}

// Reload applies the file before it returns, without waiting for the
// watcher.
func TestCoreReloadAppliesTheFile(t *testing.T) {
	before := coreWatchInterval
	coreWatchInterval = time.Hour // only the Reload applies
	t.Cleanup(func() { coreWatchInterval = before })
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
	c := u.control()
	if err := c.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, err := c.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	running, err := config.Parse([]byte(st.Config))
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := running.Project("b"); !ok || !p.Enabled {
		t.Fatalf("after Reload the engine runs %s", st.Config)
	}
	u.waitFor("[b] state: running")
	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
	}
}

// A start asked for through the core after the hosts file was refused asks
// for it again, as one asked for in the interface does.
func TestCoreStartProjectAsksAgain(t *testing.T) {
	u := newUpRun(t, &config.Config{Projects: []config.Project{localProject("one", testutil.Upstream(t, "one"), true)}})
	u.hostsErr = errors.New("sudo: a password is required")
	u.startCore(func(cmd *cobra.Command) coreMode {
		return coreMode{out: cmd.OutOrStdout(), startedBy: control.StartedDetached, keepRunning: true}
	})
	u.waitFor("[one] state: stopped: ")
	u.mu.Lock()
	u.hostsErr = nil
	u.mu.Unlock()
	if err := u.control().StartProject(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	u.waitFor("[one] state: running")
	if n := len(u.ensureCalls()); n != 2 {
		t.Errorf("the hosts file was asked for %d times, want 2", n)
	}
	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
	}
}

// With its page served, the core reviews what agents ask with no client
// attached: the 202 links to the request's approval on the page, and up
// says where to answer it.
func TestCoreReviewsWithThePage(t *testing.T) {
	engines := make(chan *engine.Engine, 1)
	u, sock := agentUp(t, func(u *upRun) { u.engineOf(engines) })
	eng := <-engines
	page, err := eng.PageURL()
	if err != nil {
		t.Fatal(err)
	}
	if got := interfaceOn(t, sock); got != true {
		t.Errorf("with the page and no client: interface %v", got)
	}
	status, out := askToShow(t, sock)
	if status != 202 {
		t.Fatalf("document request: %d %v", status, out)
	}
	id := out["id"].(string)
	if link, _ := out["approve_url"].(string); link != page+"/approve/"+id {
		t.Errorf("approve_url %q, want %s/approve/%s", link, page, id)
	}
	u.waitFor("Claude Code on this machine asks to show docs/p.md. Answer at " + page + "/approve/" + id + "\n")
	st, err := u.control().State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Approvals) != 1 || st.Approvals[0].ID != id {
		t.Errorf("approvals %+v, want %s", st.Approvals, id)
	}
	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
	}
}

// Without its page, the core reviews what agents ask while a client follows
// its stream, and only then.
func TestCoreWithoutPageReviewsOnlyWhileAttached(t *testing.T) {
	u, sock := agentUp(t, func(u *upRun) { u.webPort = 0 })
	if got := interfaceOn(t, sock); got != false {
		t.Fatalf("before any client: interface %v", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, streamErr, err := u.control().Events(ctx, u.hubSeq(t))
	if err != nil {
		t.Fatal(err)
	}
	testutil.WaitUntil(t, 5*time.Second, func() bool { return interfaceOn(t, sock) == true }, "the agent API to say a client reviews")
	cancel()
	if err := streamErr(); err != nil {
		t.Fatal(err)
	}
	testutil.WaitUntil(t, 5*time.Second, func() bool { return interfaceOn(t, sock) == false }, "the agent API to say no client reviews")
	want := "Nearhost on dev@laptop has no page or window to ask you in; open nearhost there, then ask again."
	if status, out := askToShow(t, sock); status != 409 || out["error"] != want {
		t.Errorf("a request with no client: %d %v, want 409 %q", status, out, want)
	}
	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
	}
}

// gatedWriter writes to out, but while held every write waits for the
// release, as a terminal whose output is paused does.
type gatedWriter struct {
	out  io.Writer
	mu   sync.Mutex
	gate chan struct{} // nil while open
}

func (w *gatedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	g := w.gate
	w.mu.Unlock()
	if g != nil {
		<-g
	}
	return w.out.Write(p)
}

func (w *gatedWriter) hold() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.gate = make(chan struct{})
}

func (w *gatedWriter) release() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.gate != nil {
		close(w.gate)
		w.gate = nil
	}
}

// Output that blocks holds up neither the agents nor the core: requests
// still get their 202 at once, and their lines are written once the output
// takes them again.
func TestApprovalLinesDoNotWaitForTheOutput(t *testing.T) {
	home := agentHome(t)
	u := newUpRun(t, &config.Config{Projects: []config.Project{localProject("web", testutil.Upstream(t, "web"), true)}})
	u.noHosts = true
	u.adjust = func(b *upBackend) { b.agents = withGrants(fakeAgents(home), home) }
	out := &gatedWriter{out: &u.out}
	u.startCore(func(cmd *cobra.Command) coreMode {
		return coreMode{out: out, startedBy: control.StartedForeground}
	})
	u.waitFor("Serving until interrupted")
	sock := agentSocket(home, "k3f9q2")
	repo := gitRepo(t)
	writeFile(t, filepath.Join(repo, "docs", "q.md"), "# Notes\n")
	out.hold()
	t.Cleanup(out.release)
	type answer struct {
		status int
		id     string
		err    error
	}
	ask := func(doc, session string) answer {
		body, _ := json.Marshal(map[string]string{"path": filepath.Join(repo, "docs", doc), "root": repo, "requester": "claude-code", "session": session})
		resp, err := agentClient(transport.AgentForward{Local: sock}).Post("http://nearhost/v1/documents", "application/json", bytes.NewReader(body))
		if err != nil {
			return answer{err: err}
		}
		defer resp.Body.Close()
		var got struct{ ID string }
		err = json.NewDecoder(resp.Body).Decode(&got)
		return answer{status: resp.StatusCode, id: got.ID, err: err}
	}
	var ids []string
	for i, doc := range []string{"p.md", "q.md"} {
		answered := make(chan answer, 1)
		go func() { answered <- ask(doc, fmt.Sprintf("s%d", i)) }()
		select {
		case a := <-answered:
			if a.err != nil || a.status != 202 {
				t.Fatalf("request for %s while the output is held: %d %v", doc, a.status, a.err)
			}
			ids = append(ids, a.id)
		case <-time.After(2 * time.Second):
			t.Fatalf("the request for %s waited for the output", doc)
		}
	}
	if strings.Contains(u.out.String(), "asks to show") {
		t.Fatalf("a line was written while the output was held:\n%s", u.out.String())
	}
	out.release()
	u.waitFor("asks to show docs/p.md. Answer at ")
	u.waitFor("asks to show docs/q.md. Answer at ")
	for _, id := range ids {
		u.waitFor("/approve/" + id + "\n")
	}
	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
	}
}

// With its page served, the core takes an answer given on the page: a Yes
// posted through the proxy, with the page's cookie, origin and token,
// opens the document the agent asked to show.
func TestCorePageAnswersAnAgent(t *testing.T) {
	engines := make(chan *engine.Engine, 1)
	opened := make(chan string, 1)
	u, sock := agentUp(t, func(u *upRun) {
		u.adjust = func(b *upBackend) {
			giveEngine(engines)(b)
			b.agents.open = func(url string) error {
				opened <- url
				return nil
			}
		}
	})
	eng := <-engines
	base, err := eng.PageURL()
	if err != nil {
		t.Fatal(err)
	}
	status, out := askToShow(t, sock)
	if status != 202 {
		t.Fatalf("document request: %d %v", status, out)
	}
	id := out["id"].(string)

	c := pageBrowser(t, eng.WebPort())
	keyed, err := proxy.PageKeyURL(base)
	if err != nil {
		t.Fatal(err)
	}
	var view string
	testutil.WaitUntil(t, 10*time.Second, func() bool {
		resp, err := c.Get(keyed + "&next=/approve/" + id)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		view = string(b)
		return resp.StatusCode == 200 && strings.Contains(view, "docs/p.md")
	}, "the approval on the page")
	m := regexp.MustCompile(`name="csrf" value="([0-9a-f]+)"`).FindStringSubmatch(view)
	if m == nil {
		t.Fatalf("the approval has no token:\n%s", view)
	}
	form := url.Values{"csrf": {m[1]}, "answer": {"yes"}}
	req, err := http.NewRequest("POST", base+"/approve/"+id, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", base)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "Answered: Yes.") {
		t.Fatalf("POST /approve/%s: %d\n%s", id, resp.StatusCode, b)
	}
	doc := getJSON(t, sock, "/v1/documents/"+id+"?wait=1")
	if doc["state"] != "open" {
		t.Fatalf("the agent was told %v", doc)
	}
	select {
	case got := <-opened:
		if got != doc["url"] {
			t.Errorf("opened %s, want %v", got, doc["url"])
		}
	case <-time.After(5 * time.Second):
		t.Error("the document was not opened")
	}
	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
	}
}

// pageBrowser is a browser of Nearhost's page on port: it keeps cookies,
// and dials 127.0.0.1 for nearhost.localhost. It does not check the page's
// certificate, which the test's own CA signed.
func pageBrowser(t *testing.T, port int) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	tr := &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, fmt.Sprintf("127.0.0.1:%d", port))
		},
	}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Jar: jar, Timeout: 30 * time.Second}
}

// fakeUpdateWatch is a watch that finds release 9.9.9 at once, and waits an
// hour for each check after it.
func fakeUpdateWatch(publish func(engine.Event)) *updateWatch {
	return &updateWatch{
		current: "0.15.0", publish: publish, first: 0, every: time.Hour,
		latest: func(context.Context) (string, error) { return "9.9.9", nil },
	}
}

// The core's update watch says the release on the events' stream, reports it
// in its status, and ends with the core.
func TestCoreRunsTheUpdateWatch(t *testing.T) {
	u := newUpRun(t, &config.Config{Projects: []config.Project{localProject("web", testutil.Upstream(t, "web"), true)}})
	u.noHosts = true
	u.adjust = func(b *upBackend) { b.updates = fakeUpdateWatch }
	u.start()
	u.waitFor("Serving until interrupted")
	envs, _, err := u.control().Events(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	nextEvent(t, envs, "A new nearhost is available: 0.15.0 → 9.9.9.")
	st, err := u.control().Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Update != "9.9.9" {
		t.Errorf("Status.Update = %q, want 9.9.9", st.Update)
	}
	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
	}
	if n := strings.Count(u.out.String(), "A new nearhost is available"); n != 1 {
		t.Errorf("the notice is printed %d times, want once:\n%s", n, u.out.String())
	}
}

// The production backend makes no watch when updates are switched off, and
// the test binary's development version never has one.
func TestCoreRunsNoUpdateWatchByDefault(t *testing.T) {
	t.Setenv("NEARHOST_NO_UPDATE_CHECK", "1")
	if w := systemUp(nil).updates(func(engine.Event) {}); w != nil {
		t.Error("a watch is made with NEARHOST_NO_UPDATE_CHECK set")
	}
}

// A core that cannot serve its control socket returns at once, whatever its
// update watch is waiting for.
func TestCoreWithBusySocketDoesNotWaitForTheUpdateWatch(t *testing.T) {
	u := newUpRun(t, &config.Config{Projects: []config.Project{localProject("web", testutil.Upstream(t, "web"), true)}})
	u.noHosts = true
	u.adjust = func(b *upBackend) {
		b.updates = fakeUpdateWatch
		start := b.start
		b.start = func(eng *engine.Engine, ctx context.Context) error {
			if err := start(eng, ctx); err != nil {
				return err
			}
			// Another core answers on the socket by the time this one
			// listens: the engine clears stale sockets as it starts.
			srv, err := control.Listen(control.SocketPath(u.runDir()), infoCore{}, control.NewHub())
			if err != nil {
				return err
			}
			t.Cleanup(func() { srv.Close() })
			return nil
		}
	}
	u.start()
	done := make(chan error, 1)
	go func() { done <- u.exited() }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "Another Nearhost answers") {
			t.Errorf("up returned %v, want a refusal for the busy socket", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("up waits for the update watch after the control socket failed")
	}
}
