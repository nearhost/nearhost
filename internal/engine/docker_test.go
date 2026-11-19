package engine

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/testutil"
)

// fakeDocker stands in for Options.DockerRanges. Each call takes the next of
// answers, and the last one once they run out; an answer with err set fails.
type fakeDocker struct {
	mu      sync.Mutex
	answers []dockerAnswer
	calls   int
}

type dockerAnswer struct {
	ranges []string
	err    error
}

func (f *fakeDocker) ranges(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.answers[min(f.calls, len(f.answers)-1)]
	f.calls++
	return slices.Clone(a.ranges), a.err
}

func (f *fakeDocker) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// optedIn is a local project whose approved .nearhostrc asks to be served to
// Docker networks.
func optedIn(t *testing.T, name string) config.Project {
	p := localProject(name, upstream(t, name))
	p.Nearhostrc = &config.RcSnapshot{SHA256: "h", DockerClients: true}
	return p
}

// dockerHarness starts an engine whose Docker is f, refreshed every period
// (0 for the default), with the given projects.
func dockerHarness(t *testing.T, f *fakeDocker, period time.Duration, projects ...config.Project) *harness {
	t.Helper()
	h := buildHarness(t, &config.Config{Projects: projects}, func(o *Options) {
		o.DockerRanges = f.ranges
		o.DockerRefresh = period
	})
	if err := h.eng.Start(bg); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return h
}

// clientsOf returns the Clients of every published site of the project with
// the given alias, one entry per site.
func clientsOf(h *harness, alias string) [][]string {
	var out [][]string
	for _, s := range h.eng.Sites() {
		if s.Alias == alias {
			out = append(out, s.Clients)
		}
	}
	return out
}

// waitClients waits for every site of alias to carry exactly want.
func waitClients(t *testing.T, h *harness, alias string, want []string) {
	t.Helper()
	testutil.WaitUntil(t, 2*time.Second, func() bool {
		got := clientsOf(h, alias)
		if len(got) == 0 {
			return false
		}
		for _, c := range got {
			if !slices.Equal(c, want) {
				return false
			}
		}
		return true
	}, alias+" to be served to "+strings.Join(want, ", "))
}

// waitLog waits for a log line of project carrying exactly message.
func waitLog(t *testing.T, events <-chan Event, project, message string) {
	t.Helper()
	var seen []string
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Project != project || ev.State != "" {
				continue
			}
			if ev.Message == message {
				return
			}
			seen = append(seen, ev.Message)
		case <-deadline:
			t.Fatalf("no log line %q for %s; got %q", message, project, seen)
		}
	}
}

// recordRefreshes wraps the engine's refresh apply and returns the sites of
// every rebuild it made.
func recordRefreshes(h *harness) func() [][]proxy.Site {
	var mu sync.Mutex
	var applied [][]proxy.Site
	apply := h.eng.refreshSites
	h.eng.refreshSites = func(sites []proxy.Site) error {
		mu.Lock()
		applied = append(applied, sites)
		mu.Unlock()
		return apply(sites)
	}
	return func() [][]proxy.Site {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(applied)
	}
}

func TestOptedInProjectIsServedToDockerRanges(t *testing.T) {
	f := &fakeDocker{answers: []dockerAnswer{{ranges: []string{"10.99.0.0/16"}}}}
	h := dockerHarness(t, f, 0, optedIn(t, "a"), localProject("b", upstream(t, "b")))
	events, unsubscribe := h.eng.Events().Subscribe(256)
	defer unsubscribe()
	mustStart(t, h, "a")
	mustStart(t, h, "b")
	if got := clientsOf(h, "a.test"); len(got) != 1 || !slices.Equal(got[0], []string{"10.99.0.0/16"}) {
		t.Errorf("a's clients = %q", got)
	}
	if got := clientsOf(h, "b.test"); len(got) != 1 || got[0] != nil {
		t.Errorf("b's clients = %q, want nil", got)
	}
	waitLog(t, events, "a", "accepts requests from Docker networks: 10.99.0.0/16")
}

// While Docker has never answered, nothing beyond loopback is let in: a
// guess at its networks would hold LAN addresses too.
func TestOptedInProjectServesOnlyLoopbackUntilDockerAnswers(t *testing.T) {
	f := &fakeDocker{answers: []dockerAnswer{{err: errors.New("no Docker socket found")}}}
	h := dockerHarness(t, f, 0, optedIn(t, "a"))
	events, unsubscribe := h.eng.Events().Subscribe(256)
	defer unsubscribe()
	mustStart(t, h, "a")
	if st := h.eng.Status("a"); st.State != StateRunning {
		t.Fatalf("status = %+v", st)
	}
	if got := clientsOf(h, "a.test"); len(got) != 1 || got[0] != nil {
		t.Errorf("clients = %q, want nil", got)
	}
	waitLog(t, events, "a", "Docker did not answer (no Docker socket found); accepting requests from containers once it does")
}

func TestRefreshFollowsDockerNetworks(t *testing.T) {
	both := []string{"10.99.0.0/16", "10.98.0.0/16"}
	f := &fakeDocker{answers: []dockerAnswer{{ranges: []string{"10.99.0.0/16"}}, {ranges: both}}}
	h := dockerHarness(t, f, 20*time.Millisecond, optedIn(t, "a"))
	refreshes := recordRefreshes(h)
	events, unsubscribe := h.eng.Events().Subscribe(256)
	defer unsubscribe()
	mustStart(t, h, "a")
	waitClients(t, h, "a.test", both)
	waitLog(t, events, "a", "accepts requests from Docker networks: 10.99.0.0/16, 10.98.0.0/16")
	rebuilt := refreshes()
	if len(rebuilt) != 1 || len(rebuilt[0]) != 1 || !slices.Equal(rebuilt[0][0].Clients, both) {
		t.Fatalf("refresh rebuilds = %+v, want one with %q", rebuilt, both)
	}
	// The same answer again rebuilds nothing.
	calls := f.count()
	testutil.WaitUntil(t, 2*time.Second, func() bool { return f.count() >= calls+3 }, "three more refreshes")
	if n := len(refreshes()); n != 1 {
		t.Errorf("%d rebuilds after Docker gave the same ranges again, want 1", n)
	}
}

func TestRefreshKeepsLastRangesWhenDockerStopsAnswering(t *testing.T) {
	f := &fakeDocker{answers: []dockerAnswer{{ranges: []string{"10.99.0.0/16"}}, {err: errors.New("gone")}}}
	h := dockerHarness(t, f, 20*time.Millisecond, optedIn(t, "a"))
	mustStart(t, h, "a")
	testutil.WaitUntil(t, 2*time.Second, func() bool { return f.count() >= 5 }, "several refreshes")
	if got := clientsOf(h, "a.test"); len(got) != 1 || !slices.Equal(got[0], []string{"10.99.0.0/16"}) {
		t.Errorf("clients = %q, want the answered ranges", got)
	}
}

func TestRefreshServesDockerOnceItAnswers(t *testing.T) {
	f := &fakeDocker{answers: []dockerAnswer{{err: errors.New("not yet")}, {ranges: []string{"10.99.0.0/16"}}}}
	h := dockerHarness(t, f, 20*time.Millisecond, optedIn(t, "a"))
	mustStart(t, h, "a")
	waitClients(t, h, "a.test", []string{"10.99.0.0/16"})
}

func TestRefreshStopsWithoutOptedInProjects(t *testing.T) {
	f := &fakeDocker{answers: []dockerAnswer{{ranges: []string{"10.99.0.0/16"}}}}
	h := dockerHarness(t, f, 20*time.Millisecond, optedIn(t, "a"), localProject("b", upstream(t, "b")))
	mustStart(t, h, "a")
	mustStart(t, h, "b")
	testutil.WaitUntil(t, 2*time.Second, func() bool { return f.count() >= 3 }, "refreshes while a runs")
	if err := h.eng.StopProject(bg, "a"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitUntil(t, 2*time.Second, func() bool {
		h.eng.mu.Lock()
		defer h.eng.mu.Unlock()
		return h.eng.docker.loops == 0
	}, "the refresh loop to end")
	calls := f.count()
	time.Sleep(10 * 20 * time.Millisecond)
	if n := f.count(); n != calls {
		t.Errorf("Docker asked %d more times with no opted-in project running", n-calls)
	}
}

// A remote project is never served to Docker networks, even when a config
// that skipped Validate gives it the flag: containers cannot reach Nearhost
// from another machine, and Docker is not asked.
func TestRemoteProjectIsNotServedToDocker(t *testing.T) {
	f := &fakeDocker{answers: []dockerAnswer{{ranges: []string{"10.99.0.0/16"}}}}
	cfg := remoteConfig(testutil.FreePort(t))
	cfg.Projects[0].Nearhostrc = &config.RcSnapshot{SHA256: "h", DockerClients: true}
	h := buildHarness(t, cfg, func(o *Options) { o.DockerRanges = f.ranges })
	if err := h.eng.Start(bg); err != nil {
		t.Fatalf("Start: %v", err)
	}
	mustStart(t, h, "myapp")
	got := clientsOf(h, "myapp.test")
	if len(got) == 0 {
		t.Fatal("myapp has no published site")
	}
	for _, c := range got {
		if c != nil {
			t.Errorf("myapp's clients = %q, want nil", got)
		}
	}
	if n := f.count(); n != 0 {
		t.Errorf("Docker asked %d times for a remote project", n)
	}
}

// A start with no refresh loop running asks Docker again, though it has
// answered before, so a project started after a long idle is served to the
// networks Docker has now rather than those it had when the loop stopped.
func TestStartAsksDockerAgainWhenNoLoopRuns(t *testing.T) {
	one, both := []string{"10.99.0.0/16"}, []string{"10.99.0.0/16", "10.98.0.0/16"}
	f := &fakeDocker{answers: []dockerAnswer{{ranges: one}, {ranges: both}}}
	h := dockerHarness(t, f, time.Hour, optedIn(t, "a"))
	mustStart(t, h, "a")
	if got := clientsOf(h, "a.test"); len(got) != 1 || !slices.Equal(got[0], one) {
		t.Fatalf("a's clients = %q, want %q", got, one)
	}
	if err := h.eng.StopProject(bg, "a"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitUntil(t, 2*time.Second, func() bool {
		h.eng.mu.Lock()
		defer h.eng.mu.Unlock()
		return h.eng.docker.loops == 0
	}, "the refresh loop to end")
	mustStart(t, h, "a")
	// The refresh is an hour away, so only the start can have asked.
	if got := clientsOf(h, "a.test"); len(got) != 1 || !slices.Equal(got[0], both) {
		t.Errorf("a's clients after starting again = %q, want %q", got, both)
	}
}

// daemonBody is a Docker daemon's network list that a test can change.
type daemonBody struct {
	mu   sync.Mutex
	body string
}

func (d *daemonBody) set(body string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.body = body
}

// leftOutLine is what a project's log says of the network "lan" (macvlan).
const leftOutLine = `docker network "lan" ("macvlan") is not a local bridge; its clients are not let in`

// logsOf returns the log lines the engine has published for project so far.
func logsOf(h *harness, project string) []string {
	var out []string
	for {
		select {
		case ev := <-h.events:
			h.seen = append(h.seen, ev)
		default:
			for _, ev := range h.seen {
				if ev.Project == project && ev.State == "" {
					out = append(out, ev.Message)
				}
			}
			return out
		}
	}
}

// Every project served to Docker is told which networks are left out and
// why, whether it asked Docker itself, started while the refresh loop ran,
// or started after the loop had stopped; a network that turns up while
// projects run is announced to them once.
func TestProjectLogsSayWhichDockerNetworksAreLeftOut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake daemon listens on a unix socket")
	}
	const bridge = `{"Name":"bridge","Driver":"bridge","Scope":"local","IPAM":{"Config":[{"Subnet":"172.17.0.0/16"}]}}`
	const lan = `{"Name":"lan","Driver":"macvlan","Scope":"local","IPAM":{"Config":[{"Subnet":"192.168.1.0/24"}]}}`
	daemon := &daemonBody{body: "[" + bridge + "," + lan + "]"}
	sock := filepath.Join(testutil.TempDir(t, "nh-dn-"), "docker.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		daemon.mu.Lock()
		defer daemon.mu.Unlock()
		_, _ = w.Write([]byte(daemon.body))
	})}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })

	var h *harness
	h = buildHarness(t, &config.Config{Projects: []config.Project{optedIn(t, "a"), optedIn(t, "b")}}, func(o *Options) {
		o.DockerRefresh = 20 * time.Millisecond
		o.DockerRanges = func(ctx context.Context) ([]string, error) {
			return h.eng.docker.source.RangesFrom(ctx, []string{sock})
		}
	})
	if err := h.eng.Start(bg); err != nil {
		t.Fatalf("Start: %v", err)
	}
	count := func(project string) int {
		n := 0
		for _, line := range logsOf(h, project) {
			if line == leftOutLine {
				n++
			}
		}
		return n
	}

	mustStart(t, h, "a") // asks Docker itself
	if n := count("a"); n != 1 {
		t.Errorf("a's log has the left-out line %d times, want 1", n)
	}
	mustStart(t, h, "b") // starts while the refresh loop runs, so does not ask
	if n := count("b"); n != 1 {
		t.Errorf("b's log has the left-out line %d times, want 1", n)
	}
	// The refresh loop runs on, and the network is not announced again.
	time.Sleep(100 * time.Millisecond)
	if n, m := count("a"), count("b"); n != 1 || m != 1 {
		t.Errorf("after refreshes a has the line %d times and b %d, want 1 each", n, m)
	}

	// A network that turns up while they run is announced to both.
	const mesh = `{"Name":"mesh","Driver":"overlay","Scope":"swarm","IPAM":{"Config":[{"Subnet":"10.0.1.0/24"}]}}`
	daemon.set("[" + bridge + "," + lan + "," + mesh + "]")
	meshLine := `docker network "mesh" ("overlay") is not a local bridge; its clients are not let in`
	testutil.WaitUntil(t, 2*time.Second, func() bool {
		for _, p := range []string{"a", "b"} {
			if !slices.Contains(logsOf(h, p), meshLine) {
				return false
			}
		}
		return true
	}, "the new network to be announced to both projects")

	// Started after the loop has stopped, a project is told again.
	for _, p := range []string{"a", "b"} {
		if err := h.eng.StopProject(bg, p); err != nil {
			t.Fatal(err)
		}
	}
	testutil.WaitUntil(t, 2*time.Second, func() bool {
		h.eng.mu.Lock()
		defer h.eng.mu.Unlock()
		return h.eng.docker.loops == 0
	}, "the refresh loop to end")
	before := count("a")
	mustStart(t, h, "a")
	if n := count("a"); n != before+1 {
		t.Errorf("a's log has the left-out line %d times after a restart, want %d", n, before+1)
	}
}
