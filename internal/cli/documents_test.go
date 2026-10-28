package cli

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/agentapi"
	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/documents"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/grants"
	"github.com/nearhost/nearhost/internal/tui"
)

// writeFile writes content to path, making its folders.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// realDir is dir with its symlinks resolved, as the shower gives roots.
func realDir(t *testing.T, dir string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// gitRepo is a temporary git repository holding docs/p.md, notes.txt and
// .superpowers/b/x.html.
func gitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, "docs", "p.md"), "# Plan\n")
	writeFile(t, filepath.Join(repo, "notes.txt"), "notes\n")
	writeFile(t, filepath.Join(repo, ".superpowers", "b", "x.html"), "<p>x</p>\n")
	return repo
}

func TestShowerResolvesLocal(t *testing.T) {
	var eng atomic.Pointer[engine.Engine]
	s := newShower(&eng)
	defer s.pool.Close()
	repo := gitRepo(t)
	real := realDir(t, repo)
	ctx := context.Background()

	got, err := s.Resolve(ctx, config.LocalServer, filepath.Join(repo, "docs", "p.md"), repo)
	if err != nil {
		t.Fatal(err)
	}
	if got.Root != real || got.Doc != "docs/p.md" {
		t.Fatalf("resolved %#v, want root %s", got, real)
	}

	got, err = s.Resolve(ctx, config.LocalServer, filepath.Join(repo, ".superpowers", "b", "x.html"), repo)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(real, ".superpowers", "b"); got.Root != want || got.Doc != "x.html" {
		t.Fatalf("under a dot-folder: %#v, want root %s", got, want)
	}

	if _, err := s.Resolve(ctx, config.LocalServer, filepath.Join(repo, "notes.txt"), repo); !errors.Is(err, agentapi.ErrBadDocument) {
		t.Fatalf("a .txt: %v", err)
	}
	if _, err := s.Resolve(ctx, config.LocalServer, filepath.Join(repo, "docs"), repo); !errors.Is(err, agentapi.ErrBadDocument) {
		t.Fatalf("a folder: %v", err)
	}
	if _, err := s.Resolve(ctx, config.LocalServer, filepath.Join(repo, "gone.md"), repo); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	// A root that isn't there is set aside for the document's folder.
	got, err = s.Resolve(ctx, config.LocalServer, filepath.Join(repo, "docs", "p.md"), filepath.Join(repo, "nope"))
	if err != nil || got.Root != filepath.Join(real, "docs") || got.Doc != "p.md" {
		t.Fatalf("missing root: %#v %v", got, err)
	}
}

func TestShowerRefusesTheHomeFolder(t *testing.T) {
	var eng atomic.Pointer[engine.Engine]
	s := newShower(&eng)
	defer s.pool.Close()
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeFile(t, filepath.Join(home, "p.md"), "# Plan\n")
	_, err := s.Resolve(context.Background(), config.LocalServer, filepath.Join(home, "p.md"), home)
	if !errors.Is(err, agentapi.ErrBadDocument) ||
		err.Error() != "Nearhost won't serve your whole home folder; save the document in a project or a folder of its own" {
		t.Fatalf("home: %v", err)
	}
}

func TestShowerMapsNotConnected(t *testing.T) {
	var eng atomic.Pointer[engine.Engine]
	s := newShower(&eng)
	defer s.pool.Close()
	ctx := context.Background()
	if _, err := s.Resolve(ctx, "mini", "/home/dev/p.md", "/home/dev"); !errors.Is(err, agentapi.ErrNotConnected) {
		t.Fatalf("no engine: %v", err)
	}
	cfg := &config.Config{Servers: []config.Server{{Name: "mini", SSHHost: "mini"}}}
	eng.Store(engine.New(cfg, engine.Options{}))
	if _, err := s.Resolve(ctx, "mini", "/home/dev/p.md", "/home/dev"); !errors.Is(err, agentapi.ErrNotConnected) {
		t.Fatalf("no session: %v", err)
	}
	// The served pages need the documents package's own error, or they
	// show a 502 instead of the page that says the server isn't connected.
	if _, err := s.pool.Open(ctx, "mini"); !errors.Is(err, documents.ErrNotConnected) {
		t.Fatalf("pool: %v", err)
	}
}

func TestShowerShowOpensBrowser(t *testing.T) {
	var eng atomic.Pointer[engine.Engine]
	s := newShower(&eng)
	defer s.pool.Close()
	var opened []string
	s.open = func(url string) error {
		opened = append(opened, url)
		return nil
	}
	ctx := context.Background()
	if _, err := s.Show(ctx, config.LocalServer, "/code/app", "docs/p.md", "claude-code"); err == nil {
		t.Fatal("showed with no engine")
	}
	e := engine.New(&config.Config{}, engine.Options{HTTPSPort: 8443})
	eng.Store(e)
	base, err := e.PageURL()
	if err != nil {
		t.Fatal(err)
	}
	url, err := s.Show(ctx, config.LocalServer, "/code/app", "docs/my plan.md", "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(opened, []string{url}) {
		t.Fatalf("opened %v, want %s once", opened, url)
	}
	if !strings.HasPrefix(url, base+"/d/") || !strings.HasSuffix(url, "/docs/my%20plan.md") {
		t.Fatalf("url %s, base %s", url, base)
	}
	if by, ok := s.Served(config.LocalServer, "/code/app"); !ok || by != "claude-code" {
		t.Fatalf("served %q %v", by, ok)
	}
	if _, ok := s.Served("mini", "/code/app"); ok {
		t.Fatal("served on another server")
	}
}

func TestShowerListsTreesForThePage(t *testing.T) {
	var eng atomic.Pointer[engine.Engine]
	s := newShower(&eng)
	defer s.pool.Close()
	if got := s.homeTrees(); len(got) != 0 {
		t.Fatalf("empty: %v", got)
	}
	tr := s.table.Add("mini", "/code/app", "claude-code", "a.md")
	s.table.Add("mini", "/code/app", "claude-code", "b/c d.md")
	got := s.homeTrees()
	if len(got) != 1 || got[0].Server != "mini" || got[0].Root != "/code/app" || len(got[0].Docs) != 2 {
		t.Fatalf("trees %#v", got)
	}
	if d := got[0].Docs[0]; d.Path != "b/c d.md" || d.URL != "/d/"+tr.ID+"/b/c%20d.md" {
		t.Fatalf("newest doc %#v", d)
	}
}

func TestDocumentsLocalServerIsConfigs(t *testing.T) {
	if documents.LocalServer != config.LocalServer {
		t.Fatalf("documents %q, config %q", documents.LocalServer, config.LocalServer)
	}
}

func TestPruneRemovesTrees(t *testing.T) {
	u, _ := applyRun(t)
	table := u.agents.docs.table
	table.Add("mini", "/code/app", "claude-code", "a.md")
	table.Add("apollo", "/srv/x", "claude-code", "b.md")
	table.Add(config.LocalServer, "/Users/dev/code", "claude-code", "c.md")
	next := &config.Config{Servers: []config.Server{{Name: "apollo", SSHHost: "apollo.example"}}}
	if err := u.Apply(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	if got := table.List("mini"); len(got) != 0 {
		t.Fatalf("mini's trees kept: %v", got)
	}
	if len(table.List("apollo")) != 1 || len(table.List(config.LocalServer)) != 1 {
		t.Fatalf("trees left: %v", table.List(""))
	}
}

// closer counts its closes.
type closer struct{ n atomic.Int32 }

func (c *closer) Close() error {
	c.n.Add(1)
	return nil
}

// fakePool replaces the shower's pool with one whose every dial succeeds,
// and returns the closer each client is closed with.
func fakePool(t *testing.T, s *shower) *closer {
	t.Helper()
	c := &closer{}
	s.pool.Close()
	s.pool = documents.NewPool(func(context.Context, string) (documents.FS, io.Closer, error) {
		return documents.LocalFS{}, c, nil
	}, 0)
	t.Cleanup(func() { s.pool.Close() })
	return c
}

func TestServerGoneDropsItsClient(t *testing.T) {
	home := agentHome(t)
	opts := engine.Options{RunDir: home}
	a, note := serveAgents(withGrants(fakeAgents(home), home), &opts, nil, "", 443, agentEnv{})
	if a == nil {
		t.Fatal(note)
	}
	defer a.close()
	c := fakePool(t, a.docs)
	if _, err := a.docs.pool.Open(context.Background(), "mini"); err != nil {
		t.Fatal(err)
	}
	opts.AgentDone("mini")
	if c.n.Load() != 1 {
		t.Fatalf("closed %d times", c.n.Load())
	}
}

func TestDisconnectDropsTheClient(t *testing.T) {
	home := agentHome(t)
	opts := engine.Options{RunDir: home}
	a, note := serveAgents(withGrants(fakeAgents(home), home), &opts, nil, "", 443, agentEnv{})
	if a == nil {
		t.Fatal(note)
	}
	defer a.close()
	c := fakePool(t, a.docs)
	if _, err := a.docs.pool.Open(context.Background(), "mini"); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Servers: []config.Server{{Name: "mini", SSHHost: "mini"}}}
	eng := engine.New(cfg, opts)
	a.attach(eng)
	// mini has no session, so it reads as disconnected when its session
	// says something.
	deadline := time.Now().Add(5 * time.Second)
	for c.n.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the client was not dropped")
		}
		eng.Events().Publish(engine.Event{Server: "mini", Message: "disconnected"})
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDocumentsAreServedWithGrants(t *testing.T) {
	home := agentHome(t)
	a, note := serveAgents(withGrants(fakeAgents(home), home), &engine.Options{RunDir: home}, nil, "", 443, agentEnv{})
	if a == nil {
		t.Fatal(note)
	}
	defer a.close()
	out := getJSON(t, a.sock, "/v1")
	if !slices.Contains(out["capabilities"].([]any), any("documents")) {
		t.Fatalf("GET /v1: %v", out)
	}
}

func TestReviewerForwardsDocRequest(t *testing.T) {
	var to tui.Proposals
	r := reviewer{&to}
	expires := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	r.ReviewDocument(agentapi.DocRequest{ID: "d1", Origin: "mini", Requester: "claude-code", Session: "s1",
		Label: "plan", Root: "/code/app", Doc: "plans/x.md", CanAlways: true, Expires: expires})
	r.Shown("mini", "claude-code", "plans/y.md")

	_, _, _, docs, shown := to.Waiting()
	if len(docs) != 1 || len(shown) != 1 {
		t.Fatalf("waiting: %d requests, %d shown", len(docs), len(shown))
	}
	got := docs[0]
	if got.ID != "d1" || got.Origin != "mini" || got.Requester != "claude-code" || got.Session != "s1" ||
		got.Label != "plan" || got.Root != "/code/app" || got.Doc != "plans/x.md" || !got.CanAlways || !got.Expires.Equal(expires) {
		t.Fatalf("request: %#v", got)
	}
	if s := shown[0]; s.Origin != "mini" || s.Requester != "claude-code" || s.Doc != "plans/y.md" {
		t.Fatalf("shown: %#v", s)
	}
}

func TestAnswerDocumentMarksAnEndedRequest(t *testing.T) {
	home := agentHome(t)
	a, note := serveAgents(withGrants(fakeAgents(home), home), &engine.Options{RunDir: home}, nil, "", 443, agentEnv{})
	if a == nil {
		t.Fatal(note)
	}
	defer a.close()
	if err := a.answerDocument(context.Background(), "gone", tui.AnswerYes); !errors.Is(err, tui.ErrRequestEnded) {
		t.Fatalf("yes: %v", err)
	}
	if err := a.answerDocument(context.Background(), "gone", tui.AnswerNo); err != nil {
		t.Fatalf("no: %v", err)
	}
	var none *agents
	if err := none.answerDocument(context.Background(), "x", tui.AnswerYes); err == nil {
		t.Fatal("answered without agents")
	}
}

// postDoc asks the agent API on sock to show path, in the tree root, with
// token when it is not empty.
func postDoc(t *testing.T, sock, path, root, token string) (int, map[string]any) {
	t.Helper()
	body := `{"path": "` + path + `", "root": "` + root + `", "requester": "claude-code", "session": "s1"`
	if token != "" {
		body += `, "token": "` + token + `"`
	}
	return postJSON(t, sock, "/v1/documents", body+"}")
}

// Headless `nearhost up` has no one to prompt, so it shows a document only
// with a valid token, and refuses the rest.
func TestUpShowsDocumentsWithAToken(t *testing.T) {
	home := agentHome(t)
	opts := engine.Options{RunDir: filepath.Join(home, "run"), HTTPSPort: 8443}
	a, note := serveAgents(withGrants(fakeAgents(home), home), &opts, nil, "", 8443, agentEnv{})
	if a == nil {
		t.Fatal(note)
	}
	defer a.close() // no a.review: this is `nearhost up`
	a.attach(engine.New(&config.Config{}, opts))
	var opened []string
	a.docs.open = func(url string) error {
		opened = append(opened, url)
		return nil
	}
	repo := gitRepo(t)
	doc := filepath.Join(repo, "docs", "p.md")

	if status, out := postDoc(t, a.sock, doc, repo, ""); status != 409 || !strings.Contains(out["error"].(string), "cannot show prompts") {
		t.Fatalf("no token: %d %v", status, out)
	}
	token, err := a.grants.Allow(config.LocalServer, "claude-code", grants.KindDocuments)
	if err != nil {
		t.Fatal(err)
	}
	status, out := postDoc(t, a.sock, doc, repo, token)
	if status != 201 || out["state"] != "open" {
		t.Fatalf("token: %d %v", status, out)
	}
	if url, _ := out["url"].(string); !slices.Equal(opened, []string{url}) || !strings.HasPrefix(url, "https://localhost:8443/d/") {
		t.Fatalf("url %v, opened %v", out["url"], opened)
	}
	// A passthrough token is not a documents token.
	pass, err := a.grants.Allow(config.LocalServer, "claude-code", grants.KindPassthrough)
	if err != nil {
		t.Fatal(err)
	}
	if status, out := postDoc(t, a.sock, doc, repo, pass); status != 401 {
		t.Fatalf("passthrough token: %d %v", status, out)
	}
}

// The interface lists the served trees, opens a tree's newest document at
// the page's address, and stops serving a tree.
func TestInterfaceOpensAndRemovesTrees(t *testing.T) {
	home := agentHome(t)
	opts := engine.Options{RunDir: filepath.Join(home, "run"), HTTPSPort: 8443}
	a, note := serveAgents(withGrants(fakeAgents(home), home), &opts, nil, "", 8443, agentEnv{})
	if a == nil {
		t.Fatal(note)
	}
	defer a.close()
	var opened []string
	a.docs.open = func(url string) error {
		opened = append(opened, url)
		return nil
	}
	a.docs.table.Add("mini", "/code/app", "claude-code", "spec.md")
	tr := a.docs.table.Add("mini", "/code/app", "claude-code", "plans/a b.md")
	a.docs.table.Add(config.LocalServer, "/Users/dev/app", "codex", "x.md")

	if err := a.openDocument(tr.ID); err == nil {
		t.Fatal("opened a document before the engine started")
	}
	a.attach(engine.New(&config.Config{}, opts))
	if got := a.documents("mini"); len(got) != 1 || got[0].ID != tr.ID {
		t.Fatalf("trees on mini: %#v", got)
	}
	if got := a.documents(""); len(got) != 2 {
		t.Fatalf("every tree: %#v", got)
	}
	if err := a.openDocument(tr.ID); err != nil {
		t.Fatal(err)
	}
	if want := "https://localhost:8443/d/" + tr.ID + "/plans/a%20b.md"; !slices.Equal(opened, []string{want}) {
		t.Fatalf("opened %v, want %s", opened, want)
	}
	a.docs.open = func(string) error { return errors.New("no browser") }
	if err := a.openDocument(tr.ID); err == nil || !strings.Contains(err.Error(), "no browser") {
		t.Fatalf("a browser that fails: %v", err)
	}
	if err := a.removeDocuments(tr.ID); err != nil {
		t.Fatal(err)
	}
	if got := a.documents("mini"); len(got) != 0 {
		t.Fatalf("the removed tree is listed: %#v", got)
	}
	if err := a.openDocument(tr.ID); err == nil {
		t.Fatal("opened a removed tree")
	}

	var none *agents
	if none.documents("") != nil || none.openDocument("x") == nil || none.removeDocuments("x") == nil {
		t.Fatal("documents without agents")
	}
}

// The interface lists grants of both kinds, and revokes one kind alone.
func TestInterfaceGrantsOfEachKind(t *testing.T) {
	home := agentHome(t)
	a, note := serveAgents(withGrants(fakeAgents(home), home), &engine.Options{RunDir: home}, nil, "", 443, agentEnv{})
	if a == nil {
		t.Fatal(note)
	}
	defer a.close()
	for _, kind := range []string{grants.KindPassthrough, grants.KindDocuments} {
		if _, err := a.grants.Allow("mini", "claude-code", kind); err != nil {
			t.Fatal(err)
		}
	}
	got, err := a.grantsOf("mini")
	if err != nil || len(got) != 2 {
		t.Fatalf("grants %#v, %v", got, err)
	}
	if err := a.revokeGrant("mini", "claude-code", grants.KindDocuments); err != nil {
		t.Fatal(err)
	}
	got, err = a.grantsOf("mini")
	if err != nil || len(got) != 1 || got[0].Kind != grants.KindPassthrough {
		t.Fatalf("after revoking documents: %#v, %v", got, err)
	}
}
