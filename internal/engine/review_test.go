package engine

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/discovery"
	"github.com/nearhost/nearhost/internal/nearhostrc"
	"github.com/nearhost/nearhost/internal/testutil"
)

// withReview builds and starts a harness whose Options.Review is review.
func withReview(t *testing.T, cfg *config.Config, review func(context.Context, config.Project, discovery.FileReader) Review, sshEnv ...string) *harness {
	t.Helper()
	h := buildHarness(t, cfg, func(o *Options) { o.Review = review }, sshEnv...)
	if err := h.eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return h
}

func TestPendingChangeKeepsApprovedRoutesServing(t *testing.T) {
	port := upstream(t, "p")
	cfg := &config.Config{Projects: []config.Project{localProject("p", port)}}
	h := withReview(t, cfg, func(_ context.Context, p config.Project, _ discovery.FileReader) Review {
		next := p
		next.Routes = []config.Route{{Port: port, Role: config.RoleWeb, Hosts: []string{"new.local.x.io"}}}
		return Review{Pending: &Change{SHA256: "b", Project: next, Items: []nearhostrc.Item{{Port: port, Host: "new.local.x.io", Why: "a real domain"}}}}
	})
	events, unsubscribe := h.eng.Events().Subscribe(64)
	defer unsubscribe()
	mustStart(t, h, "p")

	for _, s := range h.eng.Sites() {
		for _, host := range s.Hosts {
			if host == "new.local.x.io" {
				t.Fatalf("a host waiting for approval is served: %+v", h.eng.Sites())
			}
		}
	}
	if sites := h.eng.Sites(); len(sites) != 1 || sites[0].Hosts[0] != "p.test" {
		t.Fatalf("Sites = %+v", sites)
	}
	st := h.eng.Status("p")
	if st.State != StateRunning || st.Pending == nil || st.Pending.SHA256 != "b" {
		t.Fatalf("status = %+v", st)
	}
	if !sawLog(events, "p", ".nearhostrc asks for changes that need approval: new.local.x.io") {
		t.Fatal("no event about the pending change")
	}
}

func TestPortOnlyChangeIsSavedOnceAndDoesNotRestartAgain(t *testing.T) {
	old, moved := upstream(t, "old"), upstream(t, "moved")
	cfg := &config.Config{Projects: []config.Project{localProject("p", old)}}
	var calls atomic.Int32
	var updated config.Project
	h := withReview(t, cfg, func(_ context.Context, p config.Project, _ discovery.FileReader) Review {
		if calls.Add(1) > 1 {
			return Review{}
		}
		next := p
		next.Routes = []config.Route{{Port: moved, Role: config.RoleWeb, Label: "web"}}
		next.Nearhostrc = &config.RcSnapshot{SHA256: "a", Routes: next.Routes}
		updated = next
		return Review{Updated: &next}
	})
	events, unsubscribe := h.eng.Events().Subscribe(256)
	defer unsubscribe()
	mustStart(t, h, "p")

	if sites := h.eng.Sites(); len(sites) != 1 || sites[0].Upstream != "localhost:"+strconv.Itoa(moved) {
		t.Fatalf("Sites = %+v", h.eng.Sites())
	}
	if !sawLog(events, "p", ".nearhostrc changed nothing that needs approval; applied") {
		t.Fatal("no event about the applied change")
	}
	if p, _ := h.eng.Config().Project("p"); p.Routes[0].Port != moved {
		t.Fatalf("the engine's config still has %+v", p.Routes)
	}

	stateEvents(events, "p") // the start's own
	// The saved file comes back through Apply, as the cli's reload does.
	if err := h.eng.Apply(context.Background(), &config.Config{Projects: []config.Project{updated}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // a restart Apply began would have called Review by now
	if states, _ := stateEvents(events, "p"); len(states) != 0 {
		t.Fatalf("the project was restarted: %v", states)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("Review called %d times, want 1", n)
	}
	if st := h.eng.Status("p"); st.State != StateRunning {
		t.Fatalf("status = %+v", st)
	}
}

func TestUnreadableFileKeepsApprovedRoutes(t *testing.T) {
	// The fake ssh refuses the sftp subsystem, so the file cannot be read.
	var called atomic.Bool
	h := withReview(t, remoteConfig(testutil.FreePort(t)), func(context.Context, config.Project, discovery.FileReader) Review {
		called.Store(true)
		return Review{}
	})
	mustStart(t, h, "myapp")
	st := h.eng.Status("myapp")
	if st.State != StateRunning {
		t.Fatalf("status = %+v", st)
	}
	if !strings.HasPrefix(st.Notice, "cannot read .nearhostrc") {
		t.Fatalf("Notice = %q", st.Notice)
	}
	if called.Load() {
		t.Fatal("Review was called without a reader")
	}
	if sites := h.eng.Sites(); len(sites) != 1 || sites[0].Hosts[0] != "myapp.test" {
		t.Fatalf("Sites = %+v", sites)
	}
}

func TestDeclinedChangeStaysDeclinedUntilTheFileChanges(t *testing.T) {
	port := upstream(t, "p")
	cfg := &config.Config{Projects: []config.Project{localProject("p", port)}}
	var mu sync.Mutex
	sha := "b"
	h := withReview(t, cfg, func(_ context.Context, p config.Project, _ discovery.FileReader) Review {
		mu.Lock()
		defer mu.Unlock()
		next := p
		next.Routes = []config.Route{{Port: port, Role: config.RoleWeb, Hosts: []string{"x.example.com"}}}
		return Review{Pending: &Change{SHA256: sha, Project: next, Items: []nearhostrc.Item{{Port: port, Host: "x.example.com", Why: "a real domain"}}}}
	})
	mustStart(t, h, "p")
	if st := h.eng.Status("p"); st.Pending == nil || st.Pending.SHA256 != "b" {
		t.Fatalf("status = %+v", st)
	}

	h.eng.DeclineChange("p", "b")
	if st := h.eng.Status("p"); st.Pending != nil {
		t.Fatalf("Pending after decline = %+v", st.Pending)
	}
	if err := h.eng.Recheck(context.Background(), "p"); err != nil {
		t.Fatal(err)
	}
	if st := h.eng.Status("p"); st.Pending != nil {
		t.Fatalf("a declined change came back: %+v", st.Pending)
	}

	mu.Lock()
	sha = "c"
	mu.Unlock()
	if err := h.eng.Recheck(context.Background(), "p"); err != nil {
		t.Fatal(err)
	}
	if st := h.eng.Status("p"); st.Pending == nil || st.Pending.SHA256 != "c" {
		t.Fatalf("status after the file changed = %+v", st)
	}
	if st := h.eng.Status("p"); st.State != StateRunning {
		t.Fatalf("Recheck changed the state: %+v", st)
	}
}

// sawLog reports whether a log line (not a state change) of project with
// exactly message is buffered on events. Other events are dropped.
func sawLog(events <-chan Event, project, message string) bool {
	for {
		select {
		case ev := <-events:
			if ev.Project == project && ev.State == "" && ev.Message == message {
				return true
			}
		default:
			return false
		}
	}
}

func TestPendingLogNamesDockerClients(t *testing.T) {
	port := upstream(t, "p")
	cfg := &config.Config{Projects: []config.Project{localProject("p", port)}}
	h := withReview(t, cfg, func(_ context.Context, p config.Project, _ discovery.FileReader) Review {
		next := p
		next.Nearhostrc = &config.RcSnapshot{SHA256: "b", Routes: next.Routes, DockerClients: true}
		return Review{Pending: &Change{SHA256: "b", Project: next, Items: []nearhostrc.Item{{Option: nearhostrc.OptionDockerClients, Why: "lets local Docker containers reach this project"}}}}
	})
	events, unsubscribe := h.eng.Events().Subscribe(64)
	defer unsubscribe()
	mustStart(t, h, "p")
	if !sawLog(events, "p", ".nearhostrc asks for changes that need approval: docker_clients") {
		t.Fatal("no event naming docker_clients")
	}
}

// The engine's copies of its configuration keep docker_clients, so an
// approval or a port-only change through the engine does not lose it.
func TestEngineCopiesKeepDockerClients(t *testing.T) {
	snap := &config.RcSnapshot{SHA256: "a", DockerClients: true, Routes: []config.Route{{Port: 80, Role: config.RoleWeb}}}
	cfg := &config.Config{Projects: []config.Project{{Name: "p", Server: config.LocalServer, Alias: "p.test", Nearhostrc: snap}}}
	if p, _ := cloneConfig(cfg).Project("p"); !p.DockerClients() {
		t.Fatalf("cloneConfig lost docker_clients: %+v", p.Nearhostrc)
	}

	// A port-only change reaches adoptLocked through a start.
	moved := upstream(t, "moved")
	old := upstream(t, "old")
	proj := localProject("p", old)
	proj.Nearhostrc = &config.RcSnapshot{SHA256: "a", DockerClients: true, Routes: proj.Routes}
	var calls atomic.Int32
	h := withReview(t, &config.Config{Projects: []config.Project{proj}}, func(_ context.Context, p config.Project, _ discovery.FileReader) Review {
		if calls.Add(1) > 1 {
			return Review{}
		}
		next := p
		next.Routes = []config.Route{{Port: moved, Role: config.RoleWeb, Label: "web"}}
		next.Nearhostrc = &config.RcSnapshot{SHA256: "b", DockerClients: true, Routes: next.Routes}
		return Review{Updated: &next}
	})
	mustStart(t, h, "p")
	if p, _ := h.eng.Config().Project("p"); p.Routes[0].Port != moved || !p.DockerClients() {
		t.Fatalf("adopted project = %+v, nearhostrc %+v", p, p.Nearhostrc)
	}
}

// A main name a .nearhostrc moves without needing approval is adopted by the
// start: the status, the proxy's site and the proxy's logs follow it.
func TestStartAdoptsFileAliasAndFilesLogsUnderIt(t *testing.T) {
	port := upstream(t, "p")
	p := localProject("p", port)
	p.Alias = "a.test"
	p.Nearhostrc = &config.RcSnapshot{SHA256: "old", Alias: "a.test", Routes: p.Routes}
	var calls atomic.Int32
	h := withReview(t, &config.Config{Projects: []config.Project{p}}, func(_ context.Context, p config.Project, _ discovery.FileReader) Review {
		if calls.Add(1) > 1 {
			return Review{}
		}
		next := p
		next.Alias = "b.test"
		next.Nearhostrc = &config.RcSnapshot{SHA256: "new", Alias: "b.test", Routes: next.Routes}
		return Review{Updated: &next}
	})
	events, unsubscribe := h.eng.Events().Subscribe(256)
	defer unsubscribe()
	mustStart(t, h, "p")

	if st := h.eng.Status("p"); st.State != StateRunning || st.Alias != "b.test" {
		t.Fatalf("status = %+v", st)
	}
	if sites := h.eng.Sites(); len(sites) != 1 || sites[0].Alias != "b.test" || sites[0].Hosts[0] != "b.test" {
		t.Fatalf("Sites = %+v", sites)
	}
	if q, _ := h.eng.Config().Project("p"); q.Alias != "b.test" {
		t.Fatalf("the engine's config has alias %q", q.Alias)
	}
	h.eng.proxyLog("b.test", "GET / 200")
	if !sawLog(events, "p", "GET / 200") {
		t.Fatal("a proxy log line under the new main name was not filed under the project")
	}
}
