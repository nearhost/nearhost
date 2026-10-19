package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/doctor"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/nearhostrc"
	"github.com/nearhost/nearhost/internal/tui"
)

// agentHome is a home directory short enough for ~/.nearhost/<id>.sock.
func agentHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "nh-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func fakeAgents(home string) agentsBackend {
	return agentsBackend{
		home:   func() (string, error) { return home, nil },
		id:     func() (string, error) { return "k3f9q2", nil },
		laptop: func() string { return "ecs@macbook" },
	}
}

// getJSON asks the agent API on sock for path.
func getJSON(t *testing.T, sock, path string) map[string]any {
	t.Helper()
	c := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	resp, err := c.Get("http://nearhost" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAZeroAgentsBackendServesNone(t *testing.T) {
	var opts engine.Options
	a, note := serveAgents(agentsBackend{}, &opts, "", 443, agentEnv{})
	if a != nil || note != "" || opts.Agent != nil {
		t.Fatalf("%v %q", a, note)
	}
	a.attach(nil) // nil agents do nothing
	a.close()
}

func TestAgentsListenOnTheLaptopsSocketAndForwardEachServer(t *testing.T) {
	home := agentHome(t)
	opts := engine.Options{RunDir: filepath.Join(home, "run")}
	a, note := serveAgents(fakeAgents(home), &opts, filepath.Join(home, "config.yaml"), 443, agentEnv{})
	if a == nil || note != "" {
		t.Fatalf("%v %q", a, note)
	}
	defer a.close()
	sock := filepath.Join(home, ".nearhost", "k3f9q2.sock")
	if out := getJSON(t, sock, "/v1"); out["origin"] != "local" || out["laptop"] != "ecs@macbook" {
		t.Fatalf("GET /v1: %v", out)
	}
	f, ok := opts.Agent("mini")
	if !ok || f.Name != "k3f9q2.sock" || !strings.HasPrefix(f.Local, opts.RunDir+"/agent-") || f.Own != sock {
		t.Fatalf("forward %+v %v", f, ok)
	}
	if out := getJSON(t, f.Local, "/v1"); out["origin"] != "mini" {
		t.Fatalf("mini's socket: %v", out)
	}
	a.close()
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("close left the socket: %v", err)
	}
}

func TestASecondNearhostLeavesAgentsToTheFirst(t *testing.T) {
	home := agentHome(t)
	first, _ := serveAgents(fakeAgents(home), &engine.Options{RunDir: home}, "", 443, agentEnv{})
	defer first.close()
	var opts engine.Options
	second, note := serveAgents(fakeAgents(home), &opts, "", 443, agentEnv{})
	if second != nil || opts.Agent != nil || !strings.Contains(note, "Another Nearhost is already serving coding agents") {
		t.Fatalf("%v %q", second, note)
	}
}

func TestAgentStatusComesFromTheConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := &config.Config{
		Servers: []config.Server{{Name: "mini", SSHHost: "mini"}},
		Projects: []config.Project{
			{Name: "here", Server: config.LocalServer, Path: "/Users/ecs/here", Alias: "here.test", Enabled: true,
				Routes: []config.Route{{Port: 3000, Role: config.RoleWeb}}},
			{Name: "myapp", Server: "mini", Path: "/home/dev/code/myapp", Alias: "myapp.test", Enabled: true,
				Routes: []config.Route{{Port: 8080, Role: config.RoleWeb, Label: "app"}, {Port: 5432, Role: config.RoleIgnore}}},
		},
	}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	src := &agentSource{cfgPath: path, httpsPort: 18443}
	got := src.Projects("mini")
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	p := got[0]
	if p.Name != "myapp" || p.URL != "https://myapp.test:18443" || p.State != "stopped" || p.Web != 8080 ||
		len(p.Routes) != 2 || p.Routes[0].Applied || p.Routes[0].Label != "app" {
		t.Fatalf("%+v", p)
	}
	if got := src.Projects("local"); len(got) != 1 || got[0].URL != "https://here.test:18443" {
		t.Fatalf("local: %+v", got)
	}
	if siteURL("a.test", 443) != "https://a.test" {
		t.Fatal(siteURL("a.test", 443))
	}
}

func TestAServersSocketThatCannotBeListenedOnIsLogged(t *testing.T) {
	home := agentHome(t)
	blocked := filepath.Join(home, "run")
	os.WriteFile(blocked, []byte("a file where the run directory should be"), 0o600)
	opts := engine.Options{RunDir: blocked}
	a, _ := serveAgents(fakeAgents(home), &opts, filepath.Join(home, "config.yaml"), 443, agentEnv{})
	defer a.close()
	eng := engine.New(&config.Config{}, engine.Options{})
	a.attach(eng)
	events, unsubscribe := eng.Events().Subscribe(8)
	defer unsubscribe()
	if _, ok := opts.Agent("mini"); ok {
		t.Fatal("a forward without a socket to reach")
	}
	select {
	case ev := <-events:
		if ev.Server != "mini" || !strings.Contains(ev.Message, "mini: agents unavailable: ") {
			t.Fatalf("event %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("nothing was logged")
	}
}

func TestAServerThatIsGoneStopsBeingServed(t *testing.T) {
	home := agentHome(t)
	opts := engine.Options{RunDir: filepath.Join(home, "run")}
	a, _ := serveAgents(fakeAgents(home), &opts, filepath.Join(home, "config.yaml"), 443, agentEnv{})
	defer a.close()
	f, _ := opts.Agent("mini")
	opts.AgentDone("mini")
	if a.api.Listening("mini") {
		t.Fatal("still serving mini")
	}
	if _, err := os.Stat(f.Local); !os.IsNotExist(err) {
		t.Fatalf("mini's socket is still there: %v", err)
	}
}

func TestAgentProjectsListEveryURL(t *testing.T) {
	p := config.Project{Name: "dd", Server: config.LocalServer, Path: "/code/dd", Alias: "dd.test", Enabled: true,
		Routes: []config.Route{
			{Port: 8220, Role: config.RoleWeb, Label: "app", Hosts: []string{"app.local.dd.io", "*.status.local.dd.io"}},
			{Port: 8230, Role: config.RoleWeb, Label: "ws", Hosts: []string{"ws.local.dd.io"}},
			{Port: 5432, Role: config.RoleIgnore},
		}}
	st := engine.ProjectStatus{State: engine.StateRunning, WebLocalPort: 8220, WebLocalPorts: map[int]int{8220: 8220, 8230: 8230}}
	got := agentProject(p, st, 443)
	wantURLs := []string{"https://dd.test", "https://app.local.dd.io", "https://*.status.local.dd.io", "https://ws.local.dd.io"}
	if got.URL != "https://dd.test" || !reflect.DeepEqual(got.URLs, wantURLs) {
		t.Fatalf("url %q urls %v", got.URL, got.URLs)
	}
	if len(got.Routes) != 3 || !got.Routes[0].Applied || !got.Routes[1].Applied || got.Routes[2].Applied ||
		!reflect.DeepEqual(got.Routes[0].Hosts, []string{"dd.test", "app.local.dd.io", "*.status.local.dd.io"}) ||
		!reflect.DeepEqual(got.Routes[1].Hosts, []string{"ws.local.dd.io"}) || got.Routes[2].Hosts != nil {
		t.Fatalf("%+v", got.Routes)
	}
	// A second web route that is not serving is not applied.
	st.WebLocalPorts = map[int]int{8220: 8220}
	if got := agentProject(p, st, 18443); got.Routes[1].Applied || got.URLs[3] != "https://ws.local.dd.io:18443" {
		t.Fatalf("%+v %v", got.Routes, got.URLs)
	}
}

// fakeProjects is an engine that reports states and records rereads, stops
// and starts. A reread calls reread when it is set and finds review; Status
// shows pending and notice.
type fakeProjects struct {
	states  map[string]engine.State
	reread  func(name string)
	log     *[]string
	review  engine.Review
	pending *engine.Change
	notice  string
	// rereadErr fails each Reread, as for a server not connected.
	rereadErr error
}

func (f fakeProjects) Status(name string) engine.ProjectStatus {
	st := engine.ProjectStatus{State: engine.StateStopped, Pending: f.pending, Notice: f.notice}
	if s, ok := f.states[name]; ok {
		st.State = s
	}
	return st
}

func (f fakeProjects) Reread(_ context.Context, name string) (engine.Review, error) {
	*f.log = append(*f.log, "reread "+name)
	if f.reread != nil {
		f.reread(name)
	}
	return f.review, f.rereadErr
}

func (f fakeProjects) StopProject(ctx context.Context, name string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	*f.log = append(*f.log, "stop "+name)
	return nil
}

func (f fakeProjects) StartProject(ctx context.Context, name string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	*f.log = append(*f.log, "start "+name)
	return nil
}

// severities lists the id and severity of each finding.
func severities(findings []doctor.Finding) []string {
	var out []string
	for _, f := range findings {
		out = append(out, f.ID+" "+string(f.Severity))
	}
	return out
}

func TestAgentDoctorChecksLocalAndRemoteProjects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	here := localProject("here", 3000, true)
	here.Path = t.TempDir()
	myapp := config.Project{Name: "myapp", Server: "box", Path: "/srv/myapp", Alias: "myapp.test", Enabled: true}
	saveConfig(t, path, &config.Config{
		Servers:  []config.Server{{Name: "box", SSHHost: "box.lan"}},
		Projects: []config.Project{here, myapp},
	})
	box := &memFiles{dirs: map[string]map[string]string{"/srv/myapp": {}, "/srv/plain": {}}}
	var log []string
	src := &agentSource{
		cfgPath: path,
		env:     agentEnv{doctor: doctorEnv{listening: func(context.Context, int) bool { return false }, goos: "darwin"}},
		openFiles: func(_ context.Context, srv config.Server) (serverFiles, error) {
			log = append(log, "open "+srv.Name)
			if srv.Name == config.LocalServer {
				return localFiles{}, nil
			}
			return box, nil
		},
	}
	ctx := context.Background()

	// Without an engine yet, nothing is re-read and nothing runs.
	got, err := src.Doctor(ctx, config.LocalServer, "here", here.Path)
	if err != nil || !reflect.DeepEqual(severities(got), []string{"routes.not-listening info"}) {
		t.Fatalf("%v %+v", err, got)
	}

	// A running project's port that nothing listens on is a warning.
	src.projects = fakeProjects{states: map[string]engine.State{"here": engine.StateRunning}, log: &log,
		reread: func(name string) {
			if name == "myapp" {
				// A change the re-read finds is in the findings.
				box.dirs["/srv/myapp"][".nearhostrc"] = unknownKeyRc
			}
		}}
	log = nil
	got, err = src.Doctor(ctx, config.LocalServer, "here", here.Path)
	if err != nil || !reflect.DeepEqual(severities(got), []string{"routes.not-listening warning"}) {
		t.Fatalf("%v %+v", err, got)
	}
	if !reflect.DeepEqual(log, []string{"reread here", "open local"}) {
		t.Fatalf("log %v", log)
	}

	log = nil
	got, err = src.Doctor(ctx, "box", "myapp", "/srv/myapp")
	if err != nil || len(got) == 0 || got[0].ID != "nearhostrc.invalid" || got[0].Project != "myapp" {
		t.Fatalf("%v %+v", err, got)
	}
	if !reflect.DeepEqual(log, []string{"reread myapp", "open box"}) || box.closed != 1 {
		t.Fatalf("log %v, closed %d", log, box.closed)
	}

	// A directory not added is checked as it would be added, not re-read.
	log = nil
	got, err = src.Doctor(ctx, "box", "", "/srv/plain")
	if err != nil || len(got) != 0 {
		t.Fatalf("%v %+v", err, got)
	}
	if !reflect.DeepEqual(log, []string{"open box"}) || box.closed != 2 {
		t.Fatalf("log %v, closed %d", log, box.closed)
	}

	_, err = src.Doctor(ctx, "box", "", "/srv/gone")
	if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "/srv/gone: no such directory") || box.closed != 3 {
		t.Fatalf("a missing directory: %v, closed %d", err, box.closed)
	}
}

func TestAgentDoctorSaysWhyAServerCannotBeChecked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	saveConfig(t, path, &config.Config{Servers: []config.Server{{Name: "box", SSHHost: "box.lan"}}})
	src := &agentSource{cfgPath: path, openFiles: func(context.Context, config.Server) (serverFiles, error) {
		return nil, errors.New("connect to box.lan: no route to host")
	}}
	_, err := src.Doctor(context.Background(), "box", "", "/srv/plain")
	if err == nil || errors.Is(err, fs.ErrNotExist) || err.Error() != "connect to box.lan: no route to host" {
		t.Fatalf("%v", err)
	}
}

func TestAgentsOfferTheDoctorAndReload(t *testing.T) {
	home := agentHome(t)
	opts := engine.Options{RunDir: filepath.Join(home, "run")}
	a, _ := serveAgents(fakeAgents(home), &opts, filepath.Join(home, "config.yaml"), 443, agentEnv{})
	defer a.close()
	out := getJSON(t, filepath.Join(home, ".nearhost", "k3f9q2.sock"), "/v1")
	if caps, _ := out["capabilities"].([]any); len(caps) != 2 || caps[0] != "doctor" || caps[1] != "reload" {
		t.Fatalf("GET /v1: %v", out)
	}
}

func TestAgentDoctorGivesUpWhenTheCallEnds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	saveConfig(t, path, &config.Config{Servers: []config.Server{{Name: "box", SSHHost: "box.lan"}}})
	box := &memFiles{dirs: map[string]map[string]string{"/srv/plain": {}}, block: make(chan struct{})}
	defer close(box.block)
	src := &agentSource{cfgPath: path, openFiles: func(context.Context, config.Server) (serverFiles, error) { return box, nil }}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := src.Doctor(ctx, "box", "", "/srv/plain")
	if !errors.Is(err, context.DeadlineExceeded) || box.closed != 1 {
		t.Fatalf("%v, closed %d", err, box.closed)
	}
}

// waitingChange is a change of p's .nearhostrc that waits for approval.
var waitingChange = &engine.Change{SHA256: "b", Items: []nearhostrc.Item{
	{Port: 3000, Host: "app.example.com", Why: "a real domain"}, {Option: "docker_clients"},
}}

func TestReloadReportsAChangeTheUserDeclined(t *testing.T) {
	var log []string
	src := &agentSource{projects: fakeProjects{log: &log, review: engine.Review{Pending: waitingChange}}}
	got, err := src.Reload(context.Background(), "p")
	if err != nil || !got.Declined || len(got.Items) != 0 || got.SHA256 != "" {
		t.Fatalf("%+v %v", got, err)
	}

	// The same change still waiting is what waits.
	src.projects = fakeProjects{log: &log, review: engine.Review{Pending: waitingChange}, pending: waitingChange}
	got, err = src.Reload(context.Background(), "p")
	if err != nil || got.Declined || !reflect.DeepEqual(got.Items, []string{"app.example.com", "docker_clients"}) || got.SHA256 != "b" {
		t.Fatalf("waiting: %+v %v", got, err)
	}

	src.projects = fakeProjects{log: &log, review: engine.Review{Problem: "bad"}, notice: "bad"}
	if got, err = src.Reload(context.Background(), "p"); err != nil || got.Problem != "bad" || got.Declined {
		t.Fatalf("a problem: %+v %v", got, err)
	}
	if !reflect.DeepEqual(log, []string{"reread p", "reread p", "reread p"}) {
		t.Fatalf("log %v", log)
	}

	if _, err := (&agentSource{}).Reload(context.Background(), "p"); err == nil {
		t.Fatal("reloaded without an engine")
	}
}

func TestWaitingIsTheHashOfTheChangeThatWaits(t *testing.T) {
	src := &agentSource{}
	if got := src.Waiting("p"); got != "" {
		t.Fatalf("without an engine: %q", got)
	}
	src.projects = fakeProjects{pending: waitingChange}
	if got := src.Waiting("p"); got != "b" {
		t.Fatalf("waiting: %q", got)
	}
	src.projects = fakeProjects{}
	if got := src.Waiting("p"); got != "" {
		t.Fatalf("nothing waiting: %q", got)
	}
}

func TestReloadAsksTheInterfaceToShowTheChange(t *testing.T) {
	var log []string
	eng := engine.New(&config.Config{}, engine.Options{})
	events, unsubscribe := eng.Events().Subscribe(8)
	defer unsubscribe()
	src := &agentSource{projects: fakeProjects{log: &log, review: engine.Review{Pending: waitingChange}, pending: waitingChange}}
	src.eng.Store(eng)

	// Under nearhost up nothing can show it.
	if _, err := src.Reload(context.Background(), "p"); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-events:
		t.Fatalf("without the interface: %+v", ev)
	default:
	}

	src.reviewed.Store(true)
	if _, err := src.Reload(context.Background(), "p"); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-events:
		if ev.Project != "p" || ev.Message != "A coding agent asks you to approve what its .nearhostrc asks for: select p and press a." {
			t.Fatalf("event %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("the interface was not asked")
	}
}

func TestReloadRestartsUnderUp(t *testing.T) {
	updated := engine.Review{Updated: &config.Project{Name: "p"}}
	running := map[string]engine.State{"p": engine.StateRunning}
	for _, c := range []struct {
		name    string
		restart bool
		states  map[string]engine.State
		review  engine.Review
		want    []string
	}{
		{"up", true, running, updated, []string{"reread p", "stop p", "start p"}},
		{"the interface", false, running, updated, []string{"reread p"}},
		{"stopped", true, nil, updated, []string{"reread p"}},
		{"unchanged", true, running, engine.Review{}, []string{"reread p"}},
	} {
		var log []string
		src := &agentSource{env: agentEnv{restart: c.restart}, projects: fakeProjects{log: &log, states: c.states, review: c.review}}
		got, err := src.Reload(context.Background(), "p")
		if err != nil || got.Declined || len(got.Items) != 0 || !reflect.DeepEqual(log, c.want) {
			t.Errorf("%s: %+v %v, log %v", c.name, got, err, log)
		}
	}
}

// Under up a change saved by the doctor's re-read is served at once, as a
// reload's is, so the findings are about what runs.
func TestDoctorRestartsUnderUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	p := localProject("p", 3000, true)
	p.Path = t.TempDir()
	saveConfig(t, path, &config.Config{Projects: []config.Project{p}})
	updated := engine.Review{Updated: &config.Project{Name: "p"}}
	running := map[string]engine.State{"p": engine.StateRunning}
	for _, c := range []struct {
		name    string
		restart bool
		states  map[string]engine.State
		review  engine.Review
		want    []string
	}{
		{"up", true, running, updated, []string{"reread p", "stop p", "start p", "open local"}},
		{"the interface", false, running, updated, []string{"reread p", "open local"}},
		{"stopped", true, nil, updated, []string{"reread p", "open local"}},
		{"unchanged", true, running, engine.Review{}, []string{"reread p", "open local"}},
	} {
		var log []string
		src := &agentSource{
			cfgPath:  path,
			env:      agentEnv{restart: c.restart, doctor: doctorEnv{listening: func(context.Context, int) bool { return true }, goos: "darwin"}},
			projects: fakeProjects{log: &log, states: c.states, review: c.review},
			openFiles: func(_ context.Context, srv config.Server) (serverFiles, error) {
				log = append(log, "open "+srv.Name)
				return localFiles{}, nil
			},
		}
		if _, err := src.Doctor(context.Background(), config.LocalServer, "p", p.Path); err != nil || !reflect.DeepEqual(log, c.want) {
			t.Errorf("%s: %v, log %v", c.name, err, log)
		}
	}
}

// A re-read that fails, as for a server not connected, still leaves the
// doctor to report what is in the project's files.
func TestDoctorToleratesAFailedReread(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	p := localProject("p", 3000, true)
	p.Path = t.TempDir()
	writeFiles(t, p.Path, map[string]string{".nearhostrc": unknownKeyRc})
	saveConfig(t, path, &config.Config{Projects: []config.Project{p}})
	var log []string
	src := &agentSource{
		cfgPath: path,
		env:     agentEnv{restart: true, doctor: doctorEnv{listening: func(context.Context, int) bool { return true }, goos: "darwin"}},
		projects: fakeProjects{log: &log, states: map[string]engine.State{"p": engine.StateRunning},
			review: engine.Review{Updated: &config.Project{Name: "p"}}, rereadErr: errors.New(`server "local" is not connected`)},
		openFiles: func(context.Context, config.Server) (serverFiles, error) { return localFiles{}, nil },
	}
	got, err := src.Doctor(context.Background(), config.LocalServer, "p", p.Path)
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if !slices.ContainsFunc(got, func(f doctor.Finding) bool { return f.ID == "nearhostrc.invalid" }) {
		t.Errorf("findings %+v, want the broken .nearhostrc", got)
	}
	if !reflect.DeepEqual(log, []string{"reread p"}) {
		t.Errorf("log %v, want one re-read and no restart", log)
	}
}

// An agent is told what to do once ssh lets Nearhost in, in its own words.
func TestAgentDoctorTellsTheAgentToRunItAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	saveConfig(t, path, &config.Config{Servers: []config.Server{{Name: "box", SSHHost: "box.lan"}}})
	ssh, _ := fakeClient(t, "FAKE_SSH_AUTH_FAIL=1")
	src := &agentSource{cfgPath: path, env: agentEnv{ssh: ssh}}
	_, err := src.Doctor(context.Background(), "box", "", "/srv/plain")
	if err == nil || !strings.Contains(err.Error(), "run `ssh box.lan` once in a terminal to fix access, then run /nearhost doctor again") {
		t.Fatalf("%v", err)
	}
}

// postJSON posts body to path on the agent API on sock.
func postJSON(t *testing.T, sock, path, body string) (int, map[string]any) {
	t.Helper()
	c := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	resp, err := c.Post("http://nearhost"+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, out
}

// A reload waiting on a change ends when the user answers it in the
// interface.
func TestAnsweringAChangeTellsTheAgents(t *testing.T) {
	for _, accept := range []bool{true, false} {
		u, path := pendingUI(t, []string{"app.local.example.io"})
		home := agentHome(t)
		opts := engine.Options{RunDir: filepath.Join(home, "run")}
		a, note := serveAgents(fakeAgents(home), &opts, path, 443, agentEnv{})
		if a == nil {
			t.Fatal(note)
		}
		defer a.close()
		a.attach(u.Engine)
		a.review(&tui.Proposals{})
		u.agents = a
		sock := filepath.Join(home, ".nearhost", "k3f9q2.sock")
		status, out := postJSON(t, sock, "/v1/projects/reload", `{"path": "/tmp/two"}`)
		if status != 202 {
			t.Fatalf("reload: %d %v", status, out)
		}
		id := out["id"].(string)
		want := "declined"
		if accept {
			want = "accepted"
			if err := u.AcceptChange("two", "new"); err != nil {
				t.Fatal(err)
			}
		} else {
			u.DeclineChange("two", "new")
		}
		if out := getJSON(t, sock, "/v1/proposals/"+id+"?wait=5"); out["state"] != want {
			t.Fatalf("accept %v: %v", accept, out)
		}
	}
}

// A re-read while the user's approval is saved can clear the engine's
// pending change before the save returns. An agent polling then still sees
// its reload waiting, and then accepted.
func TestAcceptingAChangeHoldsItsReloadsWhileSaving(t *testing.T) {
	u, path := pendingUI(t, []string{"app.local.example.io"})
	home := agentHome(t)
	opts := engine.Options{RunDir: filepath.Join(home, "run")}
	a, note := serveAgents(fakeAgents(home), &opts, path, 443, agentEnv{})
	if a == nil {
		t.Fatal(note)
	}
	defer a.close()
	a.attach(u.Engine)
	a.review(&tui.Proposals{})
	u.agents = a
	sock := filepath.Join(home, ".nearhost", "k3f9q2.sock")
	status, out := postJSON(t, sock, "/v1/projects/reload", `{"path": "/tmp/two"}`)
	if status != 202 {
		t.Fatalf("reload: %d %v", status, out)
	}
	id := out["id"].(string)

	var during any
	u.acceptSaving = func() {
		// What a re-read in another goroutine could do meanwhile.
		u.Engine.DeclineChange("two", "new")
		during = getJSON(t, sock, "/v1/proposals/"+id)["state"]
	}
	if err := u.AcceptChange("two", "new"); err != nil {
		t.Fatal(err)
	}
	if during != "pending" {
		t.Errorf("polled during the save: %v", during)
	}
	if out := getJSON(t, sock, "/v1/proposals/"+id+"?wait=5"); out["state"] != "accepted" {
		t.Fatalf("after the save: %v", out)
	}
}

// AcceptChange holds a change before it reads whether the change still
// waits, and lets go of it when it no longer does, so that the reload
// waiting on it then ends superseded.
func TestAcceptingAChangeThatNoLongerWaitsReleasesItsReloads(t *testing.T) {
	u, path := pendingUI(t, []string{"app.local.example.io"})
	home := agentHome(t)
	opts := engine.Options{RunDir: filepath.Join(home, "run")}
	a, note := serveAgents(fakeAgents(home), &opts, path, 443, agentEnv{})
	if a == nil {
		t.Fatal(note)
	}
	defer a.close()
	a.attach(u.Engine)
	a.review(&tui.Proposals{})
	u.agents = a
	sock := filepath.Join(home, ".nearhost", "k3f9q2.sock")
	status, out := postJSON(t, sock, "/v1/projects/reload", `{"path": "/tmp/two"}`)
	if status != 202 {
		t.Fatalf("reload: %d %v", status, out)
	}
	id := out["id"].(string)

	u.Engine.DeclineChange("two", "new")
	if err := u.AcceptChange("two", "new"); err == nil {
		t.Fatal("accepted a change that no longer waits")
	}
	if out := getJSON(t, sock, "/v1/proposals/"+id); out["state"] != "superseded" {
		t.Fatalf("after the refused accept: %v", out)
	}
}
