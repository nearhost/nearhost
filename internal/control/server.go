package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/nearhost/nearhost/api"
	"github.com/nearhost/nearhost/internal/report"
)

// Backend is the core, as the control API sees it.
type Backend interface {
	Info() Info
	Status() Status
	// State is everything a client mirrors, but for Seq and Clients, which
	// the server sets. It must include every envelope published before it
	// is called, and may include ones published while it runs.
	State() State
	// Stop starts the shutdown and returns at once.
	Stop()
	// HostsChanged retries the projects that failed on the hosts file.
	HostsChanged(ctx context.Context)
	Actions
}

// ErrInUse is returned by Listen when another core answers on the socket.
var ErrInUse = errors.New("another Nearhost answers on the control socket")

// probeTimeout is how long Listen waits for a socket to answer before it takes
// it for a leftover.
const probeTimeout = 500 * time.Millisecond

// Server serves the control API on a Unix socket.
type Server struct {
	srv *http.Server
	l   net.Listener
	b   Backend
	hub *Hub
	// keepalive is keepaliveEvery when the server started.
	keepalive time.Duration
	// mux and patterns are set by routes: the routes, and the pattern of
	// each in the order handle registered them.
	mux      *http.ServeMux
	patterns []string
}

// handle registers h for pattern, such as "GET /v1/state", on the server's
// mux, and records the pattern. Every route goes through it, so Patterns
// lists them all.
func (s *Server) handle(pattern string, h http.HandlerFunc) {
	s.mux.HandleFunc(pattern, h)
	s.patterns = append(s.patterns, pattern)
}

// Patterns returns the pattern of every route routes registered, in the
// order it registered them. The slice is a copy.
func (s *Server) Patterns() []string {
	return slices.Clone(s.patterns)
}

// keepaliveEvery is how often an idle stream is sent a comment, so that
// neither end takes it for dead. Tests shorten it before Listen.
var keepaliveEvery = 15 * time.Second

// wrapHandler, when set, wraps the handler of every server Listen starts.
// Tests set it to check each request and answer against the API's schema.
var wrapHandler func(http.Handler) http.Handler

// Listen starts serving the control API for b on a Unix socket at path,
// streaming what is published to hub. When hub is nil, Listen makes a hub
// of its own, to which nothing publishes. It makes the socket's directory,
// private to the user, and the socket readable and writable by the user
// alone. A socket that answers belongs to a running core, which Listen
// leaves be and reports as ErrInUse; one that does not is left from a core
// that was killed, and is replaced.
func Listen(path string, b Backend, hub *Hub) (*Server, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if answers(path) {
		return nil, ErrInUse
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	if hub == nil {
		hub = NewHub()
	}
	s := &Server{b: b, l: l, hub: hub, keepalive: keepaliveEvery}
	h := s.routes()
	if wrapHandler != nil {
		h = wrapHandler(h)
	}
	s.srv = &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go s.srv.Serve(l)
	return s, nil
}

// answers reports whether a core answers 200 to GET /v1 on the socket at path
// within probeTimeout.
func answers(path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	_, err := newClient(path).Info(ctx)
	return err == nil
}

// Close stops serving and removes the socket.
func (s *Server) Close() error {
	err := s.srv.Close()
	// Close the listener too: if Serve has not started yet, the server's
	// Close does not know of it and would leave the socket answering.
	if lerr := s.l.Close(); err == nil && !errors.Is(lerr, net.ErrClosed) {
		err = lerr
	}
	return err
}

// routes returns the handler of the API. Unknown paths answer 404 and wrong
// methods 405, in JSON like every other failure, which ServeMux alone would
// answer in plain text.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	s.mux, s.patterns = mux, nil
	s.handle("GET /v1", func(w http.ResponseWriter, r *http.Request) {
		reply(w, http.StatusOK, s.b.Info())
	})
	s.handle("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		reply(w, http.StatusOK, s.b.Status())
	})
	s.handle("GET /v1/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		w.Write(api.Schema)
	})
	s.handle("GET /v1/state", func(w http.ResponseWriter, r *http.Request) {
		// The number is read first: the state then includes every
		// envelope up to it, and may include some after it.
		seq := s.hub.Seq()
		st := s.b.State()
		st.Seq, st.Clients = seq, s.hub.Clients()
		reply(w, http.StatusOK, st)
	})
	s.handle("GET /v1/events", s.stream)
	s.handle("POST /v1/stop", func(w http.ResponseWriter, r *http.Request) {
		// Answer before the shutdown starts: the shutdown closes this
		// socket, and would cut the answer off, leaving the client an EOF.
		// The length says the answer is whole once flushed.
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusAccepted)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		s.b.Stop()
	})
	s.handle("POST /v1/hosts/changed", func(w http.ResponseWriter, r *http.Request) {
		s.b.HostsChanged(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})
	s.registerActions()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}
		// No route matches: let the mux say which way, then say it in JSON.
		probe := &probeWriter{header: http.Header{}}
		h.ServeHTTP(probe, r)
		if probe.code == http.StatusMethodNotAllowed {
			allow := probe.header.Get("Allow")
			w.Header().Set("Allow", allow)
			fail(w, http.StatusMethodNotAllowed, report.Errorf(report.ControlMethodNotAllowed, "%s does not take %s; it takes %s", r.URL.Path, r.Method, allow))
			return
		}
		fail(w, http.StatusNotFound, report.Errorf(report.ControlNoRoute, "no such route: %s", r.URL.Path))
	})
}

// probeWriter is a ResponseWriter that keeps only the status and headers.
type probeWriter struct {
	header http.Header
	code   int
}

func (p *probeWriter) Header() http.Header         { return p.header }
func (p *probeWriter) Write(b []byte) (int, error) { return len(b), nil }
func (p *probeWriter) WriteHeader(code int)        { p.code = code }

// streamWriteTimeout is how long a write to a stream may wait on a client
// that does not read.
var streamWriteTimeout = 30 * time.Second

// stream sends the envelopes after the one numbered by the query's after, 0
// when it has none, as server-sent events, until the client goes or the
// server closes.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	var after uint64
	if q := r.URL.Query().Get("after"); q != "" {
		n, err := strconv.ParseUint(q, 10, 64)
		if err != nil {
			fail(w, http.StatusBadRequest, report.Errorf(report.ControlBadRequest, "after must be an envelope number, not %q", q))
			return
		}
		after = n
	}
	// Attached before the answer starts, so a client counts as attached
	// once Events returns.
	sub, backlog := s.hub.subscribe(after)
	defer s.hub.unsubscribe(sub)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	// Each write has a deadline, so a client that stops reading, as a
	// suspended interface does, is let go rather than counted as attached
	// for good: it fetches the state again when it reads once more.
	rc := http.NewResponseController(w)
	deadline := func() error { return rc.SetWriteDeadline(time.Now().Add(streamWriteTimeout)) }
	flush := func() error { return rc.Flush() }
	if deadline() != nil || flush() != nil {
		return
	}
	if writeEnvelopes(w, backlog, deadline) != nil || deadline() != nil || flush() != nil {
		return
	}
	keepalive := time.NewTicker(s.keepalive)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-sub.ready:
			if writeEnvelopes(w, sub.take(), deadline) != nil {
				return
			}
		case <-keepalive.C:
			if deadline() != nil {
				return
			}
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
		}
		// The flush gets a deadline of its own.
		if deadline() != nil || flush() != nil {
			return
		}
	}
}

// writeEnvelopes writes each of es as one server-sent event, calling
// deadline before each, so a long batch to a client that reads is not cut
// short: only a write that waits too long is.
func writeEnvelopes(w io.Writer, es []Envelope, deadline func() error) error {
	for _, e := range es {
		data, err := json.Marshal(e)
		if err != nil {
			return err
		}
		if err := deadline(); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return err
		}
	}
	return nil
}

// reply answers with v as JSON. A v that does not marshal, as a message
// whose data holds a value JSON cannot, answers 500 with why, rather than
// a 200 with no body.
func reply(w http.ResponseWriter, code int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		report.WriteProblemStatus(w, report.Errorf(report.Unexpected, "The core's answer could not be written as JSON.").Wrap(err).
			Because("%s", err), http.StatusInternalServerError, nil)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(append(data, '\n'))
}

// fail answers with m as problem details, with the status code code, which
// a client of API version 1 has always been answered with. Its `error` is
// m's title, as before problem details.
func fail(w http.ResponseWriter, code int, m *report.Message) {
	report.WriteProblemStatus(w, m, code, nil)
}
