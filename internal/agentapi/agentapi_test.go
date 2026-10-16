package agentapi

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// shortDir is a temp directory short enough for a Unix socket path: the one
// t.TempDir gives on macOS is not.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "nh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

type fakeSource map[string][]Project

func (f fakeSource) Projects(origin string) []Project { return f[origin] }

type fakeReviewer struct {
	mu   sync.Mutex
	seen []Proposal
}

func (f *fakeReviewer) Review(p Proposal) {
	f.mu.Lock()
	f.seen = append(f.seen, p)
	f.mu.Unlock()
}

func (f *fakeReviewer) all() []Proposal {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Proposal(nil), f.seen...)
}

// clock is a settable clock.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func client(sock string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}}
}

func call(t *testing.T, sock, method, path, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, "http://nearhost"+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client(sock).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("%s %s: not JSON: %q", method, path, data)
	}
	return resp.StatusCode, out
}

var mini = []Project{
	{Name: "myapp", Path: "/home/dev/code/myapp", URL: "https://myapp.test", State: "running", Web: 8080,
		Routes: []Route{{Port: 8080, Role: "web", Label: "app", Applied: true}}},
	{Name: "inner", Path: "/home/dev/code/myapp/packages/inner", URL: "https://inner.test", State: "stopped", Web: 3000},
}

func newServer(t *testing.T, review bool) (*Server, *fakeReviewer, *clock, string, string) {
	t.Helper()
	c := &clock{now: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
	s := New(Options{Laptop: "ecs@macbook", Source: fakeSource{"mini": mini}, Now: c.Now})
	r := &fakeReviewer{}
	if review {
		s.SetReviewer(r)
	}
	dir := shortDir(t)
	local, remote := filepath.Join(dir, "local.sock"), filepath.Join(dir, "mini.sock")
	if err := s.Listen("local", local); err != nil {
		t.Fatal(err)
	}
	if err := s.Listen("mini", remote); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, r, c, local, remote
}

func TestHelloNamesTheOriginAndTheLaptop(t *testing.T) {
	_, _, _, local, remote := newServer(t, true)
	status, out := call(t, remote, "GET", "/v1", "")
	if status != 200 || out["api"] != float64(1) || out["origin"] != "mini" || out["laptop"] != "ecs@macbook" || out["interface"] != true {
		t.Fatalf("GET /v1 on mini: %d %v", status, out)
	}
	if _, out := call(t, local, "GET", "/v1", ""); out["origin"] != "local" {
		t.Fatalf("GET /v1 on local: %v", out)
	}
}

func TestProjectsAreTheOriginsOnly(t *testing.T) {
	_, _, _, local, remote := newServer(t, true)
	_, out := call(t, remote, "GET", "/v1/projects", "")
	if got := out["projects"].([]any); len(got) != 2 {
		t.Fatalf("mini's projects: %v", got)
	}
	_, out = call(t, local, "GET", "/v1/projects", "")
	if got := out["projects"].([]any); len(got) != 0 {
		t.Fatalf("local sees mini's projects: %v", got)
	}
	if status, _ := call(t, local, "GET", "/v1/projects?path=/home/dev/code/myapp", ""); status != 404 {
		t.Fatalf("local found mini's project: %d", status)
	}
}

func TestProjectsByPathFindTheNearestParent(t *testing.T) {
	_, _, _, _, remote := newServer(t, true)
	for path, want := range map[string]string{
		"/home/dev/code/myapp":                        "myapp",
		"/home/dev/code/myapp/src/":                   "myapp",
		"/home/dev/code/myapp/packages/inner/src":     "inner",
		"/home/dev/code/myapp/packages/../packages/x": "myapp",
	} {
		status, out := call(t, remote, "GET", "/v1/projects?path="+path, "")
		if status != 200 || out["name"] != want {
			t.Errorf("%s: %d %v, want %s", path, status, out, want)
		}
	}
	if status, out := call(t, remote, "GET", "/v1/projects?path=/home/dev/code/myapp2", ""); status != 404 || out["error"] == nil {
		t.Errorf("a sibling with a longer name matched: %d %v", status, out)
	}
	if status, _ := call(t, remote, "GET", "/v1/projects?path=code/myapp", ""); status != 400 {
		t.Errorf("a relative path: %d", status)
	}
}

func TestProposeRefusesBadRequests(t *testing.T) {
	_, r, _, _, remote := newServer(t, true)
	for name, body := range map[string]string{
		"relative": `{"path": "code/app"}`,
		"port":     `{"path": "/srv/app", "web": 70000}`,
		"name":     `{"path": "/srv/app", "name": "two\nlines"}`,
		"json":     `{"path": `,
		"big":      `{"path": "/srv/app", "name": "` + strings.Repeat("x", 5000) + `"}`,
	} {
		if status, out := call(t, remote, "POST", "/v1/proposals", body); status != 400 || out["error"] == nil {
			t.Errorf("%s: %d %v", name, status, out)
		}
	}
	if len(r.all()) != 0 {
		t.Fatalf("a bad request reached the reviewer: %v", r.all())
	}
}

func TestProposeAPathAProjectHoldsSaysItExists(t *testing.T) {
	_, r, _, _, remote := newServer(t, true)
	status, out := call(t, remote, "POST", "/v1/proposals", `{"path": "/home/dev/code/myapp/src"}`)
	if status != 200 || out["state"] != "exists" || out["project"].(map[string]any)["url"] != "https://myapp.test" {
		t.Fatalf("%d %v", status, out)
	}
	if len(r.all()) != 0 {
		t.Fatal("an existing project was proposed")
	}
}

func TestProposeWithoutTheInterfaceIsRefused(t *testing.T) {
	_, _, _, _, remote := newServer(t, false)
	status, out := call(t, remote, "POST", "/v1/proposals", `{"path": "/srv/app"}`)
	if status != 409 || !strings.Contains(out["error"].(string), "open nearhost on ecs@macbook") {
		t.Fatalf("%d %v", status, out)
	}
	if _, out := call(t, remote, "GET", "/v1", ""); out["interface"] != false {
		t.Fatalf("GET /v1 says there is an interface: %v", out)
	}
}

func TestProposeReachesTheReviewerAndWaitsForTheAnswer(t *testing.T) {
	s, r, c, _, remote := newServer(t, true)
	status, out := call(t, remote, "POST", "/v1/proposals", `{"path": "/srv/app/", "name": "app", "web": 5173}`)
	if status != 202 || out["expires_at"] != "2026-10-15T12:10:00Z" {
		t.Fatalf("%d %v", status, out)
	}
	id := out["id"].(string)
	seen := r.all()
	if len(seen) != 1 || seen[0].ID != id || seen[0].Origin != "mini" || seen[0].Path != "/srv/app" || seen[0].Name != "app" || seen[0].Web != 5173 {
		t.Fatalf("the reviewer saw %+v", seen)
	}
	if _, out := call(t, remote, "GET", "/v1/proposals/"+id, ""); out["state"] != "pending" {
		t.Fatalf("before the answer: %v", out)
	}

	// The long poll returns as soon as the answer is there.
	go func() {
		time.Sleep(100 * time.Millisecond)
		s.Accept(id, "myapp")
	}()
	start := time.Now()
	_, out = call(t, remote, "GET", "/v1/proposals/"+id+"?wait=30", "")
	if time.Since(start) > 5*time.Second {
		t.Fatal("the long poll did not return early")
	}
	if out["state"] != "accepted" || out["project"].(map[string]any)["url"] != "https://myapp.test" {
		t.Fatalf("after the answer: %v", out)
	}
	// An answer after the first is dropped.
	s.Decline(id, "too late")
	if _, out := call(t, remote, "GET", "/v1/proposals/"+id, ""); out["state"] != "accepted" {
		t.Fatalf("a second answer changed it: %v", out)
	}
	c.add(time.Hour)
	if status, _ := call(t, remote, "GET", "/v1/proposals/"+id, ""); status != 404 {
		t.Fatalf("an hour later it is still there: %d", status)
	}
}

func TestDeclineGivesTheReason(t *testing.T) {
	s, _, _, _, remote := newServer(t, true)
	_, out := call(t, remote, "POST", "/v1/proposals", `{"path": "/srv/app"}`)
	id := out["id"].(string)
	s.Decline(id, "/srv/app: no such directory")
	if _, out := call(t, remote, "GET", "/v1/proposals/"+id+"?wait=5s", ""); out["state"] != "declined" || out["reason"] != "/srv/app: no such directory" {
		t.Fatalf("%v", out)
	}
}

func TestTheLongPollEndsOnTime(t *testing.T) {
	_, _, _, _, remote := newServer(t, true)
	_, out := call(t, remote, "POST", "/v1/proposals", `{"path": "/srv/app"}`)
	start := time.Now()
	_, out = call(t, remote, "GET", "/v1/proposals/"+out["id"].(string)+"?wait=1", "")
	if d := time.Since(start); d < 900*time.Millisecond || d > 5*time.Second || out["state"] != "pending" {
		t.Fatalf("after %s: %v", d, out)
	}
	if status, _ := call(t, remote, "GET", "/v1/proposals/x?wait=soon", ""); status != 400 {
		t.Fatalf("a bad wait: %d", status)
	}
}

func TestProposalsExpireAfterTenMinutes(t *testing.T) {
	s, _, c, _, remote := newServer(t, true)
	_, out := call(t, remote, "POST", "/v1/proposals", `{"path": "/srv/app"}`)
	id := out["id"].(string)
	c.add(10 * time.Minute)
	if _, out := call(t, remote, "GET", "/v1/proposals/"+id, ""); out["state"] != "expired" {
		t.Fatalf("%v", out)
	}
	s.Accept(id, "app") // saved after it expired: the agent was told already
	if _, out := call(t, remote, "GET", "/v1/proposals/"+id, ""); out["state"] != "expired" {
		t.Fatalf("accepting changed it: %v", out)
	}
}

func TestAnOriginMayHaveThreeProposalsWaiting(t *testing.T) {
	s, _, _, local, remote := newServer(t, true)
	var ids []string
	for i := range 3 {
		status, out := call(t, remote, "POST", "/v1/proposals", `{"path": "/srv/app`+string(rune('a'+i))+`"}`)
		if status != 202 {
			t.Fatalf("proposal %d: %d %v", i, status, out)
		}
		ids = append(ids, out["id"].(string))
	}
	if status, _ := call(t, remote, "POST", "/v1/proposals", `{"path": "/srv/appd"}`); status != 429 {
		t.Fatalf("a fourth: %d", status)
	}
	if status, _ := call(t, local, "POST", "/v1/proposals", `{"path": "/srv/app"}`); status != 202 {
		t.Fatalf("another origin's: %d", status)
	}
	s.Decline(ids[0], "")
	if status, _ := call(t, remote, "POST", "/v1/proposals", `{"path": "/srv/appd"}`); status != 202 {
		t.Fatalf("after one was answered: %d", status)
	}
}

func TestAProposalIsOnlyItsOriginsToAskAbout(t *testing.T) {
	_, _, _, local, remote := newServer(t, true)
	_, out := call(t, remote, "POST", "/v1/proposals", `{"path": "/srv/app"}`)
	if status, _ := call(t, local, "GET", "/v1/proposals/"+out["id"].(string), ""); status != 404 {
		t.Fatalf("local read mini's proposal: %d", status)
	}
}

func TestListenReplacesAStaleSocketAndStepsAsideForALiveOne(t *testing.T) {
	dir := shortDir(t)
	path := filepath.Join(dir, "sub", "k3f9q2.sock")
	// A socket file nothing listens on, as a crash leaves it.
	os.MkdirAll(filepath.Dir(path), 0o700)
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()

	a := New(Options{Source: fakeSource{}})
	defer a.Close()
	if err := a.Listen("local", path); err != nil {
		t.Fatalf("over a stale socket: %v", err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %v", info.Mode().Perm())
	}
	if info, _ := os.Stat(filepath.Dir(path)); info.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode %v", info.Mode().Perm())
	}
	b := New(Options{Source: fakeSource{}})
	defer b.Close()
	if err := b.Listen("local", path); err != ErrInUse {
		t.Fatalf("over a live socket: %v", err)
	}
	if status, _ := call(t, path, "GET", "/v1", ""); status != 200 {
		t.Fatal("the first server lost its socket")
	}
	a.Unlisten("local")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Unlisten left the socket: %v", err)
	}
}

func TestUnknownCallsSayWhatToDo(t *testing.T) {
	_, _, _, _, remote := newServer(t, true)
	status, out := call(t, remote, "GET", "/v2/projects", "")
	if status != 404 || !strings.Contains(out["error"].(string), "GET /v1") {
		t.Fatalf("%d %v", status, out)
	}
}
