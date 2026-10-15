package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// idOf is the nearhost_id of the access log line for path under alias.
func idOf(t *testing.T, h *harness, alias, path string) string {
	t.Helper()
	var id string
	waitUntil(t, 5*time.Second, func() bool {
		h.rec.mu.Lock()
		defer h.rec.mu.Unlock()
		for _, l := range h.rec.lines {
			line, ok := strings.CutPrefix(l, alias+" ")
			if !ok || !strings.Contains(line, `"uri":"`+path+`"`) {
				continue
			}
			var v struct {
				ID string `json:"nearhost_id"`
			}
			if json.Unmarshal([]byte(line), &v) == nil && v.ID != "" {
				id = v.ID
				return true
			}
		}
		return false
	}, "an access line with a nearhost_id for "+path)
	return id
}

// The proxy keeps each exchange with its real headers and bodies, and names
// it in the access log, which stays redacted.
func TestCaptureKeepsExchanges(t *testing.T) {
	ClearCaptured()
	t.Cleanup(ClearCaptured)
	up := upstream(t, "a")
	h := startProxyWith(t, []Site{{Alias: "a.test", Upstream: addr(up.Server)}}, true)
	c := h.client("a.test", false)
	h.getEventually(c, "a.test", "/hello")

	req, _ := http.NewRequest("POST", h.url("a.test", "/echo"), strings.NewReader("ping"))
	req.Header.Set("Cookie", "session=secret")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "got ping" {
		t.Fatalf("the upstream saw %q", body)
	}
	id := idOf(t, h, "a.test", "/echo")
	e, ok := Captured(id)
	if !ok {
		t.Fatalf("nothing kept under %s", id)
	}
	if e.Method != "POST" || e.URI != "/echo" || e.Alias != "a.test" || e.Status != http.StatusCreated ||
		string(e.ReqBody) != "ping" || string(e.RespBody) != "got ping" || e.RespHeader.Get("Content-Type") != "text/plain" ||
		e.ReqHeader.Get("Cookie") != "session=secret" || e.ReqBodyCut() || e.RespBodyCut() {
		t.Errorf("kept %+v", e)
	}
	if h.rec.hasFor("a.test", "session=secret") {
		t.Error("the cookie reached the log")
	}
}

// Bodies are cut at the limit, and the oldest exchanges go first.
func TestCaptureLimits(t *testing.T) {
	ClearCaptured()
	t.Cleanup(ClearCaptured)
	defer func(body, n int) { captureBodyMax, captureMax = body, n }(captureBodyMax, captureMax)
	captureBodyMax, captureMax = 4, 2
	up := upstream(t, "a")
	h := startProxyWith(t, []Site{{Alias: "a.test", Upstream: addr(up.Server)}}, true)
	c := h.client("a.test", false)
	h.getEventually(c, "a.test", "/hello")
	first := idOf(t, h, "a.test", "/hello")
	e, _ := Captured(first)
	if string(e.RespBody) != "hell" || e.RespBodySize != int64(len("hello from a")) || !e.RespBodyCut() {
		t.Errorf("kept %q of %d", e.RespBody, e.RespBodySize)
	}
	for _, p := range []string{"/echo", "/echo?again"} {
		r, err := c.Post(h.url("a.test", p), "text/plain", strings.NewReader("x"))
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
	}
	idOf(t, h, "a.test", "/echo?again")
	if _, ok := Captured(first); ok {
		t.Error("the oldest exchange was kept past the limit")
	}
}

// Streams still stream, and WebSockets still upgrade, through the capture.
func TestCaptureLeavesStreamsAndSocketsAlone(t *testing.T) {
	ClearCaptured()
	t.Cleanup(ClearCaptured)
	up := upstream(t, "a")
	h := startProxyWith(t, []Site{{Alias: "a.test", Upstream: addr(up.Server)}}, true)
	c := h.client("a.test", true)
	h.getEventually(c, "a.test", "/hello")

	sresp, err := c.Get(h.url("a.test", "/stream"))
	if err != nil {
		t.Fatal(err)
	}
	rd := bufio.NewReader(sresp.Body)
	if line, err := rd.ReadString('\n'); err != nil || line != "one\n" {
		t.Fatalf("first chunk = %q, %v", line, err)
	}
	close(up.chunk2) // the upstream sends chunk two only now
	if line, err := rd.ReadString('\n'); err != nil || line != "two\n" {
		t.Fatalf("second chunk = %q, %v", line, err)
	}
	sresp.Body.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, fmt.Sprintf("wss://a.test:%d/ws", h.httpsPort), &websocket.DialOptions{HTTPClient: h.client("a.test", true)})
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	if err := ws.Write(ctx, websocket.MessageText, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if _, data, err := ws.Read(ctx); err != nil || string(data) != "echo:hi" {
		t.Fatalf("ws read %q, %v", data, err)
	}
	ws.Close(websocket.StatusNormalClosure, "")
	e, ok := Captured(idOf(t, h, "a.test", "/ws"))
	if !ok || !e.Upgraded || e.Status != http.StatusSwitchingProtocols {
		t.Errorf("kept %+v", e)
	}
}

// Without capture nothing is kept and no handler is added.
func TestNoCapture(t *testing.T) {
	cfg, err := BuildConfig([]Site{{Alias: "a.test", Upstream: "127.0.0.1:1"}}, Options{StorageRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cfg), captureModule) {
		t.Error("a capture handler without capture")
	}
	cfg, _ = BuildConfig([]Site{{Alias: "a.test", Upstream: "127.0.0.1:1"}}, Options{StorageRoot: t.TempDir(), Capture: true})
	if !strings.Contains(string(cfg), `"handler":"`+captureModule+`"`) {
		t.Errorf("no capture handler:\n%s", cfg)
	}
}

// A kept request is sent again through the proxy, with its body and real
// headers, and is kept and logged again.
func TestReplay(t *testing.T) {
	ClearCaptured()
	t.Cleanup(ClearCaptured)
	up := upstream(t, "a")
	h := startProxyWith(t, []Site{{Alias: "a.test", Upstream: addr(up.Server)}}, true)
	c := h.client("a.test", false)
	h.getEventually(c, "a.test", "/hello")
	r, err := c.Post(h.url("a.test", "/echo"), "text/plain", strings.NewReader("ping"))
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	first := idOf(t, h, "a.test", "/echo")
	e, _ := Captured(first)
	status, err := Replay(context.Background(), e, h.storage, h.httpsPort)
	if err != nil || status != http.StatusCreated {
		t.Fatalf("Replay = %d, %v", status, err)
	}
	waitUntil(t, 5*time.Second, func() bool {
		captures.Lock()
		defer captures.Unlock()
		return len(captures.order) >= 3
	}, "the replay kept")
	captures.Lock()
	again := *captures.byID[captures.order[len(captures.order)-1]]
	captures.Unlock()
	if again.ID == first || string(again.ReqBody) != "ping" || string(again.RespBody) != "got ping" || again.Method != "POST" {
		t.Errorf("replay kept as %+v", again)
	}
}

func TestCanReplay(t *testing.T) {
	if err := CanReplay(Exchange{Upgraded: true}); err == nil {
		t.Error("an upgrade can be replayed")
	}
	if err := CanReplay(Exchange{ReqBody: []byte("ab"), ReqBodySize: 10}); err == nil || err.Error() != "only the first 2 bytes of its body were kept" {
		t.Errorf("a cut body: %v", err)
	}
	if err := CanReplay(Exchange{ReqBody: []byte("ab"), ReqBodySize: 2}); err != nil {
		t.Errorf("a whole body: %v", err)
	}
}

// An upstream that answers before reading the request body leaves the
// transport still sending it after the handler has kept the exchange: what
// is kept is taken once, safely, and nothing is added afterwards. (Through a
// real proxy the race detector also reports Caddy's own byte count, which
// races the same way with or without this handler, so this drives the tee
// directly.)
func TestRequestBodyStillReadAfterTheExchangeIsKept(t *testing.T) {
	var kept limited
	body := &teeBody{ReadCloser: io.NopCloser(strings.NewReader(strings.Repeat("x", 4<<20))), to: &kept}
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		io.Copy(io.Discard, body) // the transport, still sending
	}()
	buf, n := kept.done() // the handler, done with the exchange
	if int64(len(buf)) > n || len(buf) > captureBodyMax {
		t.Errorf("kept %d bytes of %d", len(buf), n)
	}
	<-sent
	if after, m := kept.done(); len(after) != len(buf) || m != n {
		t.Errorf("more was kept after done: %d bytes of %d, then %d of %d", len(buf), n, len(after), m)
	}
}

// An exchange that ends after the store was cleared, such as a WebSocket
// that outlived the proxy, is not kept.
func TestCaptureKeepsNothingAfterItIsCleared(t *testing.T) {
	ClearCaptured()
	t.Cleanup(ClearCaptured)
	gen := captureGen()
	ClearCaptured() // the proxy stopped while the exchange was open
	keep(&Exchange{ID: "late"}, gen)
	if _, ok := Captured("late"); ok {
		t.Error("an exchange from before the store was cleared was kept")
	}
	keep(&Exchange{ID: "now"}, captureGen())
	if _, ok := Captured("now"); !ok {
		t.Error("a current exchange was not kept")
	}
}

// The store's limit counts headers and the room the bodies take, not only
// the bodies' lengths.
func TestCaptureLimitCountsHeaders(t *testing.T) {
	ClearCaptured()
	t.Cleanup(ClearCaptured)
	defer func(total int) { captureTotalMax = total }(captureTotalMax)
	captureTotalMax = 10 << 10
	big := http.Header{"X-Big": {strings.Repeat("h", 6<<10)}}
	keep(&Exchange{ID: "one", ReqHeader: big}, captureGen())
	keep(&Exchange{ID: "two", ReqHeader: big}, captureGen())
	if _, ok := Captured("one"); ok {
		t.Error("two exchanges of 6 KiB of headers each were kept under a 10 KiB limit")
	}
	if _, ok := Captured("two"); !ok {
		t.Error("the newest exchange was let go")
	}
}

// The request sent again goes to the proxy for its host, with only the path
// and query of an absolute-form URI, and without the headers its Connection
// header names, which were meant for the hop it came on.
func TestReplayRequest(t *testing.T) {
	e := Exchange{Method: "GET", Host: "myapp.test", URI: "http://myapp.test/a/b?c=1", ReqHeader: http.Header{
		"Connection": {"keep-alive, X-Hop"}, "X-Hop": {"1"}, "X-Kept": {"2"}, "Keep-Alive": {"timeout=5"},
	}}
	req, err := replayRequest(context.Background(), e, 18443)
	if err != nil {
		t.Fatal(err)
	}
	if got := req.URL.String(); got != "https://myapp.test:18443/a/b?c=1" {
		t.Errorf("URL %s", got)
	}
	if req.Host != "myapp.test" || req.Header.Get("X-Kept") != "2" || req.Header.Get("X-Hop") != "" || req.Header.Get("Connection") != "" || req.Header.Get("Keep-Alive") != "" {
		t.Errorf("host %q, headers %v", req.Host, req.Header)
	}
	e.URI = "/plain?x=1"
	if req, _ := replayRequest(context.Background(), e, 443); req.URL.String() != "https://myapp.test/plain?x=1" {
		t.Errorf("URL %s", req.URL)
	}
}
