package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/agentapi"
	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/testutil"
	"github.com/nearhost/nearhost/internal/tui"
)

// mirrorCore is a core for the client backend's tests: it serves the state
// the test sets, records each action it is asked for, and can hold the
// state back until the test lets it go.
type mirrorCore struct {
	mu      sync.Mutex
	st      control.State
	hold    chan struct{} // State waits for it to close while it is set
	actions []string
	// answerErr is what the answers return.
	answerErr error
}

func (f *mirrorCore) Info() control.Info {
	return control.Info{API: control.APIVersion, Version: "test"}
}
func (f *mirrorCore) Status() control.Status       { return control.Status{} }
func (f *mirrorCore) Stop()                        { f.hit("Stop") }
func (f *mirrorCore) HostsChanged(context.Context) { f.hit("HostsChanged") }

func (f *mirrorCore) State() control.State {
	f.mu.Lock()
	hold := f.hold
	f.mu.Unlock()
	if hold != nil {
		<-hold
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st
}

// set changes the state the core serves.
func (f *mirrorCore) set(fn func(st *control.State)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(&f.st)
}

// holdState makes State wait until the function returned is called.
func (f *mirrorCore) holdState() (release func()) {
	hold := make(chan struct{})
	f.mu.Lock()
	f.hold = hold
	f.mu.Unlock()
	return sync.OnceFunc(func() {
		f.mu.Lock()
		f.hold = nil
		f.mu.Unlock()
		close(hold)
	})
}

func (f *mirrorCore) hit(action string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actions = append(f.actions, action)
}

// asked returns the actions asked for so far.
func (f *mirrorCore) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.actions)
}

func (f *mirrorCore) StartProject(_ context.Context, name string) error {
	f.hit("StartProject " + name)
	return nil
}
func (f *mirrorCore) StopProject(_ context.Context, name string) error {
	f.hit("StopProject " + name)
	return nil
}
func (f *mirrorCore) Recheck(_ context.Context, name string) error {
	f.hit("Recheck " + name)
	return nil
}
func (f *mirrorCore) AcceptChange(name, sha string) error {
	f.hit("AcceptChange " + name + " " + sha)
	return nil
}
func (f *mirrorCore) DeclineChange(name, sha string) { f.hit("DeclineChange " + name + " " + sha) }
func (f *mirrorCore) ConnectServer(_ context.Context, name string) error {
	f.hit("ConnectServer " + name)
	return nil
}
func (f *mirrorCore) DisconnectServer(_ context.Context, name string) error {
	f.hit("DisconnectServer " + name)
	return nil
}
func (f *mirrorCore) ReconnectServer(_ context.Context, name string) error {
	f.hit("ReconnectServer " + name)
	return nil
}
func (f *mirrorCore) Reload(context.Context) error   { f.hit("Reload"); return nil }
func (f *mirrorCore) AcceptProposal(id, name string) { f.hit("AcceptProposal " + id + " " + name) }
func (f *mirrorCore) DeclineProposal(id, reason string) {
	f.hit("DeclineProposal " + id + " " + reason)
}
func (f *mirrorCore) AnswerPassthrough(_ context.Context, id, answer string) error {
	f.hit("AnswerPassthrough " + id + " " + answer)
	return f.answerErr
}
func (f *mirrorCore) ClosePassthrough(_ context.Context, id string) error {
	f.hit("ClosePassthrough " + id)
	return nil
}
func (f *mirrorCore) AnswerDocument(_ context.Context, id, answer string) error {
	f.hit("AnswerDocument " + id + " " + answer)
	return f.answerErr
}
func (f *mirrorCore) OpenDocument(id string) error    { f.hit("OpenDocument " + id); return nil }
func (f *mirrorCore) RemoveDocuments(id string) error { f.hit("RemoveDocuments " + id); return nil }
func (f *mirrorCore) RevokeGrant(server, requester, kind string) error {
	f.hit("RevokeGrant " + server + " " + requester + " " + kind)
	return nil
}
func (f *mirrorCore) Captured(id string) (proxy.Exchange, bool) {
	f.hit("Captured " + id)
	return proxy.Exchange{ID: id}, true
}
func (f *mirrorCore) Replay(_ context.Context, id string) (int, error) {
	f.hit("Replay " + id)
	return 200, nil
}
func (f *mirrorCore) PageKeyURL() (string, error) {
	f.hit("PageKeyURL")
	return "https://nearhost.localhost:6327/?k=secret", nil
}

var _ control.Backend = (*mirrorCore)(nil)

// mirrorHarness is a mirrorCore served on a control socket, and a client
// of it.
type mirrorHarness struct {
	core *mirrorCore
	hub  *control.Hub
	srv  *control.Server
	c    *control.Client
}

// webConfig is the YAML of a config with the local project web.
func webConfig(t *testing.T) string {
	t.Helper()
	data, err := config.Marshal(&config.Config{Projects: []config.Project{localProject("web", 8000, true)}})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func newMirrorHarness(t *testing.T) *mirrorHarness {
	t.Helper()
	core := &mirrorCore{st: control.State{
		ConfigPath: "/home/me/.config/nearhost/config.yaml",
		Config:     webConfig(t),
		HTTPSPort:  8443, HTTPPort: 8080, WebPort: 6327,
		Projects: map[string]engine.ProjectStatus{"web": {State: engine.StateStopped}},
		Servers:  map[string]control.ServerState{},
	}}
	runDir := testutil.TempDir(t, "nhm")
	hub := control.NewHub()
	srv, err := control.Listen(control.SocketPath(runDir), core, hub)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	c, err := control.Dial(runDir)
	if err != nil {
		t.Fatal(err)
	}
	return &mirrorHarness{core: core, hub: hub, srv: srv, c: c}
}

// state fetches the core's state, as the client is made with.
func (h *mirrorHarness) state(t *testing.T) control.State {
	t.Helper()
	st, err := h.c.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// backend makes a client backend from st, closed when the test ends.
func (h *mirrorHarness) backend(t *testing.T, st control.State, local *localOps, props *tui.Proposals) *clientBackend {
	t.Helper()
	if local == nil {
		local = &localOps{}
	}
	if props == nil {
		props = &tui.Proposals{}
	}
	b := newClientBackend(h.c, st, local, props, false)
	t.Cleanup(b.Close)
	return b
}

// nextBusEvent waits for the next event on events.
func nextBusEvent(t *testing.T, events <-chan engine.Event) engine.Event {
	t.Helper()
	select {
	case ev := <-events:
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
	return engine.Event{}
}

// An event that changes the state reaches the interface only once the
// mirror has a state at least as new, so the interface sees the change when
// the event tells it to look.
func TestEventsWaitForTheirState(t *testing.T) {
	h := newMirrorHarness(t)
	b := h.backend(t, h.state(t), nil, nil)
	events, unsubscribe := b.Events().Subscribe(16)
	defer unsubscribe()

	release := h.core.holdState()
	defer release()
	h.core.set(func(st *control.State) {
		st.Projects["web"] = engine.ProjectStatus{State: engine.StateRunning}
	})
	ev := engine.Event{Project: "web", State: engine.StateRunning, Message: "state: running"}
	seq := h.hub.Publish(control.Envelope{Kind: control.KindEvent, Event: &ev})

	select {
	case ev := <-events:
		t.Fatalf("event %q came before its state", ev.Message)
	case <-time.After(200 * time.Millisecond):
	}
	release()
	got := nextBusEvent(t, events)
	if got.Message != "state: running" || got.Project != "web" {
		t.Fatalf("event = %+v", got)
	}
	if st := b.Status("web"); st.State != engine.StateRunning {
		t.Errorf("Status on receipt = %v, want running", st.State)
	}
	if s := b.mirror.seq(); s < seq {
		t.Errorf("mirror at %d, event %d", s, seq)
	}
}

// A reset fetches the state again, and the log says once that lines were
// missed.
func TestResetRefetchesState(t *testing.T) {
	h := newMirrorHarness(t)
	for range 3 {
		h.hub.Publish(control.Envelope{Kind: control.KindEvent, Event: &engine.Event{Message: "old"}})
	}
	st := h.state(t)
	// A state ahead of the hub: the stream answers with a reset.
	st.Seq = 100
	h.core.set(func(st *control.State) {
		st.Projects["web"] = engine.ProjectStatus{State: engine.StateRunning}
	})
	release := h.core.holdState()
	defer release()
	b := h.backend(t, st, nil, nil)
	events, unsubscribe := b.Events().Subscribe(16)
	defer unsubscribe()
	release()

	got := nextBusEvent(t, events)
	if got.Message != "Some log lines were missed." {
		t.Fatalf("event = %q", got.Message)
	}
	if s := b.Status("web"); s.State != engine.StateRunning {
		t.Errorf("Status after the reset = %v, want running", s.State)
	}
	if s := b.mirror.seq(); s != 3 {
		t.Errorf("mirror at %d after the reset, want 3", s)
	}
	// What comes after the reset still reaches the interface, and the note
	// is not said again.
	h.hub.Publish(control.Envelope{Kind: control.KindEvent, Event: &engine.Event{Message: "after"}})
	if got := nextBusEvent(t, events); got.Message != "after" {
		t.Errorf("next event = %q, want after", got.Message)
	}
}

// Approvals from the state and the stream reach the interface once each, in
// order; notices of what opened without asking reach it too.
func TestApprovalsReachTheInterface(t *testing.T) {
	h := newMirrorHarness(t)
	expires := time.Now().Add(time.Minute).Truncate(time.Second)
	first := control.Approval{ID: "p1", Proposal: &agentapi.Proposal{ID: "p1", Origin: "local", Path: "/code/blog", Expires: expires}}
	h.core.set(func(st *control.State) { st.Approvals = []control.Approval{first} })
	props := &tui.Proposals{}
	b := h.backend(t, h.state(t), nil, props)
	_ = b

	publish := func(e control.Envelope) { h.hub.Publish(e) }
	publish(control.Envelope{Kind: control.KindApproval, Approval: &first}) // given already
	publish(control.Envelope{Kind: control.KindApproval, Approval: &control.Approval{ID: "p2", Proposal: &agentapi.Proposal{ID: "p2", Origin: "mini", Path: "/srv/app"}}})
	publish(control.Envelope{Kind: control.KindApproval, Approval: &control.Approval{ID: "x1", Pass: &agentapi.PassRequest{ID: "x1", Origin: "mini", Requester: "claude-code", Remote: 5173, CanAlways: true}}})
	publish(control.Envelope{Kind: control.KindEnded, ID: "unknown"})
	publish(control.Envelope{Kind: control.KindApproval, Approval: &control.Approval{ID: "d1", Doc: &agentapi.DocRequest{ID: "d1", Origin: "mini", Requester: "claude-code", Root: "/srv/app/docs", Doc: "plan.md"}}})
	publish(control.Envelope{Kind: control.KindOpened, Opened: &agentapi.Passthrough{Origin: "mini", Requester: "codex", Remote: 3000}})
	publish(control.Envelope{Kind: control.KindShown, Shown: &control.Shown{Origin: "mini", Requester: "codex", Doc: "spec.md"}})

	testutil.WaitUntil(t, 5*time.Second, func() bool {
		_, _, _, _, shown := props.Waiting()
		return len(shown) == 1
	}, "the shown notice")
	gotProps, passes, opened, docs, shown := props.Waiting()
	if len(gotProps) != 2 || gotProps[0].ID != "p1" || gotProps[1].ID != "p2" || gotProps[1].Origin != "mini" || !gotProps[0].Expires.Equal(expires) {
		t.Errorf("proposals = %+v, want p1 then p2", gotProps)
	}
	if len(passes) != 1 || passes[0].ID != "x1" || passes[0].Remote != 5173 || !passes[0].CanAlways || passes[0].Requester != "claude-code" {
		t.Errorf("passes = %+v", passes)
	}
	if len(docs) != 1 || docs[0].ID != "d1" || docs[0].Doc != "plan.md" || docs[0].Root != "/srv/app/docs" {
		t.Errorf("docs = %+v", docs)
	}
	if len(opened) != 1 || opened[0].Remote != 3000 || opened[0].Requester != "codex" {
		t.Errorf("opened = %+v", opened)
	}
	if len(shown) != 1 || shown[0].Doc != "spec.md" {
		t.Errorf("shown = %+v", shown)
	}
}

// An answer to a request that has ended says so as the interface knows it.
func TestAnswerEndedMapsToTui(t *testing.T) {
	h := newMirrorHarness(t)
	h.core.answerErr = fmt.Errorf("the agent gave up: %w", control.ErrRequestEnded)
	b := h.backend(t, h.state(t), nil, nil)
	ctx := context.Background()
	err := b.AnswerPassthrough(ctx, "x1", tui.AnswerYes)
	if !errors.Is(err, tui.ErrRequestEnded) || err.Error() != "the agent gave up: the request has ended" {
		t.Errorf("AnswerPassthrough = %v, want the core's words, as ended", err)
	}
	if err := b.AnswerDocument(ctx, "d1", tui.AnswerAlways); !errors.Is(err, tui.ErrRequestEnded) {
		t.Errorf("AnswerDocument = %v, want ended", err)
	}
	h.core.answerErr = errors.New("no such tool")
	if err := b.AnswerPassthrough(ctx, "x2", tui.AnswerNo); err == nil || errors.Is(err, tui.ErrRequestEnded) {
		t.Errorf("a refused answer = %v, want an error that is not ended", err)
	}
	if err := b.AnswerDocument(ctx, "d2", tui.AnswerClose); err == nil {
		t.Error("a refused document answer gave no error")
	}
	want := []string{"AnswerPassthrough x1 yes", "AnswerDocument d1 always", "AnswerPassthrough x2 no", "AnswerDocument d2 close"}
	if got := h.core.asked(); !slices.Equal(got, want) {
		t.Errorf("the core was asked %q, want %q", got, want)
	}
}

// When the core goes, the backend tries the stream once more, then says the
// core is lost, once.
func TestLostWhenTheCoreGoes(t *testing.T) {
	h := newMirrorHarness(t)
	b := h.backend(t, h.state(t), nil, nil)
	start := time.Now()
	h.srv.Close()
	select {
	case err := <-b.Lost():
		if err == nil {
			t.Error("Lost sent nil")
		}
		if d := time.Since(start); d < redialAfter {
			t.Errorf("lost after %v, before the redial", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Lost said nothing within 2s")
	}
	select {
	case err := <-b.Lost():
		t.Errorf("a second error on Lost: %v", err)
	case <-time.After(2 * redialAfter):
	}
}

// Reload, and Apply of today's interface, have the core load its file.
func TestReloadGoesToTheCore(t *testing.T) {
	h := newMirrorHarness(t)
	b := h.backend(t, h.state(t), nil, nil)
	if err := b.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(context.Background(), &config.Config{}); err != nil {
		t.Fatal(err)
	}
	if got := h.core.asked(); !slices.Equal(got, []string{"Reload", "Reload"}) {
		t.Errorf("the core was asked %q, want two reloads", got)
	}
}

// What the interface does on this machine asks nothing of the core.
func TestLocalOpsStayLocal(t *testing.T) {
	h := newMirrorHarness(t)
	local := formEngine(t)
	trust := newFake()
	local.b.up.trust = trust.backend()
	local.root = (&fakeRoot{needed: true, sticks: true}).root()
	local.b.up.ssh = sftpOnly(t, t.TempDir())
	b := h.backend(t, h.state(t), local, nil)
	ctx := context.Background()

	if err := b.Trust(ctx); err != nil || trust.installs != 1 {
		t.Errorf("Trust = %v, installs %d", err, trust.installs)
	}
	p := config.Project{Name: "blog", Server: config.LocalServer, Path: "/code/blog", Alias: "blog.test", Enabled: true,
		Routes: []config.Route{{Port: 8000, Role: config.RoleWeb}}}
	if err := b.SaveProject(p); err != nil {
		t.Errorf("SaveProject = %v", err)
	}
	if _, ok := loadSaved(t, local.path).Project("blog"); !ok {
		t.Error("the project was not saved")
	}
	if err := b.CheckServer(ctx, config.Server{Name: "mini", SSHHost: "mini.lan"}); err != nil {
		t.Errorf("CheckServer = %v", err)
	}
	if got := h.core.asked(); len(got) != 0 {
		t.Errorf("the core was asked %q", got)
	}
}

// The actions go to the core, and the answers come back.
func TestActionsGoToTheCore(t *testing.T) {
	h := newMirrorHarness(t)
	var opened []string
	local := &localOps{b: uiBackend{open: func(url string) error {
		opened = append(opened, url)
		return nil
	}}}
	b := h.backend(t, h.state(t), local, nil)
	ctx := context.Background()
	for _, err := range []error{
		b.StartProject(ctx, "web"),
		b.StopProject(ctx, "web"),
		b.Recheck(ctx, "web"),
		b.AcceptChange("web", "abc"),
		b.ConnectServer(ctx, "mini"),
		b.DisconnectServer(ctx, "mini"),
		b.ReconnectServer(ctx, "mini"),
		b.ClosePassthrough(ctx, "x1"),
		b.OpenDocument("t1"),
		b.RemoveDocuments("t1"),
		b.RevokeGrant("mini", "claude-code", "passthrough"),
		b.Shutdown(ctx),
	} {
		if err != nil {
			t.Error(err)
		}
	}
	b.DeclineChange("web", "abc")
	b.AcceptProposal("p1", "blog")
	b.DeclineProposal("p2", "not now")
	if status, err := b.Replay(ctx, "r1"); status != 200 || err != nil {
		t.Errorf("Replay = %d, %v", status, err)
	}
	// A captured exchange is fetched once.
	for range 2 {
		if ex, ok := b.Captured("r2"); !ok || ex.ID != "r2" {
			t.Errorf("Captured = %+v, %v", ex, ok)
		}
	}
	if err := b.OpenPage(); err != nil || !slices.Equal(opened, []string{"https://nearhost.localhost:6327/?k=secret"}) {
		t.Errorf("OpenPage = %v, opened %q", err, opened)
	}
	want := []string{
		"StartProject web", "StopProject web", "Recheck web", "AcceptChange web abc",
		"ConnectServer mini", "DisconnectServer mini", "ReconnectServer mini", "ClosePassthrough x1",
		"OpenDocument t1", "RemoveDocuments t1", "RevokeGrant mini claude-code passthrough", "Stop",
		"DeclineChange web abc", "AcceptProposal p1 blog", "DeclineProposal p2 not now", "Replay r1",
		"Captured r2", "PageKeyURL",
	}
	if got := h.core.asked(); !slices.Equal(got, want) {
		t.Errorf("the core was asked\n%q\nwant\n%q", got, want)
	}
}

// Opening the page leaves its key out of an error.
func TestClientOpenPageHidesTheKey(t *testing.T) {
	h := newMirrorHarness(t)
	local := &localOps{b: uiBackend{open: func(url string) error {
		return fmt.Errorf("could not run the opener for %s", url)
	}}}
	b := h.backend(t, h.state(t), local, nil)
	err := b.OpenPage()
	if err == nil || err.Error() != "could not run the opener for https://nearhost.localhost:6327" {
		t.Errorf("OpenPage = %v", err)
	}
}

// Close ends the backend's goroutines, and is safe to call twice.
func TestClientCloseEndsItsWork(t *testing.T) {
	h := newMirrorHarness(t)
	b := newClientBackend(h.c, h.state(t), &localOps{}, &tui.Proposals{}, false)
	b.Close()
	b.Close()
	select {
	case <-b.done:
	default:
		t.Error("the backend's goroutines are still running after Close")
	}
	select {
	case err := <-b.Lost():
		t.Errorf("Close reported the core lost: %v", err)
	default:
	}
}
