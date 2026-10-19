package engine

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/testutil"
)

func TestPlanApply(t *testing.T) {
	web := func(name string, port int) config.Project {
		return config.Project{Name: name, Server: config.LocalServer, Path: "/p/" + name, Alias: name + ".test", Enabled: true,
			Routes: []config.Route{{Port: port, Role: config.RoleWeb, Label: "web"}}}
	}
	disabled := func(p config.Project) config.Project { p.Enabled = false; return p }
	onMini := func(p config.Project) config.Project { p.Server = "mini"; return p }
	mini := []config.Server{{Name: "mini", SSHHost: "mini"}}
	for _, c := range []struct {
		name       string
		prev, next *config.Config
		active     []string
		want       applyPlan
	}{
		{"new enabled project starts",
			&config.Config{}, &config.Config{Projects: []config.Project{web("a", 1)}}, nil,
			applyPlan{start: []string{"a"}}},
		{"new disabled project waits",
			&config.Config{}, &config.Config{Projects: []config.Project{disabled(web("a", 1))}}, nil,
			applyPlan{}},
		{"removed project stops",
			&config.Config{Projects: []config.Project{web("a", 1)}}, &config.Config{}, []string{"a"},
			applyPlan{stop: []string{"a"}, replace: map[string]bool{"a": true}}},
		{"changed running project restarts",
			&config.Config{Projects: []config.Project{web("a", 1)}}, &config.Config{Projects: []config.Project{web("a", 2)}}, []string{"a"},
			applyPlan{stop: []string{"a"}, replace: map[string]bool{"a": true}, start: []string{"a"}}},
		{"changed stopped project stays stopped",
			&config.Config{Projects: []config.Project{web("a", 1)}}, &config.Config{Projects: []config.Project{web("a", 2)}}, nil,
			applyPlan{stop: []string{"a"}, replace: map[string]bool{"a": true}}},
		{"a new label restarts nothing",
			&config.Config{Projects: []config.Project{web("a", 1)}},
			&config.Config{Projects: []config.Project{func() config.Project { p := web("a", 1); p.Routes[0].Label = "app"; return p }()}},
			[]string{"a"}, applyPlan{}},
		{"disabled project stops",
			&config.Config{Projects: []config.Project{web("a", 1)}}, &config.Config{Projects: []config.Project{disabled(web("a", 1))}}, []string{"a"},
			applyPlan{stop: []string{"a"}}},
		{"enabled project starts",
			&config.Config{Projects: []config.Project{disabled(web("a", 1))}}, &config.Config{Projects: []config.Project{web("a", 1)}}, nil,
			applyPlan{start: []string{"a"}}},
		{"untouched project stopped by hand stays stopped",
			&config.Config{Projects: []config.Project{web("a", 1), web("b", 2)}},
			&config.Config{Projects: []config.Project{web("a", 1), web("b", 3)}}, []string{"b"},
			applyPlan{stop: []string{"b"}, replace: map[string]bool{"b": true}, start: []string{"b"}}},
		{"a server's new ssh_host restarts its projects",
			&config.Config{Servers: mini, Projects: []config.Project{onMini(web("a", 1)), web("b", 2)}},
			&config.Config{Servers: []config.Server{{Name: "mini", SSHHost: "mini.lan"}}, Projects: []config.Project{onMini(web("a", 1)), web("b", 2)}},
			[]string{"a", "b"},
			applyPlan{stop: []string{"a"}, replace: map[string]bool{"a": true}, start: []string{"a"}, drop: []string{"mini"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			active := map[string]bool{}
			for _, n := range c.active {
				active[n] = true
			}
			got := planApply(c.prev, c.next, func(n string) bool { return active[n] })
			if c.want.replace == nil {
				c.want.replace = map[string]bool{}
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("plan = %+v\nwant   %+v", got, c.want)
			}
		})
	}
}

// waitState waits for a project to reach a state.
func waitState(t *testing.T, h *harness, name string, want State) {
	t.Helper()
	testutil.WaitUntil(t, 10*time.Second, func() bool { return h.eng.Status(name).State == want }, name+" to be "+string(want))
}

func TestApplyStartsAndStopsProjects(t *testing.T) {
	a := localProject("a", upstream(t, "a"))
	h := newHarness(t, &config.Config{Projects: []config.Project{a}})
	if err := h.eng.StartProject(bg, "a"); err != nil {
		t.Fatal(err)
	}
	b := localProject("b", upstream(t, "b"))
	if err := h.eng.Apply(bg, &config.Config{Projects: []config.Project{b}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if st := h.eng.Status("a"); st.State != StateStopped || st.Alias != "" {
		t.Errorf("a removed, status %+v", st)
	}
	waitState(t, h, "b", StateRunning)
	if sites := h.eng.Sites(); len(sites) != 1 || sites[0].Alias != "b.test" {
		t.Errorf("sites = %+v", sites)
	}
	if got := h.eng.Config().Projects; len(got) != 1 || got[0].Name != "b" {
		t.Errorf("Config = %+v", got)
	}
	// The hosts block now holds the new project's alias only.
	calls := h.ensureCalls()
	if last := calls[len(calls)-1]; !reflect.DeepEqual(last, []string{"b.test"}) {
		t.Errorf("last EnsureHosts = %v", last)
	}
}

func TestApplyRestartsOnlyTheChangedProject(t *testing.T) {
	a, b := localProject("a", upstream(t, "a")), localProject("b", upstream(t, "b"))
	h := newHarness(t, &config.Config{Projects: []config.Project{a, b}})
	for _, n := range []string{"a", "b"} {
		if err := h.eng.StartProject(bg, n); err != nil {
			t.Fatal(err)
		}
	}
	events, unsubscribe := h.eng.Events().Subscribe(64)
	defer unsubscribe()
	moved := upstream(t, "a2")
	a.Routes[0].Port = moved
	if err := h.eng.Apply(bg, &config.Config{Projects: []config.Project{a, b}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	testutil.WaitUntil(t, 10*time.Second, func() bool { return h.eng.Status("a").WebLocalPort == moved }, "a on its new port")
	if code, body := h.getEventually(t, "a.test", "/"); code != 200 || !strings.HasPrefix(body, "a2:") {
		t.Errorf("a after Apply: %d %q", code, body)
	}
	unsubscribe()
	for ev := range events {
		if ev.Project == "b" && ev.State != "" {
			t.Errorf("b changed state: %+v", ev)
		}
	}
}

func TestApplyKeepsAProjectStoppedByHandStopped(t *testing.T) {
	a, b := localProject("a", upstream(t, "a")), localProject("b", upstream(t, "b"))
	h := newHarness(t, &config.Config{Projects: []config.Project{a, b}})
	if err := h.eng.StartProject(bg, "b"); err != nil {
		t.Fatal(err)
	}
	b.Routes[0].Label = "renamed"
	if err := h.eng.Apply(bg, &config.Config{Projects: []config.Project{a, b}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // a start, were one wrongly begun, would show by now
	if st := h.eng.Status("a"); st.State != StateStopped {
		t.Errorf("a = %+v", st)
	}
	if st := h.eng.Status("b"); st.State != StateRunning {
		t.Errorf("b = %+v", st)
	}
	if got := h.eng.Config().Projects[1].Routes[0].Label; got != "renamed" {
		t.Errorf("label = %q", got)
	}
}

func TestApplyRefusesAnInvalidConfig(t *testing.T) {
	a := localProject("a", upstream(t, "a"))
	h := newHarness(t, &config.Config{Projects: []config.Project{a}})
	if err := h.eng.StartProject(bg, "a"); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.Apply(bg, &config.Config{Projects: []config.Project{a, a}}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("Apply = %v", err)
	}
	if st := h.eng.Status("a"); st.State != StateRunning {
		t.Errorf("a = %+v", st)
	}
	if got := h.eng.Config().Projects; len(got) != 1 {
		t.Errorf("Config = %+v", got)
	}
}

func TestApplyAfterShutdownDoesNothing(t *testing.T) {
	h := newHarness(t, &config.Config{})
	if err := h.eng.Shutdown(bg); err != nil {
		t.Fatal(err)
	}
	b := localProject("b", upstream(t, "b"))
	if err := h.eng.Apply(bg, &config.Config{Projects: []config.Project{b}}); err == nil {
		t.Fatal("Apply after Shutdown succeeded")
	}
	if st := h.eng.Status("b"); st.Alias != "" {
		t.Errorf("b = %+v", st)
	}
}

func TestApplyReconnectsAServerWhoseHostChanged(t *testing.T) {
	cfg := remoteConfig(testutil.FreePort(t))
	h := newHarness(t, cfg)
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	next := remoteConfig(cfg.Projects[0].Routes[1].Port)
	next.Servers[0].SSHHost = "mini.lan"
	if err := h.eng.Apply(bg, next); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	waitState(t, h, "myapp", StateRunning)
	if !strings.Contains(h.log(), " mini.lan") {
		t.Errorf("no connection to the new host:\n%s", h.log())
	}
}

// Apply may run while projects are started, stopped and read from other
// goroutines, as the interface does. Run with -race.
func TestApplyIsSafeAlongsideOtherCalls(t *testing.T) {
	p1, p2 := upstream(t, "one"), upstream(t, "two")
	a := localProject("a", p1)
	h := newHarness(t, &config.Config{Projects: []config.Project{a}})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			_ = h.eng.StartProject(bg, "a")
			_ = h.eng.Status("a")
			_ = h.eng.Config()
			_ = h.eng.StopProject(bg, "a")
		}
	}()
	for i := 0; i < 10; i++ {
		a.Routes[0].Port = []int{p1, p2}[i%2]
		if err := h.eng.Apply(bg, &config.Config{Projects: []config.Project{a}}); err != nil {
			t.Fatalf("Apply %d: %v", i, err)
		}
	}
	<-done
}

// A start on a run Apply has replaced is refused even when the project is
// busy, where it could otherwise revive the server's session with the
// configuration the run came from.
func TestStartOnAReplacedRunIsRefusedEvenWhenBusy(t *testing.T) {
	h := newHarness(t, remoteConfig(testutil.FreePort(t)))
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	h.eng.mu.Lock()
	h.eng.runs["myapp"].gone = true // as Apply marks it before stopping it
	h.eng.mu.Unlock()
	if err := h.eng.StartProject(bg, "myapp"); err == nil || !strings.Contains(err.Error(), "changed while it was waiting to start") {
		t.Fatalf("StartProject on a replaced, running project = %v", err)
	}
}

// A project renamed in one Apply, at the same server and path, is the same
// project: it starts only if it was running, as a changed project would.
func TestPlanApplyTreatsARenameAsTheSameProject(t *testing.T) {
	old := config.Project{Name: "blog", Server: config.LocalServer, Path: "/p/blog", Alias: "blog.test", Enabled: true,
		Routes: []config.Route{{Port: 1, Role: config.RoleWeb}}}
	renamed := old
	renamed.Name, renamed.Alias = "shop", "shop.test"
	prev, next := &config.Config{Projects: []config.Project{old}}, &config.Config{Projects: []config.Project{renamed}}
	if got := planApply(prev, next, func(string) bool { return false }); len(got.start) != 0 {
		t.Errorf("a stopped project renamed starts: %+v", got)
	}
	if got := planApply(prev, next, func(string) bool { return true }); !reflect.DeepEqual(got.start, []string{"shop"}) {
		t.Errorf("a running project renamed does not start: %+v", got)
	}
}

// A change to the hosts of a web route alone restarts the project, so the
// proxy serves the new hosts and stops serving the removed ones, and the
// hosts file is asked for the new names.
func TestApplyServesTheNewHostsOfARunningProject(t *testing.T) {
	a := localProjectRoutes("a", config.Route{Port: upstream(t, "a"), Role: config.RoleWeb, Hosts: []string{"old.local.example.io"}})
	h := newHarness(t, &config.Config{Projects: []config.Project{a}})
	mustStart(t, h, "a")
	a.Routes = []config.Route{{Port: a.Routes[0].Port, Role: config.RoleWeb, Hosts: []string{"new.local.example.io"}}}
	if err := h.eng.Apply(bg, &config.Config{Projects: []config.Project{a}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := []string{"a.test", "new.local.example.io"}
	testutil.WaitUntil(t, 10*time.Second, func() bool {
		sites := h.eng.Sites()
		return len(sites) == 1 && slices.Equal(sites[0].Hosts, want)
	}, "a served at its new host")
	waitState(t, h, "a", StateRunning)
	calls := h.ensureCalls()
	if last := calls[len(calls)-1]; !slices.Equal(last, want) {
		t.Errorf("last EnsureHosts = %v, want %v", last, want)
	}
}

func TestApplyRestartsWhenDockerClientsChanges(t *testing.T) {
	f := &fakeDocker{answers: []dockerAnswer{{ranges: []string{"10.99.0.0/16"}}}}
	a := localProject("a", upstream(t, "a"))
	a.Nearhostrc = &config.RcSnapshot{SHA256: "h"}
	h := dockerHarness(t, f, 0, a)
	mustStart(t, h, "a")
	if got := clientsOf(h, "a.test"); len(got) != 1 || got[0] != nil {
		t.Fatalf("clients before the opt-in = %q", got)
	}
	events, unsubscribe := h.eng.Events().Subscribe(256)
	defer unsubscribe()
	next := h.eng.Config()
	next.Projects[0].Nearhostrc.DockerClients = true
	if err := h.eng.Apply(bg, next); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	waitStateEvent(t, events, "a", "state: stopped")
	waitState(t, h, "a", StateRunning)
	waitClients(t, h, "a.test", []string{"10.99.0.0/16"})
}
