package agentapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/doctor"
)

// shortDir is a temp directory short enough for a Unix socket path: the one
// t.TempDir gives on macOS is not.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "nh-")
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

func TestTheResolvedPathAlsoFindsTheProject(t *testing.T) {
	_, r, _, _, remote := newServer(t, true)
	// The agent stands in a symlinked directory; the config holds the real one.
	status, out := call(t, remote, "GET", "/v1/projects?path=/home/dev/link/src&real=/home/dev/code/myapp/src", "")
	if status != 200 || out["name"] != "myapp" {
		t.Fatalf("status by real path: %d %v", status, out)
	}
	status, out = call(t, remote, "POST", "/v1/proposals", `{"path": "/home/dev/link", "real": "/home/dev/code/myapp"}`)
	if status != 200 || out["state"] != "exists" {
		t.Fatalf("propose by real path: %d %v", status, out)
	}
	if status, _ := call(t, remote, "POST", "/v1/proposals", `{"path": "/srv/app", "real": "srv/app"}`); status != 400 {
		t.Fatalf("a relative real path: %d", status)
	}
	if status, _ := call(t, remote, "GET", "/v1/projects?path=/srv/app&real=srv", ""); status != 400 {
		t.Fatalf("a relative real path in status: %d", status)
	}
	_, out = call(t, remote, "POST", "/v1/proposals", `{"path": "/home/dev/link2", "real": "/srv/elsewhere"}`)
	if seen := r.all(); len(seen) != 1 || seen[0].Path != "/home/dev/link2" {
		t.Fatalf("the proposal should carry the path the agent stands in: %+v %v", seen, out)
	}
}

// fakeDoctor records its calls and answers with findings and err.
type fakeDoctor struct {
	mu       sync.Mutex
	calls    []string // origin|name|dir
	findings []doctor.Finding
	err      error
}

func (f *fakeDoctor) Doctor(_ context.Context, origin, name, dir string) ([]doctor.Finding, error) {
	f.mu.Lock()
	f.calls = append(f.calls, origin+"|"+name+"|"+dir)
	f.mu.Unlock()
	return f.findings, f.err
}

func (f *fakeDoctor) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// doctorServer is a server with d as its doctor, serving mini's socket.
func doctorServer(t *testing.T, d Doctor) string {
	t.Helper()
	s := New(Options{Laptop: "ecs@macbook", Source: fakeSource{"mini": mini}, Doctor: d})
	sock := filepath.Join(shortDir(t), "mini.sock")
	if err := s.Listen("mini", sock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return sock
}

func TestHelloListsCapabilities(t *testing.T) {
	_, _, _, _, remote := newServer(t, true)
	_, out := call(t, remote, "GET", "/v1", "")
	if got, ok := out["capabilities"].([]any); !ok || len(got) != 0 || out["api"] != float64(1) {
		t.Fatalf("without a doctor: %v", out)
	}
	_, out = call(t, doctorServer(t, &fakeDoctor{}), "GET", "/v1", "")
	if got, ok := out["capabilities"].([]any); !ok || len(got) != 1 || got[0] != "doctor" || out["api"] != float64(1) {
		t.Fatalf("with a doctor: %v", out)
	}
}

func TestDoctorEndpointForAProject(t *testing.T) {
	d := &fakeDoctor{findings: []doctor.Finding{{
		ID: "routes.not-listening", Severity: doctor.SeverityWarning, Project: "myapp", Message: "nothing listens on 8080",
		Fix: doctor.Fix{Kind: doctor.FixManual, Text: "Start the dev server."},
	}}}
	status, out := call(t, doctorServer(t, d), "GET", "/v1/doctor?path=/home/dev/code/myapp/src", "")
	if status != 200 {
		t.Fatalf("%d %v", status, out)
	}
	if got := d.all(); len(got) != 1 || got[0] != "mini|myapp|/home/dev/code/myapp" {
		t.Fatalf("doctor called with %v", got)
	}
	project, _ := out["project"].(map[string]any)
	findings, _ := out["findings"].([]any)
	if project["name"] != "myapp" || project["url"] != "https://myapp.test" || len(findings) != 1 {
		t.Fatalf("%v", out)
	}
	if f := findings[0].(map[string]any); f["id"] != "routes.not-listening" || f["fix"].(map[string]any)["kind"] != "manual" {
		t.Fatalf("finding %v", f)
	}
}

func TestDoctorEndpointForADirectoryNotAdded(t *testing.T) {
	d := &fakeDoctor{}
	status, out := call(t, doctorServer(t, d), "GET", "/v1/doctor?path=/home/dev/code/other/", "")
	if status != 200 {
		t.Fatalf("%d %v", status, out)
	}
	if got := d.all(); len(got) != 1 || got[0] != "mini||/home/dev/code/other" {
		t.Fatalf("doctor called with %v", got)
	}
	if p, ok := out["project"]; !ok || p != nil {
		t.Fatalf("project %v", out)
	}
	if f, ok := out["findings"].([]any); !ok || len(f) != 0 {
		t.Fatalf("findings %v", out)
	}
}

func TestDoctorEndpointMapsErrors(t *testing.T) {
	d := &fakeDoctor{}
	sock := doctorServer(t, d)
	if status, _ := call(t, sock, "GET", "/v1/doctor?path=code/myapp", ""); status != 400 {
		t.Fatalf("a relative path: %d", status)
	}
	if status, _ := call(t, sock, "GET", "/v1/doctor?path=/srv/app&real=srv", ""); status != 400 {
		t.Fatalf("a relative real: %d", status)
	}
	if len(d.all()) != 0 {
		t.Fatalf("a bad request reached the doctor: %v", d.all())
	}
	d.err = fmt.Errorf("/home/dev/gone: %w", fs.ErrNotExist)
	status, out := call(t, sock, "GET", "/v1/doctor?path=/home/dev/gone", "")
	if status != 404 || out["error"] != "/home/dev/gone is not a directory on mini" {
		t.Fatalf("a missing directory: %d %v", status, out)
	}
	d.err = errors.New("connect to mini.lan: no route to host")
	status, out = call(t, sock, "GET", "/v1/doctor?path=/home/dev/code/myapp", "")
	if status != 502 || out["error"] != "connect to mini.lan: no route to host" {
		t.Fatalf("a server not reached: %d %v", status, out)
	}

	_, _, _, local, _ := newServer(t, true)
	status, out = call(t, local, "GET", "/v1/doctor?path=/Users/ecs/app", "")
	if status != 404 || !strings.Contains(out["error"].(string), "no such call") {
		t.Fatalf("without a doctor: %d %v", status, out)
	}
}

func TestADirectoryMissingOnTheLaptopNamesIt(t *testing.T) {
	d := &fakeDoctor{err: fs.ErrNotExist}
	s := New(Options{Laptop: "ecs@macbook", Source: fakeSource{}, Doctor: d})
	sock := filepath.Join(shortDir(t), "local.sock")
	if err := s.Listen("local", sock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	status, out := call(t, sock, "GET", "/v1/doctor?path=/Users/ecs/gone", "")
	if status != 404 || out["error"] != "/Users/ecs/gone is not a directory on ecs@macbook" {
		t.Fatalf("%d %v", status, out)
	}
}

// liveSource is a Source whose projects a test can change.
type liveSource struct {
	mu       sync.Mutex
	projects map[string][]Project
}

func (l *liveSource) Projects(origin string) []Project {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Project(nil), l.projects[origin]...)
}

func (l *liveSource) setState(origin, name, state string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.projects[origin] {
		if l.projects[origin][i].Name == name {
			l.projects[origin][i].State = state
		}
	}
}

// fakeReloader records the projects it reloads and answers with got and err,
// calling then first when it is set. The change waiting is the one got
// found, unless wait sets another.
type fakeReloader struct {
	mu      sync.Mutex
	calls   []string
	got     Reloaded
	err     error
	then    func(name string)
	waiting *string
}

func (f *fakeReloader) Waiting(string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.waiting != nil {
		return *f.waiting
	}
	return f.got.SHA256
}

// wait has sha be the change waiting for approval, "" for none.
func (f *fakeReloader) wait(sha string) {
	f.mu.Lock()
	f.waiting = &sha
	f.mu.Unlock()
}

func (f *fakeReloader) Reload(_ context.Context, name string) (Reloaded, error) {
	f.mu.Lock()
	f.calls = append(f.calls, name)
	got, err, then := f.got, f.err, f.then
	f.mu.Unlock()
	if then != nil {
		then(name)
	}
	return got, err
}

func (f *fakeReloader) answer(got Reloaded, err error) {
	f.mu.Lock()
	f.got, f.err = got, err
	f.mu.Unlock()
}

func (f *fakeReloader) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// reloadServer is a server with a fake Reloader, serving mini's socket.
// mini has a third project, so three reloads can wait at once.
func reloadServer(t *testing.T, review bool) (*Server, *fakeReviewer, *fakeReloader, *liveSource, string) {
	t.Helper()
	src := &liveSource{projects: map[string][]Project{"mini": append(append([]Project(nil), mini...),
		Project{Name: "third", Path: "/home/dev/code/third", URL: "https://third.test", State: "stopped"})}}
	rl := &fakeReloader{}
	c := &clock{now: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
	s := New(Options{Laptop: "ecs@macbook", Source: src, Reload: rl, Now: c.Now})
	r := &fakeReviewer{}
	if review {
		s.SetReviewer(r)
	}
	sock := filepath.Join(shortDir(t), "mini.sock")
	if err := s.Listen("mini", sock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, r, rl, src, sock
}

var waiting = Reloaded{Items: []string{"app.example.com", "docker_clients"}, SHA256: "b"}

func TestReloadAnswers(t *testing.T) {
	_, _, _, _, remote := newServer(t, true)
	if status, out := call(t, remote, "POST", "/v1/projects/reload", `{"path": "/home/dev/code/myapp"}`); status != 404 ||
		!strings.Contains(out["error"].(string), "no such call") {
		t.Fatalf("without a reloader: %d %v", status, out)
	}

	_, r, rl, src, sock := reloadServer(t, true)
	if _, out := call(t, sock, "GET", "/v1", ""); fmt.Sprint(out["capabilities"]) != "[reload]" {
		t.Fatalf("GET /v1: %v", out)
	}
	for name, body := range map[string]string{
		"relative": `{"path": "code/myapp"}`,
		"real":     `{"path": "/home/dev/code/myapp", "real": "code/myapp"}`,
		"json":     `{"path": `,
	} {
		if status, _ := call(t, sock, "POST", "/v1/projects/reload", body); status != 400 {
			t.Errorf("%s: %d", name, status)
		}
	}
	status, out := call(t, sock, "POST", "/v1/projects/reload", `{"path": "/home/dev/code/other/"}`)
	if status != 404 || out["error"] != "no project holds /home/dev/code/other; add it first" {
		t.Fatalf("no project: %d %v", status, out)
	}
	if len(rl.all()) != 0 {
		t.Fatalf("reloaded without a project: %v", rl.all())
	}

	rl.answer(Reloaded{}, errors.New("server \"mini\" is not connected"))
	status, out = call(t, sock, "POST", "/v1/projects/reload", `{"path": "/home/dev/code/myapp/src"}`)
	if status != 502 || out["error"] != `server "mini" is not connected` {
		t.Fatalf("an error: %d %v", status, out)
	}
	if got := rl.all(); len(got) != 1 || got[0] != "myapp" {
		t.Fatalf("reloaded %v", got)
	}

	rl.answer(Reloaded{Problem: ".nearhostrc: line 3: unknown key \"hsts\""}, nil)
	status, out = call(t, sock, "POST", "/v1/projects/reload", `{"path": "/home/dev/code/myapp"}`)
	if status != 422 || out["error"] != `.nearhostrc: line 3: unknown key "hsts"` {
		t.Fatalf("a problem: %d %v", status, out)
	}

	rl.answer(Reloaded{Declined: true}, nil)
	status, out = call(t, sock, "POST", "/v1/projects/reload", `{"path": "/home/dev/code/myapp"}`)
	if status != 200 || out["state"] != "declined" || out["reason"] != "the user declined this change; change .nearhostrc to ask again" {
		t.Fatalf("declined: %d %v", status, out)
	}

	// The project answered is as it is after the reload.
	rl.answer(Reloaded{}, nil)
	rl.then = func(name string) { src.setState("mini", name, "starting") }
	status, out = call(t, sock, "POST", "/v1/projects/reload", `{"path": "/home/dev/link", "real": "/home/dev/code/myapp"}`)
	if p, _ := out["project"].(map[string]any); status != 200 || out["state"] != "applied" || p["name"] != "myapp" || p["state"] != "starting" {
		t.Fatalf("applied: %d %v", status, out)
	}
	rl.then = nil

	rl.answer(waiting, nil)
	status, out = call(t, sock, "POST", "/v1/projects/reload", `{"path": "/home/dev/code/myapp"}`)
	if status != 202 || out["state"] != "pending" || out["id"] == nil || out["expires_at"] != "2026-10-15T12:10:00Z" ||
		fmt.Sprint(out["items"]) != "[app.example.com docker_clients]" {
		t.Fatalf("waiting: %d %v", status, out)
	}
	// It waits for the user.
	if _, out := call(t, sock, "GET", "/v1/proposals/"+out["id"].(string)+"?wait=1", ""); out["state"] != "pending" {
		t.Fatalf("before an answer: %v", out)
	}
	if len(r.all()) != 0 {
		t.Fatalf("the reviewer was shown a reload: %v", r.all())
	}

	_, _, rl, _, sock = reloadServer(t, false)
	rl.answer(waiting, nil)
	status, out = call(t, sock, "POST", "/v1/projects/reload", `{"path": "/home/dev/code/myapp"}`)
	if status != 409 || out["error"] != "open nearhost on ecs@macbook to approve what .nearhostrc asks for (app.example.com, docker_clients); `nearhost up` cannot show it" ||
		fmt.Sprint(out["items"]) != "[app.example.com docker_clients]" {
		t.Fatalf("without the interface: %d %v", status, out)
	}
}

func TestReloadIsAnsweredByTheUser(t *testing.T) {
	s, r, rl, _, sock := reloadServer(t, true)
	rl.answer(waiting, nil)
	reload := func(path string) string {
		t.Helper()
		status, out := call(t, sock, "POST", "/v1/projects/reload", `{"path": "`+path+`"}`)
		if status != 202 {
			t.Fatalf("%s: %d %v", path, status, out)
		}
		return out["id"].(string)
	}
	myapp, inner, third := reload("/home/dev/code/myapp"), reload("/home/dev/code/myapp/packages/inner"), reload("/home/dev/code/third")

	// Waiting reloads are not proposals: an agent may still propose three.
	for i := range 3 {
		if status, out := call(t, sock, "POST", "/v1/proposals", `{"path": "/srv/app`+string(rune('a'+i))+`"}`); status != 202 {
			t.Fatalf("proposal %d: %d %v", i, status, out)
		}
	}
	if seen := r.all(); len(seen) != 3 {
		t.Fatalf("the reviewer saw %+v", seen)
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		s.ChangeAnswered("myapp", "b", true)
	}()
	_, out := call(t, sock, "GET", "/v1/proposals/"+myapp+"?wait=5", "")
	if p, _ := out["project"].(map[string]any); out["state"] != "accepted" || p["name"] != "myapp" || p["url"] != "https://myapp.test" {
		t.Fatalf("approved: %v", out)
	}

	s.ChangeAnswered("inner", "b", false)
	if _, out := call(t, sock, "GET", "/v1/proposals/"+inner+"?wait=5", ""); out["state"] != "declined" {
		t.Fatalf("declined: %v", out)
	}
	if _, out := call(t, sock, "GET", "/v1/proposals/"+third, ""); out["state"] != "pending" {
		t.Fatalf("an answer for another project ended it: %v", out)
	}
	if seen := r.all(); len(seen) != 3 {
		t.Fatalf("the reviewer was shown a reload: %+v", seen)
	}
}

func TestReloadReusesOrSupersedesItsRecord(t *testing.T) {
	_, _, rl, _, sock := reloadServer(t, true)
	rl.answer(waiting, nil)
	_, first := call(t, sock, "POST", "/v1/projects/reload", `{"path": "/home/dev/code/myapp"}`)
	_, again := call(t, sock, "POST", "/v1/projects/reload", `{"path": "/home/dev/code/myapp/src"}`)
	if first["id"] == nil || again["id"] != first["id"] {
		t.Fatalf("the same change twice: %v then %v", first, again)
	}

	rl.answer(Reloaded{Items: []string{"other.example.com"}, SHA256: "c"}, nil)
	status, changed := call(t, sock, "POST", "/v1/projects/reload", `{"path": "/home/dev/code/myapp"}`)
	if status != 202 || changed["id"] == first["id"] || fmt.Sprint(changed["items"]) != "[other.example.com]" {
		t.Fatalf("a changed file: %d %v", status, changed)
	}
	if _, out := call(t, sock, "GET", "/v1/proposals/"+first["id"].(string), ""); out["state"] != "superseded" {
		t.Fatalf("the first: %v", out)
	}
	if _, out := call(t, sock, "GET", "/v1/proposals/"+changed["id"].(string), ""); out["state"] != "pending" {
		t.Fatalf("the second: %v", out)
	}
}

// The user answering a newer change of the file ends a reload waiting on an
// older one, which a reload again then finds as it is.
func TestAnAnswerToANewerChangeSupersedesTheReload(t *testing.T) {
	s, _, rl, _, sock := reloadServer(t, true)
	rl.answer(waiting, nil)
	_, out := call(t, sock, "POST", "/v1/projects/reload", `{"path": "/home/dev/code/myapp"}`)
	myapp := out["id"].(string)
	_, out = call(t, sock, "POST", "/v1/projects/reload", `{"path": "/home/dev/code/third"}`)
	third := out["id"].(string)

	// Waiting still says "b": the answer alone ends it.
	go func() {
		time.Sleep(100 * time.Millisecond)
		s.ChangeAnswered("myapp", "c", true)
	}()
	if _, out := call(t, sock, "GET", "/v1/proposals/"+myapp+"?wait=5", ""); out["state"] != "superseded" {
		t.Fatalf("answered a newer change: %v", out)
	}
	if _, out := call(t, sock, "GET", "/v1/proposals/"+third, ""); out["state"] != "pending" {
		t.Fatalf("an answer for another project ended it: %v", out)
	}
}

// A reload whose change no longer waits, because the file changed back or
// was approved elsewhere, ends superseded rather than waiting until it
// expires.
func TestAReloadWhoseChangeNoLongerWaitsIsSuperseded(t *testing.T) {
	_, _, rl, _, sock := reloadServer(t, true)
	rl.answer(waiting, nil)
	_, out := call(t, sock, "POST", "/v1/projects/reload", `{"path": "/home/dev/code/myapp"}`)
	id := out["id"].(string)
	if _, out := call(t, sock, "GET", "/v1/proposals/"+id, ""); out["state"] != "pending" {
		t.Fatalf("still waiting: %v", out)
	}
	rl.wait("")
	if _, out := call(t, sock, "GET", "/v1/proposals/"+id, ""); out["state"] != "superseded" {
		t.Fatalf("the file changed back: %v", out)
	}

	// A long poll asks again when it wakes.
	rl.answer(Reloaded{Items: []string{"other.example.com"}, SHA256: "c"}, nil)
	rl.wait("c")
	_, out = call(t, sock, "POST", "/v1/projects/reload", `{"path": "/home/dev/code/myapp"}`)
	id = out["id"].(string)
	go func() {
		time.Sleep(100 * time.Millisecond)
		rl.wait("d")
	}()
	if _, out := call(t, sock, "GET", "/v1/proposals/"+id+"?wait=1", ""); out["state"] != "superseded" {
		t.Fatalf("the file changed during the poll: %v", out)
	}
}

// An answer to the change a reload waits on stands, though the change no
// longer waits once it is answered.
func TestAnAnsweredReloadStaysAnswered(t *testing.T) {
	for _, approved := range []bool{true, false} {
		s, _, rl, _, sock := reloadServer(t, true)
		rl.answer(waiting, nil)
		_, out := call(t, sock, "POST", "/v1/projects/reload", `{"path": "/home/dev/code/myapp"}`)
		id := out["id"].(string)
		s.ChangeAnswered("myapp", "b", approved)
		rl.wait("")
		want := "declined"
		if approved {
			want = "accepted"
		}
		if _, out := call(t, sock, "GET", "/v1/proposals/"+id+"?wait=5", ""); out["state"] != want {
			t.Fatalf("approved %v: %v", approved, out)
		}
	}
}
