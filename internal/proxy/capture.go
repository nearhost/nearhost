package proxy

import (
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
	ReqBodySize       int64 // the whole body's size; ReqBody may be cut short
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

// RespBodyCut reports whether the response body is longer than what was
// kept.
func (e Exchange) RespBodyCut() bool { return int64(len(e.RespBody)) < e.RespBodySize }

func (e *Exchange) size() int { return len(e.ReqBody) + len(e.RespBody) }

// captures are the exchanges kept, oldest first. Caddy is process-global,
// so they are too.
var captures = struct {
	sync.Mutex
	order []string
	byID  map[string]*Exchange
	total int
}{byID: map[string]*Exchange{}}

// keep stores e, letting the oldest go past the limits.
func keep(e *Exchange) {
	captures.Lock()
	defer captures.Unlock()
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
	id := newID()
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
	if reqBody != nil {
		e.ReqBody, e.ReqBodySize = reqBody.buf, reqBody.n
	}
	e.RespBody, e.RespBodySize = cw.body.buf, cw.body.n
	if err != nil {
		e.Err = err.Error()
	}
	keep(e)
	return err
}

// newID names an exchange.
func newID() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// limited keeps the first captureBodyMax bytes written to it and counts
// them all.
type limited struct {
	buf []byte
	n   int64
}

func (l *limited) Write(p []byte) (int, error) {
	if room := captureBodyMax - len(l.buf); room > 0 {
		l.buf = append(l.buf, p[:min(room, len(p))]...)
	}
	l.n += int64(len(p))
	return len(p), nil
}

// teeBody copies what is read from a request body into to.
type teeBody struct {
	io.ReadCloser
	to *limited
}

func (t *teeBody) Read(p []byte) (int, error) {
	n, err := t.ReadCloser.Read(p)
	t.to.Write(p[:n])
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
