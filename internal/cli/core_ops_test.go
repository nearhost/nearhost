package cli

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/testutil"
	"github.com/nearhost/nearhost/internal/tui"
)

// pendingOps is the core's side of the interface, whose config file at path
// has enabled
// "one" serving x.local.example.io and "two" serving two.local.example.io,
// and whose engine has a change pending for "two" that serves next's hosts.
func pendingOps(t *testing.T, next []string) (*coreOps, string) {
	t.Helper()
	return pendingOpsOn(t, next, 0)
}

// pendingOpsOn is pendingOps with Nearhost's page served on webPort, or not
// when it is 0.
func pendingOpsOn(t *testing.T, next []string, webPort int) (*coreOps, string) {
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
	u := &coreOps{eng: engine.New(cfg, engine.Options{Review: review, WebPort: webPort}), path: path}
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
	status := func(webPort int) control.Status {
		u, _ := pendingOpsOn(t, []string{"shop.example.com", "api.example.com"}, webPort)
		u.approvals = newApprovals(control.NewHub(), time.Now)
		t.Cleanup(u.approvals.close)
		u.approvals.ReviewPassthrough(agentapi.PassRequest{ID: "p1", Origin: "mini", Requester: "claude", Remote: 5173, Expires: time.Now().Add(time.Hour)})
		b := &coreBackend{coreOps: u, ctx: context.Background()}
		return b.Status()
	}

	st := status(0)
	if st.Page != "" || len(st.Approvals) != 2 || st.Approvals[0].ID != "p1" || st.Approvals[0].URL != "" || st.Approvals[1].URL != "" {
		t.Fatalf("without a page: %+v", st)
	}
	if want := "two's .nearhostrc asks for shop.example.com, api.example.com"; st.Approvals[1].Summary != want {
		t.Errorf("change summary = %q, want %q", st.Approvals[1].Summary, want)
	}

	st = status(6327)
	if st.Page != "https://nearhost.localhost:6327" || len(st.Approvals) != 2 {
		t.Fatalf("with the page: %+v", st)
	}
	if got, want := st.Approvals[0].URL, "https://nearhost.localhost:6327/approve/p1"; got != want {
		t.Errorf("passthrough link = %q, want %q", got, want)
	}
	if got, want := st.Approvals[1].URL, "https://nearhost.localhost:6327/change/two"; got != want {
		t.Errorf("change link = %q, want %q", got, want)
	}
}

// A report too long for the stream is cut as its message is, and one whose
// data does not marshal still goes out, so no client's stream breaks.
func TestCutMessageCutsTheReport(t *testing.T) {
	long := strings.Repeat("\x01", 4*maxEventMessage) // escaped to six bytes each
	r := report.Warnf(report.DockerUnreachable, "%s", long).Because("%s", long).
		Quote(long, long).Step(long, long).With("bad", func() {})
	ev := cutMessage(engine.Event{Project: "web", Message: long, Report: r})
	if ev.Report == nil {
		t.Fatal("the report was dropped")
	}
	if ev.Report == r || len(r.Title) != 4*maxEventMessage {
		t.Error("the engine's report was changed in place")
	}
	if len(ev.Report.Title) > maxEventMessage || !strings.HasSuffix(ev.Report.Title, "…") {
		t.Errorf("title cut to %d bytes", len(ev.Report.Title))
	}
	if len(ev.Report.Why) > maxEventMessage {
		t.Errorf("why cut to %d bytes", len(ev.Report.Why))
	}
	data, err := json.Marshal(control.Envelope{Seq: 1, Kind: control.KindEvent, Event: &ev})
	if err != nil {
		t.Fatalf("the envelope does not marshal: %v", err)
	}
	if len(data) > 1<<20 {
		t.Errorf("the envelope is %d bytes, over what a client reads", len(data))
	}
	short := report.Warnf(report.DockerUnreachable, "Docker did not answer.").With("project", "web")
	if got := cutMessage(engine.Event{Message: "x", Report: short}); got.Report != short {
		t.Errorf("a short report changed: %+v", got.Report)
	}
}

// Why grants cannot be read is a message.
func TestGrantsErrIsAMessage(t *testing.T) {
	err := (*agents)(nil).grantsErr()
	m, ok := report.As(err)
	if !ok || m.Code != report.AgentsGrantsOff {
		t.Fatalf("grantsErr = %#v, want an agents.grants-off message", err)
	}
}

// A cut to fewer bytes than the ellipsis takes gives nothing, not a panic.
func TestCutTextShort(t *testing.T) {
	for n, want := range []string{"", "", "", "…", "h…"} {
		if got := cutText("hello", n); got != want {
			t.Errorf("cutText(hello, %d) = %q, want %q", n, got, want)
		}
	}
	lines := report.Warnf(report.DockerUnreachable, "x").Quote(strings.Repeat("a", maxEventMessage-1), "bc", "d")
	r := cutReport(lines)
	if r == nil || len(strings.Join(r.Verbatim, "")) > maxEventMessage {
		t.Errorf("quoted lines = %d bytes", len(strings.Join(r.Verbatim, "")))
	}
}

// The state gives each project's server and the addresses of its web routes,
// the primary first, as Status gives them for the primary.
func TestCoreStateSites(t *testing.T) {
	two := localProject("shop", 3000, true)
	two.Routes = append(two.Routes, config.Route{Port: 4000, Role: config.RoleWeb, Hosts: []string{"admin.shop.test"}}, config.Route{Port: 5432, Role: config.RoleLoopback})
	none := localProject("db", 5432, true)
	none.Routes = []config.Route{{Port: 5432, Role: config.RoleLoopback}}
	remote := remoteProject("far")
	cfg := &config.Config{Projects: []config.Project{two, none, remote}}
	o := &coreOps{eng: engine.New(cfg, engine.Options{}), httpsPort: 9443}
	st := o.State()
	shop := st.Sites["shop"]
	want := []control.SiteRoute{
		{Host: "shop.test", Port: 3000, URL: "https://shop.test:9443"},
		{Host: "admin.shop.test", Port: 4000, URL: "https://admin.shop.test:9443"},
	}
	if shop.Server != "" || !slices.Equal(shop.Routes, want) {
		t.Errorf("shop = %+v, want routes %+v", shop, want)
	}
	if got := siteURL("shop.test", 9443); got != shop.Routes[0].URL {
		t.Errorf("URL %q differs from Status's %q", shop.Routes[0].URL, got)
	}
	if s, ok := st.Sites["db"]; !ok || s.Routes == nil || len(s.Routes) != 0 {
		t.Errorf("db = %+v, ok %v, want routes []", s, ok)
	}
	if s := st.Sites["far"]; s.Server != remote.Server || len(s.Routes) != 1 {
		t.Errorf("far = %+v, want server %q and one route", s, remote.Server)
	}
}

// On a networked core a route's network address is set, from the site the
// proxy serves for it.
func TestProjectSiteRoutesNetworkURL(t *testing.T) {
	p := localProject("shop", 3000, true)
	p.Routes = append(p.Routes, config.Route{Port: 4000, Role: config.RoleWeb, Hosts: []string{"admin.shop.lab.dev"}})
	sites := []proxy.Site{
		{Project: "shop", Hosts: []string{"shop.test", "shop.lab.dev"}},
		{Project: "shop", Hosts: []string{"admin.shop.lab.dev"}},
	}
	ns := engine.NetworkState{On: true, URL: "https://nearhost.lab.dev:9444"}
	got := projectSite(p, sites, ns, 9443).Routes
	if got[0].NetworkURL != "https://shop.lab.dev:9443" || got[1].NetworkURL != "https://admin.shop.lab.dev:9443" {
		t.Errorf("routes = %+v", got)
	}
	if off := projectSite(p, sites, engine.NetworkState{}, 9443).Routes; off[0].NetworkURL != "" || off[1].NetworkURL != "" {
		t.Errorf("off: %+v", off)
	}
}
