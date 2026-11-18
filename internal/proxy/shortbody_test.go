package proxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// rawApp is an app on a bare TCP listener. It answers /hello, and hands
// every other request to answer, which may leave the body unread: unlike
// http.Server, it reads nothing of a body after answering. It returns the
// listener's address.
func rawApp(t *testing.T, answer func(c net.Conn, req *http.Request)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				for {
					req, err := http.ReadRequest(br)
					if err != nil {
						return
					}
					if req.URL.Path == "/hello" {
						io.Copy(io.Discard, req.Body)
						io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhello")
						continue
					}
					answer(c, req)
					return
				}
			}()
		}
	}()
	return ln.Addr().String()
}

// answerAfter is an app that reads n bytes of the body (all of it when n is
// negative), answers status and hangs up.
func answerAfter(n int, status int) func(net.Conn, *http.Request) {
	return func(c net.Conn, req *http.Request) {
		if n < 0 {
			io.Copy(io.Discard, req.Body)
		} else {
			io.ReadFull(req.Body, make([]byte, n))
		}
		fmt.Fprintf(c, "HTTP/1.1 %d %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", status, http.StatusText(status))
		hangUp(c)
	}
}

// hangUp ends the app's side of c once the proxy has read the answer. What
// the proxy already wrote is read and dropped, so the close is clean (an
// unread byte turns a close into a reset on some systems); the rest of the
// body is still with the client, which holds it back, so the proxy cannot
// have read to its end.
func hangUp(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		tc.CloseWrite()
	}
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	io.Copy(io.Discard, c)
}

// sendPart sends a request to path whose body declares length bytes
// (-1 for chunked) and sends first of them, then holds the rest back until
// the proxy has kept the exchange; abort, instead, makes the client give up
// after first. It returns the exchange kept.
func sendPart(t *testing.T, s *httptest.Server, path string, header http.Header, first []byte, length int64, abort bool) Exchange {
	t.Helper()
	pr, pw := io.Pipe()
	req, err := http.NewRequest("POST", s.URL+path, pr)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.ContentLength = length
	go func() {
		if len(first) > 0 {
			if _, err := pw.Write(first); err != nil {
				return
			}
		}
		if abort {
			pw.CloseWithError(errors.New("the client gave up"))
		}
	}()
	answered := make(chan struct{})
	go func() {
		defer close(answered)
		if resp, err := s.Client().Do(req); err == nil {
			resp.Body.Close()
		}
	}()
	e := keptFor(t, path)
	pw.CloseWithError(errors.New("the client is done"))
	<-answered
	return e
}

// keptFor waits for the exchange the proxy kept for uri.
func keptFor(t *testing.T, uri string) Exchange {
	t.Helper()
	var e Exchange
	waitUntil(t, 10*time.Second, func() bool {
		captures.Lock()
		defer captures.Unlock()
		for _, id := range captures.order {
			if x := captures.byID[id]; x.URI == uri {
				e = *x
				return true
			}
		}
		return false
	}, "an exchange kept for "+uri)
	return e
}

// fronts are the capture in front of an app, over HTTP/1.1 and over
// HTTP/2 (with TLS), each passing requests on with a reverse proxy.
type fronts struct{ h1, h2 *httptest.Server }

// captureFronts starts the capture, with its store cleared, in front of an
// app that answers with answer. It runs in a plain Go server rather than
// Caddy: Caddy counts the bytes of a request body without a lock, so when
// the transport is still reading a body the app left, the race detector
// reports Caddy's count, which this handler does not touch.
func captureFronts(t *testing.T, answer func(net.Conn, *http.Request)) fronts {
	t.Helper()
	return captureFrontsTo(t, "http://"+rawApp(t, answer), nil)
}

// captureFrontsTo is captureFronts proxying to the app at target, with gate,
// when there is one, between the capture and the reverse proxy.
func captureFrontsTo(t *testing.T, target string, gate *Gate) fronts {
	t.Helper()
	ClearCaptured()
	t.Cleanup(ClearCaptured)
	app, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	// Like Caddy's, the transport sends a body at once, without waiting for
	// 100 Continue.
	tr := &http.Transport{}
	t.Cleanup(tr.CloseIdleConnections)
	rp := httputil.NewSingleHostReverseProxy(app)
	rp.Transport = tr
	rp.ErrorLog = log.New(io.Discard, "", 0)
	var next caddyhttp.Handler = caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		// As Caddy's proxy does, the body is left open: closing an
		// HTTP/1.1 body waits for a read the transport still has going.
		if r.Body != nil {
			r.Body = io.NopCloser(r.Body)
		}
		rp.ServeHTTP(w, r)
		return nil
	})
	if gate != nil {
		proxied := next
		next = caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
			return gate.ServeHTTP(w, r, proxied)
		})
	}
	capture := &Capture{Alias: "a.test"}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture.ServeHTTP(w, r, next)
	})
	f := fronts{h1: httptest.NewServer(h), h2: httptest.NewUnstartedServer(h)}
	f.h2.EnableHTTP2 = true
	f.h2.StartTLS()
	t.Cleanup(f.h1.Close)
	t.Cleanup(f.h2.Close)
	return f
}

// The app answers after 8 KiB of a 30 KiB body: the exchange says the app
// stopped reading, and the request is not sent again. So over HTTP/1.1 and
// HTTP/2 alike.
func TestCaptureMarksABodyTheAppStoppedReading(t *testing.T) {
	f := captureFronts(t, answerAfter(8<<10, http.StatusRequestEntityTooLarge))
	for _, tc := range []struct {
		path  string
		front *httptest.Server
	}{{"/upload", f.h1}, {"/upload2", f.h2}} {
		e := sendPart(t, tc.front, tc.path, nil, bytes.Repeat([]byte("x"), 8<<10), 30<<10, false)
		if e.Status != http.StatusRequestEntityTooLarge {
			t.Errorf("%s: kept status %d", tc.path, e.Status)
		}
		if !e.ReqBodyShort || e.ReqBodyAborted || e.ReqBodySize != 8<<10 || e.ReqBodyLength != 30<<10 || e.ReqBodyCut() {
			t.Errorf("%s (%s): kept short=%v aborted=%v %d of %d, cut=%v", tc.path, e.Proto, e.ReqBodyShort, e.ReqBodyAborted, e.ReqBodySize, e.ReqBodyLength, e.ReqBodyCut())
		}
		const want = "the app stopped reading this request's body, so Nearhost cannot send it again"
		if err := CanReplay(e); err == nil || err.Error() != want {
			t.Errorf("%s: CanReplay = %v", tc.path, err)
		}
		if _, err := Replay(context.Background(), e, t.TempDir(), 0); err == nil || err.Error() != want {
			t.Errorf("%s: Replay = %v", tc.path, err)
		}
	}
}

// A body past the capture's limit that the app stopped reading is both cut
// and short, and N counts every byte the app was sent, not only those kept.
func TestCaptureMarksALongBodyCutAndShort(t *testing.T) {
	defer func(n int) { captureBodyMax = n }(captureBodyMax)
	captureBodyMax = 1 << 10
	f := captureFronts(t, answerAfter(8<<10, http.StatusUnauthorized))
	e := sendPart(t, f.h1, "/upload", nil, bytes.Repeat([]byte("x"), 8<<10), 30<<10, false)
	if !e.ReqBodyShort || !e.ReqBodyCut() || e.ReqBodySize != 8<<10 || len(e.ReqBody) != 1<<10 {
		t.Errorf("kept short=%v cut=%v, %d bytes of %d", e.ReqBodyShort, e.ReqBodyCut(), len(e.ReqBody), e.ReqBodySize)
	}
}

// A chunked body, with no length given, is short when the app stopped
// before its end.
func TestCaptureMarksAChunkedBodyTheAppStoppedReading(t *testing.T) {
	f := captureFronts(t, answerAfter(8<<10, http.StatusFound))
	e := sendPart(t, f.h1, "/upload", nil, bytes.Repeat([]byte("x"), 8<<10), -1, false)
	if !e.ReqBodyShort || e.ReqBodyLength != -1 || e.ReqBodySize != 8<<10 {
		t.Errorf("kept short=%v, %d bytes of %d", e.ReqBodyShort, e.ReqBodySize, e.ReqBodyLength)
	}
	if err := CanReplay(e); err == nil {
		t.Error("a chunked body cut short can be replayed")
	}
}

// An app that answers a request expecting 100-continue at once never gets
// the body: none of it was read. The client speaks HTTP/2, as over HTTP/1.1
// Go's server cannot send the answer while the proxy waits on the body, so
// there the client sends it or gives up.
func TestCaptureMarksABodyNeverRead(t *testing.T) {
	f := captureFronts(t, answerAfter(0, http.StatusExpectationFailed))
	e := sendPart(t, f.h2, "/upload", http.Header{"Expect": {"100-continue"}}, nil, 30<<10, false)
	if e.Status != http.StatusExpectationFailed {
		t.Errorf("kept status %d", e.Status)
	}
	if !e.ReqBodyShort || e.ReqBodySize != 0 || e.ReqBodyLength != 30<<10 {
		t.Errorf("kept short=%v, %d bytes of %d", e.ReqBodyShort, e.ReqBodySize, e.ReqBodyLength)
	}
	if err := CanReplay(e); !errors.Is(err, ErrBodyNeverReached) {
		t.Errorf("CanReplay = %v", err)
	}
}

// A body the app never got, as its port is closed or its forward is down,
// is not blamed on the app.
func TestCaptureMarksABodyThatNeverReachedTheApp(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	ln.Close()
	for _, tc := range []struct {
		name string
		f    fronts
	}{
		{"closed port", captureFrontsTo(t, "http://"+closed, nil)},
		{"forward down", captureFrontsTo(t, "http://"+closed, &Gate{Upstream: closed})},
	} {
		e := sendPart(t, tc.f.h1, "/upload", nil, bytes.Repeat([]byte("x"), 8<<10), 30<<10, false)
		if e.Status != http.StatusBadGateway || !e.ReqBodyShort || e.ReqBodyAborted || e.ReqBodySize != 0 || !e.ReqBodyNeverReached() {
			t.Errorf("%s: kept %d, short=%v aborted=%v never=%v, %d bytes", tc.name, e.Status, e.ReqBodyShort, e.ReqBodyAborted, e.ReqBodyNeverReached(), e.ReqBodySize)
		}
		const want = "this request's body never reached the app, so Nearhost has none of it to send again"
		if err := CanReplay(e); err == nil || err.Error() != want {
			t.Errorf("%s: CanReplay = %v", tc.name, err)
		}
		ClearCaptured()
	}
}

// An upgraded exchange, such as an HTTP/2 WebSocket whose stream is the
// request body, is never marked short.
func TestCaptureDoesNotMarkAnUpgrade(t *testing.T) {
	ClearCaptured()
	t.Cleanup(ClearCaptured)
	req := httptest.NewRequest("GET", "/ws", strings.NewReader("frames the app has not read yet"))
	req.ContentLength = -1
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		w.WriteHeader(http.StatusSwitchingProtocols)
		return nil
	})
	if err := (&Capture{Alias: "a.test"}).ServeHTTP(httptest.NewRecorder(), req, next); err != nil {
		t.Fatal(err)
	}
	if e := keptFor(t, "/ws"); !e.Upgraded || e.ReqBodyShort {
		t.Errorf("kept upgraded=%v short=%v", e.Upgraded, e.ReqBodyShort)
	}
}

// A client that gives up on its upload leaves the body short too, and the
// refusal says the client stopped.
func TestCaptureMarksABodyTheClientStoppedSending(t *testing.T) {
	f := captureFronts(t, answerAfter(-1, http.StatusOK))
	e := sendPart(t, f.h1, "/upload", nil, bytes.Repeat([]byte("x"), 8<<10), 30<<10, true)
	if !e.ReqBodyShort || !e.ReqBodyAborted || e.ReqBodySize != 8<<10 {
		t.Errorf("kept short=%v aborted=%v, %d bytes", e.ReqBodyShort, e.ReqBodyAborted, e.ReqBodySize)
	}
	const want = "the client stopped sending this request's body, so Nearhost cannot send it again"
	if err := CanReplay(e); err == nil || err.Error() != want {
		t.Errorf("CanReplay = %v", err)
	}
}

// A body read to its end, a body past the limit read to its end, and no body
// at all are not short.
func TestCaptureDoesNotMarkWholeBodies(t *testing.T) {
	defer func(n int) { captureBodyMax = n }(captureBodyMax)
	captureBodyMax = 4
	ClearCaptured()
	t.Cleanup(ClearCaptured)
	h := startProxyWith(t, []Site{{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: rawApp(t, answerAfter(-1, http.StatusCreated))}}, true)
	c := h.client("a.test", false)
	h.getEventually(c, "a.test", "/hello")
	for _, tc := range []struct {
		path, body string
		cut        bool
	}{
		{"/whole", "pin", false},
		{"/long", "hello world", true},
		{"/empty", "", false},
	} {
		r, err := c.Post(h.url("a.test", tc.path), "text/plain", strings.NewReader(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		e := keptFor(t, tc.path)
		if e.ReqBodyShort || e.ReqBodyAborted || e.ReqBodyCut() != tc.cut || e.ReqBodySize != int64(len(tc.body)) {
			t.Errorf("%s: kept short=%v aborted=%v cut=%v, %d bytes", tc.path, e.ReqBodyShort, e.ReqBodyAborted, e.ReqBodyCut(), e.ReqBodySize)
		}
	}
	r, err := c.Get(h.url("a.test", "/get"))
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if e := keptFor(t, "/get"); e.ReqBodyShort {
		t.Error("a GET is short")
	}
}

func TestCanReplayShortBodies(t *testing.T) {
	if err := CanReplay(Exchange{ReqBody: []byte("ab"), ReqBodySize: 2, ReqBodyLength: 10, ReqBodyShort: true}); err == nil ||
		err.Error() != "the app stopped reading this request's body, so Nearhost cannot send it again" {
		t.Errorf("a short body: %v", err)
	}
	if err := CanReplay(Exchange{ReqBodySize: 2, ReqBodyLength: -1, ReqBodyShort: true, ReqBodyAborted: true}); !errors.Is(err, ErrClientStoppedSending) {
		t.Errorf("an aborted body: %v", err)
	}
}

// An HTTP/3 request gives no length and has a body object even when the
// client sent no body. When nothing reads it, as when the forward is down
// and the gate answers, a method that has no body as a rule is not marked
// short and can be sent again; one that carries a body still is. A body
// some of which the app read is short whatever the method.
func TestCaptureUnknownLengthByMethod(t *testing.T) {
	ClearCaptured()
	t.Cleanup(ClearCaptured)
	const upstream = "127.0.0.1:1"
	t.Cleanup(func() { ForgetForward(upstream) })
	down := &Gate{Upstream: upstream}
	readSome := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		io.ReadFull(r.Body, make([]byte, 3))
		w.WriteHeader(http.StatusOK)
		return nil
	})
	for _, tc := range []struct {
		method, body string
		readSome     bool
		short, never bool
	}{
		{method: "GET"},
		{method: "HEAD"},
		{method: "OPTIONS"},
		{method: "DELETE"},
		{method: "TRACE"},
		{method: "CONNECT"},
		{method: "POST", short: true, never: true},
		{method: "PUT", short: true, never: true},
		{method: "PATCH", short: true, never: true},
		// Not told apart from no body: the lesser error is a replay
		// without it.
		{method: "DELETE", body: "payload"},
		{method: "GET", body: "payload", readSome: true, short: true},
		{method: "POST", body: "payload", readSome: true, short: true},
	} {
		uri := fmt.Sprintf("/%s-%d-%v", strings.ToLower(tc.method), len(tc.body), tc.readSome)
		req := httptest.NewRequest(tc.method, uri, nil)
		req.Body = io.NopCloser(strings.NewReader(tc.body))
		req.ContentLength = -1
		var next caddyhttp.Handler = caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
			return down.ServeHTTP(w, r, nil)
		})
		if tc.readSome {
			next = readSome
		}
		if err := (&Capture{Alias: "a.test"}).ServeHTTP(httptest.NewRecorder(), req, next); err != nil {
			t.Fatal(err)
		}
		e := keptFor(t, uri)
		if e.ReqBodyShort != tc.short || e.ReqBodyNeverReached() != tc.never || e.ReqBodyAborted {
			t.Errorf("%s: kept short=%v never=%v aborted=%v, %d bytes", uri, e.ReqBodyShort, e.ReqBodyNeverReached(), e.ReqBodyAborted, e.ReqBodySize)
		}
		if err := CanReplay(e); (err == nil) == tc.short {
			t.Errorf("%s: CanReplay = %v", uri, err)
		}
	}
}
