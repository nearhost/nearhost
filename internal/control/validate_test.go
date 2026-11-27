package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"

	"github.com/nearhost/nearhost/api"
)

// Every server the tests start checks its traffic against the schema: the
// answer always, and the request too when the answer is a success, since
// tests send bad requests on purpose. HEAD requests are not checked: the
// mux answers them on every GET route, and the schema lists none. The
// network handler's routes are checked as the network sees the schema: an
// operation not marked x-nearhost-network is not there.
func init() {
	wrapHandler = func(h http.Handler, network bool) http.Handler {
		v := newValidator(h)
		v.network = network
		return v
	}
}

// TestMain fails the run on any failure a validator recorded that no test
// reported, as from a server whose test did not call checkTraffic, or an
// answer that was still being checked when its test ended.
func TestMain(m *testing.M) {
	code := m.Run()
	validators.mu.Lock()
	all := validators.all
	validators.mu.Unlock()
	for _, v := range all {
		v.settle(time.Second)
		for _, f := range v.report() {
			fmt.Fprintf(os.Stderr, "schema: %s\n", f)
			code = 1
		}
	}
	os.Exit(code)
}

// checkTraffic reports, when t ends, the schema failures of s's traffic. It
// closes s first, so that every answer has been checked.
func checkTraffic(t testing.TB, s *Server) {
	t.Helper()
	v, ok := s.srv.Handler.(*validator)
	if !ok {
		t.Fatal("the server's handler is not wrapped by the schema validator")
	}
	t.Cleanup(func() {
		s.Close()
		if !v.settle(5 * time.Second) {
			t.Errorf("schema: a request was still being served 5s after the server closed")
		}
		for _, f := range v.report() {
			t.Errorf("schema: %s", f)
		}
	})
}

// schemaCheck is the schema, loaded once, and a router over its operations.
type schemaCheck struct {
	doc    *openapi3.T
	router routers.Router
}

var loadSchemaCheck = sync.OnceValues(func() (*schemaCheck, error) {
	doc, err := openapi3.NewLoader().LoadFromData(api.Schema)
	if err != nil {
		return nil, err
	}
	if err := doc.Validate(context.Background()); err != nil {
		return nil, err
	}
	r, err := gorillamux.NewRouter(doc)
	if err != nil {
		return nil, err
	}
	return &schemaCheck{doc: doc, router: r}, nil
})

// schemaOptions are the options every schema check of a value uses.
var schemaOptions = []openapi3.SchemaValidationOption{
	openapi3.EnableJSONSchema2020(),
	openapi3.VisitAsResponse(),
}

// validators is every validator made, for TestMain.
var validators struct {
	mu  sync.Mutex
	all []*validator
}

// validator is a handler that serves with next and checks each request and
// answer against the schema, recording what does not match.
type validator struct {
	next http.Handler
	// network is whether only operations marked x-nearhost-network are
	// served.
	network bool

	mu       sync.Mutex
	failures []string
	reported int
	inflight int
}

func newValidator(next http.Handler) *validator {
	v := &validator{next: next}
	validators.mu.Lock()
	validators.all = append(validators.all, v)
	validators.mu.Unlock()
	return v
}

func (v *validator) fail(format string, args ...any) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.failures = append(v.failures, brief(fmt.Sprintf(format, args...)))
}

// brief shortens a schema error, which spells out the whole schema and
// value that failed: it keeps what failed and the start of the value.
func brief(msg string) string {
	head, rest, ok := strings.Cut(msg, "\nSchema:\n")
	if !ok {
		return msg
	}
	_, value, _ := strings.Cut(rest, "\nValue:\n")
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 200 {
		value = value[:200] + "…"
	}
	return head + " (value: " + value + ")"
}

// report returns the failures recorded since the last report.
func (v *validator) report() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	f := append([]string(nil), v.failures[v.reported:]...)
	v.reported = len(v.failures)
	return f
}

// settle waits up to d for every request being served to be checked, and
// reports whether they were.
func (v *validator) settle(d time.Duration) bool {
	end := time.Now().Add(d)
	for {
		v.mu.Lock()
		n := v.inflight
		v.mu.Unlock()
		if n == 0 {
			return true
		}
		if time.Now().After(end) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (v *validator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	v.mu.Lock()
	v.inflight++
	v.mu.Unlock()
	defer func() {
		v.mu.Lock()
		v.inflight--
		v.mu.Unlock()
	}()
	if r.Method == http.MethodHead {
		v.next.ServeHTTP(w, r)
		return
	}
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	rec := &recorder{ResponseWriter: w, v: v, where: r.Method + " " + r.URL.RequestURI()}
	v.next.ServeHTTP(rec, r)
	v.check(r, body, rec)
}

// check checks the request r, whose body was body, and the answer rec
// recorded. An event stream's events were checked as they were written.
func (v *validator) check(r *http.Request, body []byte, rec *recorder) {
	chk, err := loadSchemaCheck()
	if err != nil {
		v.fail("load the schema: %v", err)
		return
	}
	ctx := context.Background()
	code := rec.status()
	req := r.Clone(ctx)
	u := *r.URL
	u.Scheme, u.Host = "http", "nearhost"
	req.URL, req.Host = &u, "nearhost"
	req.Body = io.NopCloser(bytes.NewReader(body))
	route, params, err := chk.router.FindRoute(req)
	if v.network {
		err = v.networkView(chk, req, route, err)
	}
	switch {
	case errors.Is(err, routers.ErrMethodNotAllowed):
		v.checkShared(chk, rec, code, http.StatusMethodNotAllowed, "MethodNotAllowed")
		return
	case errors.Is(err, routers.ErrPathNotFound):
		v.checkShared(chk, rec, code, http.StatusNotFound, "NoRoute")
		return
	case err != nil:
		v.fail("%s: find the operation: %v", rec.where, err)
		return
	}
	in := &openapi3filter.RequestValidationInput{
		Request:    req,
		PathParams: params,
		Route:      route,
		Options:    &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
	}
	if code/100 == 2 {
		if err := openapi3filter.ValidateRequest(ctx, in); err != nil {
			v.fail("%s: request: %v", rec.where, err)
		}
	}
	if resp := route.Operation.Responses.Status(code); resp != nil && resp.Value != nil &&
		len(resp.Value.Content) == 0 && rec.body.Len() > 0 {
		v.fail("%s: answered %d with a body, which the schema says is empty: %s", rec.where, code, rec.body.Bytes())
	}
	out := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: in,
		Status:                 code,
		Header:                 rec.Header(),
		Options: &openapi3filter.Options{
			IncludeResponseStatus: true,
			// The stream's events are checked one by one as written.
			// The filter decodes a YAML body into an object, which a
			// schema of type string rejects; TestSchemaIsServed holds the
			// body to the embedded bytes instead.
			ExcludeResponseBody: rec.stream || rec.Header().Get("Content-Type") == "application/yaml",
			AuthenticationFunc:  openapi3filter.NoopAuthenticationFunc,
		},
	}
	out.SetBodyBytes(rec.body.Bytes())
	if err := openapi3filter.ValidateResponse(ctx, out); err != nil {
		v.fail("%s: answer %d: %v", rec.where, code, err)
	}
}

// networkView returns the error finding req's route would have given if the
// schema held only the operations marked x-nearhost-network: route and err
// are what finding it in the whole schema gave.
func (v *validator) networkView(chk *schemaCheck, req *http.Request, route *routers.Route, err error) error {
	switch {
	case err == nil:
		if onNetwork(route.Operation) {
			return nil
		}
	case errors.Is(err, routers.ErrMethodNotAllowed):
	default:
		return err
	}
	// The path is in the schema: it is a wrong method if another method's
	// operation on it is on the network, and an unknown path if none is.
	for _, m := range []string{"GET", "PUT", "POST", "DELETE", "OPTIONS", "HEAD", "PATCH", "TRACE"} {
		if m == req.Method {
			continue
		}
		other := req.Clone(req.Context())
		other.Method = m
		if r, _, err := chk.router.FindRoute(other); err == nil && onNetwork(r.Operation) {
			return routers.ErrMethodNotAllowed
		}
	}
	return routers.ErrPathNotFound
}

// onNetwork reports whether op is marked x-nearhost-network and is not one
// the page host serves.
func onNetwork(op *openapi3.Operation) bool {
	net, _ := op.Extensions["x-nearhost-network"].(bool)
	page, _ := op.Extensions["x-nearhost-page"].(bool)
	return net && !page
}

// checkShared checks an answer to a request no operation of the schema
// takes: it must have the status want and match the shared response name.
func (v *validator) checkShared(chk *schemaCheck, rec *recorder, code, want int, name string) {
	if code != want {
		v.fail("%s: no operation in the schema takes it, so want %d, but it was answered %d", rec.where, want, code)
		return
	}
	ref := chk.doc.Components.Responses[name]
	if ref == nil || ref.Value == nil {
		v.fail("%s: the schema has no response %s", rec.where, name)
		return
	}
	for h, hdr := range ref.Value.Headers {
		if hdr.Value != nil && hdr.Value.Required && rec.Header().Get(h) == "" {
			v.fail("%s: answer %d has no %s header", rec.where, code, h)
		}
	}
	ct := rec.Header().Get("Content-Type")
	media := ref.Value.Content.Get(ct)
	if media == nil || media.Schema == nil {
		v.fail("%s: answer %d has the content type %q, which %s does not", rec.where, code, ct, name)
		return
	}
	var val any
	if err := json.Unmarshal(rec.body.Bytes(), &val); err != nil {
		v.fail("%s: answer %d is not JSON: %v", rec.where, code, err)
		return
	}
	if err := media.Schema.Value.VisitJSON(val, schemaOptions...); err != nil {
		v.fail("%s: answer %d does not match %s: %v", rec.where, code, name, err)
	}
}

// checkEvent checks one server-sent event: comment lines, and one data
// line whose payload is an Envelope.
func (v *validator) checkEvent(where string, block []byte) {
	var data []string
	for _, line := range strings.Split(string(block), "\n") {
		switch {
		case line == "", strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		default:
			v.fail("%s: event line %q is neither a comment nor data", where, line)
		}
	}
	switch len(data) {
	case 0:
		return
	case 1:
	default:
		v.fail("%s: event has %d data lines, want 1", where, len(data))
		return
	}
	chk, err := loadSchemaCheck()
	if err != nil {
		v.fail("load the schema: %v", err)
		return
	}
	var val any
	if err := json.Unmarshal([]byte(data[0]), &val); err != nil {
		v.fail("%s: event data is not JSON: %v: %s", where, err, data[0])
		return
	}
	if err := chk.doc.Components.Schemas["Envelope"].Value.VisitJSON(val, schemaOptions...); err != nil {
		v.fail("%s: event does not match Envelope: %v: %s", where, err, data[0])
	}
}

// recorder passes an answer through to the client, keeping its status and
// body, and checking an event stream's events as they are written.
type recorder struct {
	http.ResponseWriter
	v     *validator
	where string

	code    int
	body    bytes.Buffer
	stream  bool
	pending []byte
}

func (c *recorder) status() int {
	if c.code == 0 {
		return http.StatusOK
	}
	return c.code
}

func (c *recorder) WriteHeader(code int) {
	if c.code == 0 {
		c.code = code
		mt, _, _ := mime.ParseMediaType(c.Header().Get("Content-Type"))
		c.stream = mt == "text/event-stream"
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *recorder) Write(b []byte) (int, error) {
	if c.code == 0 {
		c.WriteHeader(http.StatusOK)
	}
	if c.stream {
		c.pending = append(c.pending, b...)
		for {
			i := bytes.Index(c.pending, []byte("\n\n"))
			if i < 0 {
				break
			}
			c.v.checkEvent(c.where, c.pending[:i])
			c.pending = c.pending[i+2:]
		}
	} else {
		c.body.Write(b)
	}
	return c.ResponseWriter.Write(b)
}

func (c *recorder) Flush() {
	if c.code == 0 {
		c.WriteHeader(http.StatusOK)
	}
	http.NewResponseController(c.ResponseWriter).Flush()
}

// Unwrap lets http.ResponseController reach the connection, to set write
// deadlines.
func (c *recorder) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// An answer with a member the schema does not have is a failure.
func TestValidatorRejectsAnUnknownField(t *testing.T) {
	v := newValidator(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"api":1,"surprise":true}`+"\n")
	}))
	v.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://nearhost/v1", nil))
	got := v.report()
	if len(got) == 0 {
		t.Fatal("the validator recorded no failure for an answer with an unknown member")
	}
	t.Log(got)
}

// Each data line of the event stream is an Envelope; comment lines are
// skipped.
func TestValidatorChecksEvents(t *testing.T) {
	v := newValidator(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, ": keepalive\n\n")
		io.WriteString(w, `data: {"seq":1,"kind":"surprise"}`+"\n\n")
	}))
	v.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://nearhost/v1/events", nil))
	if got := v.report(); len(got) != 1 {
		t.Fatalf("the validator recorded %d failures, want 1: %q", len(got), got)
	}
}

// A time that is not RFC 3339 is a failure.
func TestValidatorChecksTimes(t *testing.T) {
	v := newValidator(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := newFake().info
		data, _ := json.Marshal(info)
		data = bytes.Replace(data, []byte(info.Started.Format(time.RFC3339)), []byte("yesterday"), 1)
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}))
	v.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://nearhost/v1", nil))
	got := v.report()
	if len(got) != 1 || !strings.Contains(got[0], "started") {
		t.Fatalf("the validator recorded %q, want one failure at started", got)
	}
}

// A list or map the core has none of may be nil, sent as null, or empty:
// the schema takes both, in the status and the state.
func TestNilAndEmptyListsMatchTheSchema(t *testing.T) {
	for _, empty := range []bool{false, true} {
		b := newFake()
		b.status = Status{Info: b.info}
		b.state = State{Info: b.info}
		if empty {
			makeEmpty(&b.status)
			makeEmpty(&b.state)
		}
		c := dial(t, serve(t, b))
		if _, err := c.Status(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := c.State(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

// makeEmpty sets each nil slice and map field of the struct p points to to
// an empty one.
func makeEmpty(p any) {
	v := reflect.ValueOf(p).Elem()
	for i := range v.NumField() {
		f := v.Field(i)
		switch {
		case !f.CanSet():
		case f.Kind() == reflect.Slice && f.IsNil():
			f.Set(reflect.MakeSlice(f.Type(), 0, 0))
		case f.Kind() == reflect.Map && f.IsNil():
			f.Set(reflect.MakeMap(f.Type()))
		}
	}
}
