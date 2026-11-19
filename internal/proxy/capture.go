package proxy

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"
)

// captureModule is the Caddy handler module name registered below.
const captureModule = "nearhost_capture"

// The limits of what is captured. A body is kept up to captureBodyMax
// bytes; past captureMax exchanges, or captureTotalMax bytes of bodies, the
// oldest go first. Tests lower them.
var (
	captureMax      = 1000
	captureBodyMax  = 64 << 10
	captureTotalMax = 32 << 20
)

// Exchange is a request the proxy passed on and the response it got, as
// far as they were kept. It holds real header values, such as cookies, which
// the access log redacts, and lives only in memory.
type Exchange struct {
	ID, Alias         string
	Method, Host, URI string
	Proto             string
	ReqHeader         http.Header
	ReqBody           []byte
	ReqBodySize       int64 // the bytes of the body the app was sent; ReqBody may be cut short
	ReqBodyLength     int64 // the Content-Length the client gave, or -1 when it gave none
	ReqBodyShort      bool  // the app was not sent the whole body: it answered first, or the client stopped
	ReqBodyAborted    bool  // the client stopped sending the body before its end; ReqBodyShort is set too
	Status            int
	RespHeader        http.Header
	RespBody          []byte
	RespBodySize      int64
	Upgraded          bool   // a 101 switch, such as a WebSocket: no body is kept
	Err               string // why the proxy could not answer, such as the upstream being down
	At                time.Time
}

// ReqBodyCut reports whether the request body is longer than what was kept.
func (e Exchange) ReqBodyCut() bool { return int64(len(e.ReqBody)) < e.ReqBodySize }

// ReqBodyNeverReached reports whether none of a request body reached the
// app, through no fault of the client: the app was down, or answered
// without reading any of it.
func (e Exchange) ReqBodyNeverReached() bool {
	return e.ReqBodyShort && !e.ReqBodyAborted && e.ReqBodySize == 0
}

// RespBodyCut reports whether the response body is longer than what was
// kept.
func (e Exchange) RespBodyCut() bool { return int64(len(e.RespBody)) < e.RespBodySize }

// size is roughly the memory an exchange holds: its bodies' room and its
// headers.
func (e *Exchange) size() int {
	return cap(e.ReqBody) + cap(e.RespBody) + headerSize(e.ReqHeader) + headerSize(e.RespHeader)
}

// headerSize is roughly the memory a header holds.
func headerSize(h http.Header) int {
	n := 0
	for name, values := range h {
		n += len(name)
		for _, v := range values {
			n += len(v)
		}
	}
	return n
}

// captures are the exchanges kept, oldest first. Caddy is process-global,
// so they are too.
var captures = struct {
	sync.Mutex
	order []string
	byID  map[string]*Exchange
	total int
	gen   int // counts the times the store was cleared
}{byID: map[string]*Exchange{}}

// captureGen is the store's generation, taken when an exchange begins: one
// that ends after the store was cleared is not kept.
func captureGen() int {
	captures.Lock()
	defer captures.Unlock()
	return captures.gen
}

// keep stores e, begun in generation gen, letting the oldest go past the
// limits.
func keep(e *Exchange, gen int) {
	captures.Lock()
	defer captures.Unlock()
	if gen != captures.gen {
		return // the store was cleared while e was open
	}
	captures.order = append(captures.order, e.ID)
	captures.byID[e.ID] = e
	captures.total += e.size()
	for len(captures.order) > captureMax || (captures.total > captureTotalMax && len(captures.order) > 1) {
		old := captures.byID[captures.order[0]]
		captures.total -= old.size()
		delete(captures.byID, old.ID)
		captures.order = captures.order[1:]
	}
}

// Captured is the exchange with id, while it is kept.
func Captured(id string) (Exchange, bool) {
	captures.Lock()
	defer captures.Unlock()
	e, ok := captures.byID[id]
	if !ok {
		return Exchange{}, false
	}
	return *e, true
}

// ClearCaptured forgets every exchange.
func ClearCaptured() {
	captures.Lock()
	defer captures.Unlock()
	captures.order, captures.byID, captures.total = nil, map[string]*Exchange{}, 0
	captures.gen++
}

func init() {
	caddy.RegisterModule(Capture{})
}

// Capture is a Caddy HTTP handler that keeps each request and the response
// to it, bodies cut at captureBodyMax, and names the exchange in the access
// log as nearhost_id. Nothing reaches the upstream that would not have.
type Capture struct {
	Alias string `json:"alias,omitempty"`
}

// CaddyModule implements caddy.Module.
func (Capture) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers." + captureModule,
		New: func() caddy.Module { return new(Capture) },
	}
}

// ServeHTTP implements caddyhttp.MiddlewareHandler.
func (c *Capture) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	id, gen := newID(), captureGen()
	if extra, ok := r.Context().Value(caddyhttp.ExtraLogFieldsCtxKey).(*caddyhttp.ExtraLogFields); ok {
		extra.Set(zap.String("nearhost_id", id))
	}
	e := &Exchange{
		ID: id, Alias: c.Alias, Method: r.Method, Host: r.Host, URI: r.RequestURI, Proto: r.Proto,
		ReqHeader: r.Header.Clone(), At: time.Now(),
	}
	var reqBody *limited
	if r.Body != nil && r.Body != http.NoBody {
		reqBody = &limited{}
		r.Body = &teeBody{ReadCloser: r.Body, to: reqBody}
	}
	cw := &captureWriter{ResponseWriterWrapper: &caddyhttp.ResponseWriterWrapper{ResponseWriter: w}, e: e}
	err := next.ServeHTTP(cw, r)
	e.ReqBodyLength = r.ContentLength
	if reqBody != nil {
		// The transport may still be sending the request body, when the
		// upstream answered without reading it: take what was read so far
		// and keep no more. From here the tee hands the transport nothing
		// more either, so what was counted is all the app was sent.
		e.ReqBody, e.ReqBodySize = reqBody.done()
		// An upgrade, such as an HTTP/2 WebSocket, carries its stream as
		// the body: it has no end to reach.
		if !e.Upgraded {
			eof, failed := reqBody.ending()
			e.ReqBodyAborted = failed
			if r.ContentLength >= 0 {
				e.ReqBodyShort = e.ReqBodySize < r.ContentLength
			} else {
				// HTTP/3 gives no length, and a body object, to a
				// request that has no body. When nothing read it, as
				// when the gate or a failed dial answered, a method
				// with no body as a rule had none. A real body on such
				// a method that nothing read looks the same, and its
				// replay goes without it.
				e.ReqBodyShort = !eof && (e.ReqBodySize > 0 || failed || carriesBody(r.Method))
			}
		}
	}
	e.RespBody, e.RespBodySize = cw.body.done()
	if err != nil {
		e.Err = err.Error()
	}
	keep(e, gen)
	return err
}

// carriesBody reports whether a request with method carries a body as a
// rule. It is the rule Caddy's reverse proxy applies to a request of
// unknown length.
func carriesBody(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions,
		http.MethodDelete, http.MethodTrace, http.MethodConnect:
		return false
	}
	return true
}

// newID names an exchange.
func newID() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// limited keeps the first captureBodyMax bytes written to it and counts
// them all, until done. It is safe for concurrent use: a request body can
// still be read by the transport after the handler has finished.
type limited struct {
	mu     sync.Mutex
	buf    []byte
	n      int64
	closed bool
	eof    bool // a request body was read to its end
	failed bool // reading a request body failed: the client stopped sending it
}

func (l *limited) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.write(p)
	return len(p), nil
}

// write keeps p, until done. The caller holds mu.
func (l *limited) write(p []byte) {
	if l.closed {
		return
	}
	if room := captureBodyMax - len(l.buf); room > 0 {
		l.buf = append(l.buf, p[:min(room, len(p))]...)
	}
	l.n += int64(len(p))
}

// read keeps what a read of a request body returned, and how it ended. It
// reports false when the exchange is already done, and what was read must
// not be passed on.
func (l *limited) read(p []byte, err error) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return false
	}
	l.write(p)
	switch {
	case err == io.EOF:
		l.eof = true
	case err != nil:
		l.failed = true
	}
	return true
}

// isDone reports whether done was called.
func (l *limited) isDone() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closed
}

// ending reports whether a request body was read to its end, and whether
// reading it failed.
func (l *limited) ending() (eof, failed bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.eof, l.failed
}

// done returns what was kept and how much was written, and keeps nothing
// more.
func (l *limited) done() ([]byte, int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	// Kept bodies hold only what they need, not what append grew them to.
	return bytes.Clone(l.buf), l.n
}

// teeBody copies what is read from a request body into to. Once to is
// done, it reads as a closed body, so the app is sent no byte that was not
// counted: the server closes the body when the handler returns anyway.
type teeBody struct {
	io.ReadCloser
	to *limited
}

func (t *teeBody) Read(p []byte) (int, error) {
	if t.to.isDone() {
		return 0, http.ErrBodyReadAfterClose
	}
	n, err := t.ReadCloser.Read(p)
	if !t.to.read(p[:n], err) {
		// The exchange ended while this read waited: what it got is
		// dropped, as it was not counted.
		return 0, http.ErrBodyReadAfterClose
	}
	return n, err
}

// captureWriter records the response as it is written. Embedding the
// wrapper keeps Unwrap, so http.ResponseController can still hijack the
// connection for a WebSocket and flush a stream.
type captureWriter struct {
	*caddyhttp.ResponseWriterWrapper
	e      *Exchange
	body   limited
	headed bool
}

func (cw *captureWriter) WriteHeader(code int) {
	// Informational answers, such as 103 Early Hints, come before the real
	// one; 101 is the last there is.
	if !cw.headed && (code >= 200 || code == http.StatusSwitchingProtocols) {
		cw.headed = true
		cw.e.Status = code
		cw.e.RespHeader = cw.Header().Clone()
		cw.e.Upgraded = code == http.StatusSwitchingProtocols
	}
	cw.ResponseWriterWrapper.WriteHeader(code)
}

func (cw *captureWriter) Write(p []byte) (int, error) {
	if !cw.headed {
		cw.WriteHeader(http.StatusOK)
	}
	n, err := cw.ResponseWriterWrapper.Write(p)
	cw.body.Write(p[:n])
	return n, err
}

// ReadFrom copies through Write, so nothing reaches the client unseen.
func (cw *captureWriter) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(writerOnly{cw}, r)
}

// writerOnly hides ReadFrom, so io.Copy does not call it back.
type writerOnly struct{ io.Writer }

var (
	_ caddyhttp.MiddlewareHandler = (*Capture)(nil)
	_ io.ReaderFrom               = (*captureWriter)(nil)
)
