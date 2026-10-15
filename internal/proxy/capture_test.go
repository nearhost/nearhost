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
