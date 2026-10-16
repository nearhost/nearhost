package cli

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
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
	a, note := serveAgents(agentsBackend{}, &opts, "", 443)
	if a != nil || note != "" || opts.Agent != nil {
		t.Fatalf("%v %q", a, note)
	}
	a.attach(nil) // nil agents do nothing
	a.close()
}

func TestAgentsListenOnTheLaptopsSocketAndForwardEachServer(t *testing.T) {
	home := agentHome(t)
	opts := engine.Options{RunDir: filepath.Join(home, "run")}
	a, note := serveAgents(fakeAgents(home), &opts, filepath.Join(home, "config.yaml"), 443)
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
	first, _ := serveAgents(fakeAgents(home), &engine.Options{RunDir: home}, "", 443)
	defer first.close()
	var opts engine.Options
	second, note := serveAgents(fakeAgents(home), &opts, "", 443)
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
	a, _ := serveAgents(fakeAgents(home), &opts, filepath.Join(home, "config.yaml"), 443)
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
	a, _ := serveAgents(fakeAgents(home), &opts, filepath.Join(home, "config.yaml"), 443)
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
