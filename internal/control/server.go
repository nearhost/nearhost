package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Backend is the core, as the control API sees it.
type Backend interface {
	Info() Info
	Status() Status
	// Stop starts the shutdown and returns at once.
	Stop()
	// HostsChanged retries the projects that failed on the hosts file.
	HostsChanged(ctx context.Context)
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
}

// Listen starts serving the control API for b on a Unix socket at path. It
// makes the socket's directory, private to the user, and the socket readable
// and writable by the user alone. A socket that answers belongs to a running
// core, which Listen leaves be and reports as ErrInUse; one that does not is
// left from a core that was killed, and is replaced.
func Listen(path string, b Backend) (*Server, error) {
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
	s := &Server{b: b, l: l}
	s.srv = &http.Server{
		Handler:           http.HandlerFunc(s.serve),
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

// serve routes a request. The routes are matched by hand, not with ServeMux
// patterns, so that a wrong method and an unknown path both answer in JSON.
func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	var method string
	switch r.URL.Path {
	case "/v1":
		method = http.MethodGet
	case "/v1/status":
		method = http.MethodGet
	case "/v1/stop", "/v1/hosts/changed":
		method = http.MethodPost
	default:
		fail(w, http.StatusNotFound, "no such route: "+r.URL.Path)
		return
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		fail(w, http.StatusMethodNotAllowed, fmt.Sprintf("%s takes %s, not %s", r.URL.Path, method, r.Method))
		return
	}
	switch r.URL.Path {
	case "/v1":
		reply(w, http.StatusOK, s.b.Info())
	case "/v1/status":
		reply(w, http.StatusOK, s.b.Status())
	case "/v1/stop":
		// Answer before the shutdown starts: the shutdown closes this
		// socket, and would cut the answer off, leaving the client an EOF.
		// The length says the answer is whole once flushed.
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusAccepted)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		s.b.Stop()
	case "/v1/hosts/changed":
		s.b.HostsChanged(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}
}

// reply answers with v as JSON.
func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// fail answers with a JSON body {"error": msg}.
func fail(w http.ResponseWriter, code int, msg string) {
	reply(w, code, errorBody{Error: msg})
}
