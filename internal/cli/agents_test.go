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
	if !ok || f.Name != "k3f9q2.sock" || !strings.HasPrefix(f.Local, opts.RunDir+"/agent-") {
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
