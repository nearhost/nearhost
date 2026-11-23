package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nearhost/nearhost/internal/agentapi"
	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/discovery"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/nearhostrc"
	"github.com/nearhost/nearhost/internal/testutil"
	"github.com/nearhost/nearhost/internal/tui"
)

// pendingOps is the core's side of the interface, whose config file at path
// has enabled
// "one" serving x.local.example.io and "two" serving two.local.example.io,
// and whose engine has a change pending for "two" that serves next's hosts.
func pendingOps(t *testing.T, next []string) (*coreOps, string) {
	t.Helper()
	one := localProject("one", 8001, true)
	one.Routes[0].Hosts = []string{"x.local.example.io"}
	two := localProject("two", 8002, true)
	two.Routes[0].Hosts = []string{"two.local.example.io"}
	cfg := &config.Config{Projects: []config.Project{one, two}}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	changed := two
	changed.Routes = []config.Route{{Port: 8002, Role: config.RoleWeb, Hosts: next}}
	changed.Nearhostrc = &config.RcSnapshot{SHA256: "new", Routes: changed.Routes}
	var items []nearhostrc.Item
	for _, h := range next {
		items = append(items, nearhostrc.Item{Port: 8002, Host: h, Why: "a real domain"})
	}
	review := func(context.Context, config.Project, discovery.FileReader) engine.Review {
		return engine.Review{Pending: &engine.Change{SHA256: "new", Project: changed, Items: items}}
	}
	u := &coreOps{eng: engine.New(cfg, engine.Options{Review: review}), path: path}
	if err := u.Recheck(context.Background(), "two"); err != nil {
		t.Fatal(err)
	}
	if u.eng.Status("two").Pending == nil {
		t.Fatal("nothing pending after the recheck")
	}
	return u, path
}

func TestAcceptRefusesAHostAnotherProjectServes(t *testing.T) {
	u, path := pendingOps(t, []string{"x.local.example.io"})
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	err = u.AcceptChange("two", "new")
	if want := `host x.local.example.io is already served by project "one"`; err == nil || err.Error() != want {
		t.Fatalf("AcceptChange = %v, want %s", err, want)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Errorf("the config changed:\n%s", after)
	}
}

// Accepting saves the change's routes and snapshot, and keeps the rest of
// the project as the file has it now.
func TestAcceptSavesOnlyTheRoutesAndSnapshot(t *testing.T) {
	u, path := pendingOps(t, []string{"app.local.example.io"})
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Projects[1].Path = "/tmp/moved"
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if err := u.AcceptChange("two", "new"); err != nil {
		t.Fatal(err)
	}
	cfg, err = config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := cfg.Project("two")
	if p.Path != "/tmp/moved" || p.Routes[0].Hosts[0] != "app.local.example.io" || p.Nearhostrc == nil || p.Nearhostrc.SHA256 != "new" {
		t.Errorf("saved %+v", p)
	}
}

// Declining drops the pending change.
func TestDeclineDropsTheChange(t *testing.T) {
	u, _ := pendingOps(t, []string{"app.local.example.io"})
	u.DeclineChange("two", "new")
	if st := u.eng.Status("two"); st.Pending != nil {
		t.Errorf("still pending: %+v", st.Pending)
	}
}

// A change other than the one pending now is refused, and nothing is saved.
func TestAcceptRefusesAStaleChange(t *testing.T) {
	u, path := pendingOps(t, []string{"app.local.example.io"})
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	err = u.AcceptChange("two", "older")
	if want := "the .nearhostrc changed again; review it again"; err == nil || err.Error() != want {
		t.Fatalf("AcceptChange = %v, want %s", err, want)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Errorf("the config changed:\n%s", after)
	}
}

// A newer .nearhostrc that a review saved while the accept waited for the
// config's lock is not overwritten with the change the user approved.
func TestAcceptRefusesAChangeANewerReviewSaved(t *testing.T) {
	u, path := pendingOps(t, []string{"app.local.example.io"})
	var before []byte
	// The review saves after the accept checked the change and before it
	// holds the lock.
	u.acceptLocking = func() {
		if err := config.Update(path, func(cfg *config.Config) error {
			p, _ := cfg.Project("two")
			p.Nearhostrc = &config.RcSnapshot{SHA256: "newer", Routes: p.Routes}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		var err error
		if before, err = os.ReadFile(path); err != nil {
			t.Fatal(err)
		}
	}
	err := u.AcceptChange("two", "new")
	if want := "the .nearhostrc changed again; review it again"; err == nil || err.Error() != want {
		t.Fatalf("AcceptChange = %v, want %s", err, want)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Errorf("the config changed:\n%s", after)
	}
}

// An accept through the control API reaches the core's AcceptChange, and a
// stale one comes back with the core's reason.
func TestAcceptChangeThroughTheCore(t *testing.T) {
	u, path := pendingOps(t, []string{"app.local.example.io"})
	runDir := testutil.TempDir(t, "nh")
	ctl, err := control.Listen(control.SocketPath(runDir), &coreBackend{coreOps: u, ctx: context.Background()}, control.NewHub())
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	c, err := control.Dial(runDir)
	if err != nil {
		t.Fatal(err)
	}
	err = c.AcceptChange(context.Background(), "two", "older")
	if want := "the .nearhostrc changed again; review it again"; err == nil || err.Error() != want {
		t.Fatalf("a stale accept = %v, want %s", err, want)
	}
	if err := c.AcceptChange(context.Background(), "two", "new"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := cfg.Project("two"); p.Nearhostrc == nil || p.Nearhostrc.SHA256 != "new" {
		t.Errorf("saved %+v", p)
	}
}

// A Reload and the watcher's Apply never apply at the same time.
func TestReloadAndWatcherTakeTurns(t *testing.T) {
	fastWatch(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := &config.Config{Projects: []config.Project{localProject("a", 8001, false)}}
	saveConfig(t, path, cfg)
	o := &coreOps{eng: engine.New(cfg, engine.Options{}), path: path}
	var mu sync.Mutex
	in, most, entries := 0, 0, 0
	o.applied = func() {
		mu.Lock()
		in++
		entries++
		most = max(most, in)
		mu.Unlock()
		// The first in waits a while for the other, which comes in unless
		// they take turns.
		for end := time.Now().Add(300 * time.Millisecond); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
			mu.Lock()
			both := in > 1
			mu.Unlock()
			if both {
				break
			}
		}
		mu.Lock()
		in--
		mu.Unlock()
	}
	w := config.NewWatcher(path) // before the change, as the core makes it
	ctx, cancel := context.WithCancel(context.Background())
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		watchConfig(ctx, o, w)
	}()
	defer func() {
		cancel()
		<-watched
	}()
	// The watcher applies the change once it has settled, while the
	// Reload waits for it.
	cfg.Projects[0].Path = "/tmp/moved"
	saveConfig(t, path, cfg)
	if err := o.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	testutil.WaitUntil(t, 10*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return entries == 2 && in == 0
	}, "the watcher to apply the change")
	if most != 1 {
		t.Errorf("%d applies at once; want one at a time", most)
	}
}

// A Reload of a file that does not load applies nothing and says why.
func TestReloadRefusesABadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := &config.Config{Projects: []config.Project{localProject("a", 8001, false)}}
	o := &coreOps{eng: engine.New(cfg, engine.Options{}), path: path}
	if err := os.WriteFile(path, []byte("projects: [oops\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	called := false
	o.applied = func() { called = true }
	if err := o.Reload(context.Background()); err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("Reload = %v, want an error that names %s", err, path)
	}
	if called {
		t.Error("a file that does not parse was applied")
	}
}

func TestAnswerOf(t *testing.T) {
	for s, want := range map[string]tui.Answer{
		"yes": tui.AnswerYes, "no": tui.AnswerNo, "always": tui.AnswerAlways, "close": tui.AnswerClose,
	} {
		if got, err := answerOf(s); err != nil || got != want {
			t.Errorf("answerOf(%q) = %v, %v; want %v", s, got, err, want)
		}
	}
	if _, err := answerOf("maybe"); err == nil {
		t.Error("answerOf took maybe")
	}
}

// An answer to a request that has ended says so to the control API, in the
// agent API's words.
func TestAnswerEndedReachesTheControlAPI(t *testing.T) {
	ended := tui.RequestEnded(errors.New("that request has expired"))
	err := controlAnswerError(ended)
	if !errors.Is(err, control.ErrRequestEnded) || err.Error() != "that request has expired" {
		t.Errorf("ended: %v", err)
	}
	other := errors.New("no coding agent can reach Nearhost")
	if err := controlAnswerError(other); errors.Is(err, control.ErrRequestEnded) || err != other {
		t.Errorf("other: %v", err)
	}
	if err := controlAnswerError(nil); err != nil {
		t.Errorf("nil: %v", err)
	}
	o := &coreOps{}
	if err := o.AnswerPassthrough(context.Background(), "p1", "maybe"); err == nil {
		t.Error("AnswerPassthrough took maybe")
	}
}

// An Always allow that was not saved says so to the control API, alone or
// with the end of its request.
func TestAllowNotSavedReachesTheControlAPI(t *testing.T) {
	notSavedErr := notSaved{errors.New("opened, but the grant could not be saved")}
	err := controlAnswerError(notSavedErr)
	if !errors.Is(err, control.ErrAllowNotSaved) || errors.Is(err, control.ErrRequestEnded) || err.Error() != notSavedErr.Error() {
		t.Errorf("not saved: %v", err)
	}
	both := tui.RequestEnded(notSavedErr)
	err = controlAnswerError(both)
	if !errors.Is(err, control.ErrAllowNotSaved) || !errors.Is(err, control.ErrRequestEnded) || !errors.Is(err, tui.ErrAllowNotSaved) {
		t.Errorf("both: %v", err)
	}
}

// The page's address comes with its key, for the client to open.
func TestPageKeyURL(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	o := &coreOps{eng: engine.New(&config.Config{}, engine.Options{HTTPSPort: 8443, WebPort: 6327})}
	got, err := o.PageKeyURL()
	if err != nil {
		t.Fatal(err)
	}
	secret, err := os.ReadFile(filepath.Join(data, "nearhost", "page-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://nearhost.localhost:6327/?k=" + string(secret); got != want {
		t.Errorf("PageKeyURL = %q, want the page with its key", got)
	}
}

// A message too long for the stream is cut, so no client's stream breaks.
func TestCutMessage(t *testing.T) {
	short := engine.Event{Project: "a", Message: "hello"}
	if got := cutMessage(short); got != short {
		t.Errorf("a short message changed: %+v", got)
	}
	long := strings.Repeat("é", maxEventMessage) // two bytes each
	got := cutMessage(engine.Event{Message: long})
	if len(got.Message) > maxEventMessage || !strings.HasSuffix(got.Message, "…") || !strings.HasPrefix(long, strings.TrimSuffix(got.Message, "…")) {
		t.Errorf("cut to %d bytes, ending %q", len(got.Message), got.Message[len(got.Message)-8:])
	}
	if !utf8.ValidString(got.Message) {
		t.Error("the cut split a character")
	}
}

// The state lists the grants on this machine and on every configured server.
func TestCoreStateListsGrants(t *testing.T) {
	u, home := applyRun(t)
	grantTo(t, grantFile(home), config.LocalServer)
	st := u.State()
	if st.GrantsError != "" {
		t.Fatalf("GrantsError = %q", st.GrantsError)
	}
	var servers []string
	for _, g := range st.Grants {
		servers = append(servers, g.Server)
	}
	slices.Sort(servers)
	if want := []string{"apollo", config.LocalServer, "mini"}; !slices.Equal(servers, want) {
		t.Errorf("grants on %v, want %v", servers, want)
	}
}

// Status lists the queue's requests, then the changes that wait, each with its
// link on the page, or without one when the page is not served.
func TestCoreStatusListsWhatWaits(t *testing.T) {
	u, _ := pendingOps(t, []string{"shop.example.com", "api.example.com"})
	u.approvals = newApprovals(control.NewHub(), time.Now)
	t.Cleanup(u.approvals.close)
	u.approvals.ReviewPassthrough(agentapi.PassRequest{ID: "p1", Origin: "mini", Requester: "claude", Remote: 5173, Expires: time.Now().Add(time.Hour)})
	b := &coreBackend{coreOps: u, ctx: context.Background()}

	st := b.Status()
	if st.Page != "" || len(st.Approvals) != 2 || st.Approvals[0].ID != "p1" || st.Approvals[0].URL != "" {
		t.Fatalf("without a page: %+v", st)
	}
	if want := "two's .nearhostrc asks for shop.example.com, api.example.com"; st.Approvals[1].Summary != want {
		t.Errorf("change summary = %q, want %q", st.Approvals[1].Summary, want)
	}

	u.eng = engine.New(u.eng.Config(), engine.Options{HTTPSPort: 8443, WebPort: 6327, Review: nil})
	if st := b.Status(); st.Page != "https://nearhost.localhost:6327" {
		t.Errorf("page = %q", st.Page)
	}
}
