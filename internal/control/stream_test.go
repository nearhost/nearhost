package control

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/agentapi"
	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/documents"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/grants"
	"github.com/nearhost/nearhost/internal/nearhostrc"
	"github.com/nearhost/nearhost/internal/testutil"
	"github.com/nearhost/nearhost/internal/transport"
)

// serveHub starts a server for b with a hub of its own, and returns the hub
// and a client. The server is closed when the test ends.
func serveHub(t *testing.T, b Backend) (*Hub, *Client) {
	t.Helper()
	dir := testutil.TempDir(t, "ctl")
	hub := NewHub()
	s, err := Listen(SocketPath(dir), b, hub)
	if err != nil {
		t.Fatal(err)
	}
	checkTraffic(t, s)
	return hub, dial(t, dir)
}

// openEvents opens a stream after seq. It ends when the test does.
func openEvents(t *testing.T, c *Client, after uint64) (<-chan Envelope, func() error, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch, errf, err := c.Events(ctx, after)
	if err != nil {
		t.Fatal(err)
	}
	return ch, errf, cancel
}

// next returns the next envelope on ch, failing the test if none comes.
func next(t *testing.T, ch <-chan Envelope) Envelope {
	t.Helper()
	select {
	case e, ok := <-ch:
		if !ok {
			t.Fatal("the stream closed")
		}
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for an envelope")
	}
	return Envelope{}
}

func event(msg string) Envelope {
	return Envelope{Kind: KindEvent, Event: &engine.Event{Time: time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC), Project: "shop", Message: msg}}
}

func TestStreamReplaysAfterSeq(t *testing.T) {
	hub, c := serveHub(t, newFake())
	for i := range 5 {
		hub.Publish(event(strings.Repeat("x", i)))
	}
	ch, _, _ := openEvents(t, c, 2)
	for want := uint64(3); want <= 5; want++ {
		e := next(t, ch)
		if e.Seq != want || e.Kind != KindEvent || e.Event == nil || e.Event.Message != strings.Repeat("x", int(want-1)) {
			t.Fatalf("got %+v, want event %d", e, want)
		}
	}
	hub.Publish(event("live"))
	if e := next(t, ch); e.Seq != 6 || e.Event == nil || e.Event.Message != "live" {
		t.Fatalf("got %+v, want the live event 6", e)
	}
}

func TestStreamResetsWhenTooFarBehind(t *testing.T) {
	hub, c := serveHub(t, newFake())
	for range RingSize + 10 {
		hub.Publish(event("old"))
	}
	ch, _, _ := openEvents(t, c, 1)
	if e := next(t, ch); e.Kind != KindReset || e.Seq != hub.Seq() {
		t.Fatalf("first envelope = %+v, want a reset to %d", e, hub.Seq())
	}
	hub.Publish(event("live"))
	if e := next(t, ch); e.Seq != hub.Seq() || e.Event == nil || e.Event.Message != "live" {
		t.Fatalf("got %+v, want the live event %d", e, hub.Seq())
	}
}

// A client that does not read is sent a reset rather than holding the core
// up, and what it is sent says exactly what it missed: items follow one
// another by number, and a reset to n is followed by n+1.
func TestSlowStreamGetsReset(t *testing.T) {
	hub, c := serveHub(t, newFake())
	ch, _, _ := openEvents(t, c, 0)
	// Long messages, so the socket's buffers cannot hold them all.
	long := strings.Repeat("x", 1024)
	var slowest time.Duration
	for range 2 * RingSize {
		start := time.Now()
		hub.Publish(event(long))
		slowest = max(slowest, time.Since(start))
	}
	// Generous for a busy runner: a Publish held up by the client would
	// wait for good.
	if slowest > time.Second {
		t.Errorf("the slowest Publish took %v, want well under a second", slowest)
	}
	var prev uint64
	resets := 0
	for prev < hub.Seq() {
		e := next(t, ch)
		switch {
		case e.Kind == KindReset:
			if e.Seq <= prev {
				t.Fatalf("a reset to %d follows %d", e.Seq, prev)
			}
			resets++
		case e.Seq != prev+1:
			t.Fatalf("%d follows %d without a reset", e.Seq, prev)
		}
		prev = e.Seq
	}
	if resets == 0 {
		t.Fatal("no reset was sent")
	}
	hub.Publish(event("live"))
	if e := next(t, ch); e.Seq != prev+1 || e.Kind != KindEvent {
		t.Fatalf("got %+v, want the live event %d", e, prev+1)
	}
}

func TestStateCarriesSeqAndClients(t *testing.T) {
	hub, c := serveHub(t, newFake())
	hub.Publish(event("a"))
	hub.Publish(event("b"))
	openEvents(t, c, 0)
	st, err := c.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Clients != 1 || st.Seq != hub.Seq() {
		t.Fatalf("State has %d clients at %d, want 1 at %d", st.Clients, st.Seq, hub.Seq())
	}
}

func TestOnAttached(t *testing.T) {
	hub, c := serveHub(t, newFake())
	var log attachLog
	hub.OnAttached(log.hook)
	_, _, cancel1 := openEvents(t, c, 0)
	_, _, cancel2 := openEvents(t, c, 0)
	if got := log.get(); !reflect.DeepEqual(got, []bool{true}) {
		t.Fatalf("with two streams open the hook got %v, want [true]", got)
	}
	cancel1()
	testutil.WaitUntil(t, 5*time.Second, func() bool { return hub.Clients() == 1 }, "the first stream to close")
	cancel2()
	// The hook is told after the stream is gone, so wait for it.
	testutil.WaitUntil(t, 5*time.Second, func() bool { return len(log.get()) >= 2 }, "the hook to be told the last stream closed")
	if hub.Clients() != 0 {
		t.Fatalf("%d clients attached, want 0", hub.Clients())
	}
	if got := log.get(); !reflect.DeepEqual(got, []bool{true, false}) {
		t.Fatalf("the hook got %v, want [true false]", got)
	}
}

// A stream ended by its context says it ended for no fault.
func TestEventsEndWithContext(t *testing.T) {
	_, c := serveHub(t, newFake())
	ch, errf, cancel := openEvents(t, c, 0)
	cancel()
	for range ch {
	}
	if err := errf(); err != nil {
		t.Fatalf("err() = %v, want nil", err)
	}
}

// A stream ended by the core says so.
func TestEventsEndWithServer(t *testing.T) {
	dir := testutil.TempDir(t, "ctl")
	s, err := Listen(SocketPath(dir), newFake(), NewHub())
	if err != nil {
		t.Fatal(err)
	}
	checkTraffic(t, s)
	ch, errf, _ := openEvents(t, dial(t, dir), 0)
	s.Close()
	for range ch {
	}
	if err := errf(); err == nil {
		t.Fatal("err() = nil, want why the stream ended")
	}
}

// A server given no hub streams from one of its own.
func TestListenWithoutHub(t *testing.T) {
	dir := testutil.TempDir(t, "ctl")
	s, err := Listen(SocketPath(dir), newFake(), nil)
	if err != nil {
		t.Fatal(err)
	}
	checkTraffic(t, s)
	c := dial(t, dir)
	openEvents(t, c, 0)
	st, err := c.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Seq != 0 || st.Clients != 1 {
		t.Fatalf("State has %d clients at %d, want 1 at 0", st.Clients, st.Seq)
	}
}

func TestEventsNotRunning(t *testing.T) {
	dir := testutil.TempDir(t, "ctl")
	s, err := Listen(SocketPath(dir), newFake(), NewHub())
	if err != nil {
		t.Fatal(err)
	}
	checkTraffic(t, s)
	c := dial(t, dir)
	s.Close()
	if _, _, err := c.Events(context.Background(), 0); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Events error = %v, want ErrNotRunning", err)
	}
}

func TestEventsBadAfter(t *testing.T) {
	_, c := serveHub(t, newFake())
	code, _, err := c.raw(context.Background(), http.MethodGet, "/v1/events?after=minus")
	if err != nil {
		t.Fatal(err)
	}
	if code != http.StatusBadRequest {
		t.Fatalf("GET /v1/events?after=minus = %d, want 400", code)
	}
}

// The server sends keepalive comments on an idle stream, in the format of
// server-sent events, and the client passes over them.
func TestKeepaliveIsANamedEvent(t *testing.T) {
	defer func(d time.Duration) { keepaliveEvery = d }(keepaliveEvery)
	keepaliveEvery = 10 * time.Millisecond
	_, c := serveHub(t, newFake())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://nearhost/v1/events?after=0", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	lines := bufio.NewScanner(resp.Body)
	for lines.Scan() && lines.Text() != "event: heartbeat" {
	}
	if !lines.Scan() || lines.Text() != "data: {}" {
		t.Fatalf("after event: heartbeat got %q, want data: {}", lines.Text())
	}
}

func TestKeepalive(t *testing.T) {
	defer func(d time.Duration) { keepaliveEvery = d }(keepaliveEvery)
	keepaliveEvery = 10 * time.Millisecond
	hub, c := serveHub(t, newFake())

	// Bounded, so a keepalive that never comes fails the test.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://nearhost/v1/events?after=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	lines := bufio.NewScanner(resp.Body)
	for lines.Scan() && lines.Text() != "event: heartbeat" {
	}
	hub.Publish(event("after the keepalive"))
	for lines.Scan() && !strings.HasPrefix(lines.Text(), "data: {\"seq\"") {
	}
	if !strings.Contains(lines.Text(), `"seq":1`) {
		t.Fatalf("got %q, want the data line of envelope 1", lines.Text())
	}
}

// The client passes over comments and fields other than data, and joins the
// data lines of one event.
func TestReadStreamSkipsComments(t *testing.T) {
	in := ": keepalive\n\n" +
		"event: heartbeat\ndata: {}\n\n" +
		"data: {\"seq\":1,\"kind\":\"reset\"}\n\n" +
		": keepalive\n\n" +
		"id: 7\n\n" +
		"data: {\"seq\":2,\ndata: \"kind\":\"ended\",\"id\":\"a1\"}\n\n"
	out := make(chan Envelope, 4)
	if err := readStream(context.Background(), strings.NewReader(in), out); !errors.Is(err, errStreamEnded) {
		t.Fatalf("readStream = %v, want errStreamEnded", err)
	}
	close(out)
	var got []Envelope
	for e := range out {
		got = append(got, e)
	}
	want := []Envelope{{Seq: 1, Kind: KindReset}, {Seq: 2, Kind: KindEnded, ID: "a1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestStateRoundTrip(t *testing.T) {
	at := time.Date(2026, 11, 21, 9, 15, 0, 0, time.UTC)
	b := newFake()
	b.state = State{
		Info:       b.info,
		ConfigPath: "/home/me/.config/nearhost/config.yaml",
		Config:     "projects: []\n",
		HTTPSPort:  8443,
		HTTPPort:   8080,
		WebPort:    6327,
		Projects: map[string]engine.ProjectStatus{"shop": {
			State:         engine.StateRunning,
			Message:       "serving",
			Alias:         "shop.test",
			WebLocalPort:  41000,
			WebLocalPorts: map[int]int{3000: 41000, 5173: 41001},
			Pending: &engine.Change{
				SHA256: "abc",
				Project: config.Project{Name: "shop", Server: "leto", Path: "/srv/shop", Alias: "shop.test", Enabled: true,
					Routes: []config.Route{{Port: 3000, Role: config.RoleWeb, Label: "app", Hosts: []string{"api.shop.test"}}}},
				Items: []nearhostrc.Item{{Port: 3000, Host: "api.shop.test", Why: "a real domain"}},
			},
			Notice:  "the file is odd",
			Waiting: "leto",
		}},
		Servers: map[string]ServerState{"leto": {
			Health: engine.ServerHealth{Health: transport.Health{
				Status: transport.StatusConnecting, LastError: "refused", Failures: 2, NextAttempt: at,
				Refused: true, AgentPath: "/run/a.sock", AgentErr: "no", AgentProblem: transport.AgentNotReady,
			}, Held: true},
			Status:  transport.StatusConnecting,
			Message: "trying again",
		}},
		Passthroughs: []engine.Passthrough{{
			PassthroughSpec: engine.PassthroughSpec{ID: "p1", Server: "leto", Requester: "claude-code", Session: "s", Label: "vite", Remote: 5173},
			Local:           5173, Opened: at,
		}},
		Documents: []documents.Tree{{
			ID: "t1", Server: "leto", Root: "/srv/shop/docs", Requester: "claude-code", RootHandle: "/h",
			Docs:   []documents.Doc{{Path: "plan.md", Opened: at}, {Path: "spec.md", Opened: at.Add(-time.Minute)}},
			Opened: at, Hidden: true,
		}},
		Grants:      []grants.Grant{{Server: "leto", Requester: "claude-code", Kind: grants.KindPassthrough, Created: at}},
		GrantsError: "the grants file is unreadable",
		Approvals: []Approval{
			{ID: "a1", Proposal: &agentapi.Proposal{ID: "a1", Origin: "leto", Path: "/srv/blog", Name: "blog", Web: 4000, Expires: at}},
			{ID: "a2", Pass: &agentapi.PassRequest{ID: "a2", Origin: "leto", Requester: "claude-code", Session: "s", Label: "api", Remote: 8000, LocalTaken: true, CanAlways: true, Expires: at}},
			{ID: "a3", Doc: &agentapi.DocRequest{ID: "a3", Origin: "leto", Requester: "claude-code", Session: "s", Label: "plan", Root: "/srv/shop", Doc: "plan.md", CanAlways: true, NewRoot: true, Expires: at}},
		},
	}
	hub, c := serveHub(t, b)
	hub.Publish(event("a"))
	hub.Publish(event("b"))
	got, err := c.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := b.state
	want.Seq, want.Clients = 2, 0
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("State =\n%+v\nwant\n%+v", got, want)
	}
}

// A client that stops reading is let go once a write to it has waited
// streamWriteTimeout, so it no longer counts as attached.
func TestStreamLetsGoOfAClientThatDoesNotRead(t *testing.T) {
	defer func(d time.Duration) { streamWriteTimeout = d }(streamWriteTimeout)
	streamWriteTimeout = 50 * time.Millisecond
	hub, c := serveHub(t, newFake())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://nearhost/v1/events?after=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() // never read
	if hub.Clients() != 1 {
		t.Fatalf("%d clients attached, want 1", hub.Clients())
	}
	long := strings.Repeat("x", 64<<10)
	testutil.WaitUntil(t, 5*time.Second, func() bool {
		hub.Publish(event(long))
		return hub.Clients() == 0
	}, "the stream to let the client go")
}
