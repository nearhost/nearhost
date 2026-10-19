// Package agentapi is the HTTP API a coding agent uses to reach nearhost: it
// reads the status of the projects on its machine, proposes a project to
// add, which the user reviews in the interface, asks the doctor what is wrong
// with a project or a directory, and has nearhost reload a project's
// .nearhostrc. GET /v1 lists which of the doctor and reload a server has.
//
// It serves JSON on Unix sockets, one per origin: "local" for this machine's
// own socket, or a server's name for the socket nearhost forwards to that
// server. A request is attributed to the socket it arrives on, so a request
// from a server sees and proposes only that server's projects.
package agentapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nearhost/nearhost/internal/doctor"
)

// Version is the API version, reported by GET /v1.
const Version = 1

const (
	// maxBody bounds a request body.
	maxBody = 4 << 10
	// maxPending is how many proposals one origin may have waiting.
	maxPending = 3
	// expiry is how long a proposal waits for an answer.
	expiry = 10 * time.Minute
	// maxWait bounds one long poll.
	maxWait = 50 * time.Second
	// keep is how long an answered or expired proposal can still be asked
	// about.
	keep = 10 * time.Minute
	// doctorTimeout bounds one call to the doctor, which may read a
	// project's files over ssh.
	doctorTimeout = 45 * time.Second
	// reloadTimeout bounds one re-read of a project's .nearhostrc, which
	// may read it over ssh.
	reloadTimeout = 45 * time.Second
)

// Route is one of a project's ports and what nearhost does with it.
type Route struct {
	Port    int    `json:"port"`
	Role    string `json:"role"`
	Label   string `json:"label"`
	Applied bool   `json:"applied"`
	// Hosts are the hosts a web route is served at, patterns as written.
	Hosts []string `json:"hosts,omitempty"`
}

// Project is a configured project as an agent sees it.
type Project struct {
	Name string `json:"name"`
	Path string `json:"path"`
	URL  string `json:"url"`
	// URLs is every URL the project's web routes are served at, the alias
	// URL first, so an agent can tell a user where each service lives.
	URLs   []string `json:"urls"`
	State  string   `json:"state"`
	Web    int      `json:"web"`
	Routes []Route  `json:"routes"`
	Error  string   `json:"error,omitempty"`
}

// Source answers the status calls.
type Source interface {
	// Projects lists the projects on origin's machine.
	Projects(origin string) []Project
}

// Doctor works out the findings for agents.
type Doctor interface {
	// Doctor checks the project called name, whose directory is dir on
	// origin's machine, or, when name is "", the directory dir as it would
	// be added. An error satisfying errors.Is(err, fs.ErrNotExist) means dir
	// is not a directory there.
	Doctor(ctx context.Context, origin, name, dir string) ([]doctor.Finding, error)
}

// Reloader re-reads a project's .nearhostrc for an agent.
type Reloader interface {
	Reload(ctx context.Context, name string) (Reloaded, error)
	// Waiting is the hash of the change of the project's .nearhostrc that
	// waits for approval, or "" when none does. It must not block.
	Waiting(name string) string
}

// Reloaded is what a reload found.
type Reloaded struct {
	Items    []string // what waits for approval, by Item.Name(); empty when nothing does
	SHA256   string   // the waiting change's hash
	Declined bool     // the file asks for a change the user declined; it stays declined until the file changes
	Problem  string   // why the file could not be taken into account
}

// Proposal is a project an agent asks to add.
type Proposal struct {
	ID     string
	Origin string
	Path   string // absolute, on the origin's machine
	Name   string // empty when the agent left it to discovery
	Web    int    // 0 when the agent did not say
	// Expires is when the proposal stops waiting for an answer.
	Expires time.Time
}

// Reviewer is shown each proposal as it arrives. Review must not block.
type Reviewer interface {
	Review(p Proposal)
}

// State is where a proposal is.
type State string

const (
	StatePending  State = "pending"
	StateAccepted State = "accepted"
	StateDeclined State = "declined"
	StateExpired  State = "expired"
	// StateExists answers a proposal for a path a project already holds.
	StateExists State = "exists"
	// StateSuperseded ends a reload waiting on a change that no longer
	// waits: a newer reload found the file changed again, the user answered
	// a newer change, or the change stopped waiting some other way.
	StateSuperseded State = "superseded"
)

// ErrInUse is returned by Listen when another process answers on the socket.
var ErrInUse = errors.New("another process is serving on the socket")

// Options configures a Server.
type Options struct {
	// Laptop names this machine to agents, as user@host.
	Laptop string
	Source Source
	// Doctor answers GET /v1/doctor; nil means no doctor.
	Doctor Doctor
	// Reload answers POST /v1/projects/reload; nil means no reload.
	Reload Reloader
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Server is the API on every socket it listens on. Its methods are safe for
// concurrent use.
type Server struct {
	opts    Options
	handler http.Handler

	mu        sync.Mutex
	reviewer  Reviewer
	listeners map[string]*listener // by origin
	proposals map[string]*proposal // by ID
	closed    bool
}

type listener struct {
	path string
	srv  *http.Server
}

// proposal is a Proposal and what became of it. done is closed when it is
// answered or expires.
//
// A reload waiting on a change of a project's .nearhostrc is one too, with
// reload set, so an agent polls it as it polls a proposal. Its Name is the
// project's and its Path the project's directory, and sha is the hash of the
// change it waits on. The user answers the change, not the reload, so a
// reload is never shown to the Reviewer, and it does not count against
// maxPending: an agent waiting on its .nearhostrc may still propose.
type proposal struct {
	Proposal
	state   State
	reason  string
	project string // the name it was saved as, when accepted
	ended   time.Time
	done    chan struct{}
	reload  bool
	sha     string
}

type originKey struct{}

// New creates a server that listens nowhere yet.
func New(opts Options) *Server {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	s := &Server{opts: opts, listeners: map[string]*listener{}, proposals: map[string]*proposal{}}
	s.handler = s.routes()
	return s
}

// SetReviewer sets who reviews proposals. Without one (nil, as under
// `nearhost up`) proposals are refused.
func (s *Server) SetReviewer(r Reviewer) {
	s.mu.Lock()
	s.reviewer = r
	s.mu.Unlock()
}

// Listen serves the API for origin on a Unix socket at path, creating its
// directory with mode 0700. A socket left at path by a process that has gone
// is removed first; one that still answers is left alone and ErrInUse
// returned. Listening again for an origin moves it to the new path.
func (s *Server) Listen(origin, path string) error {
	s.mu.Lock()
	same := s.listeners[origin] != nil && s.listeners[origin].path == path
	s.mu.Unlock()
	if same {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if answers(path) {
		return ErrInUse
	}
	os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return err
	}
	srv := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext: func(net.Listener) context.Context {
			return context.WithValue(context.Background(), originKey{}, origin)
		},
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		l.Close()
		return errors.New("the agent API is closed")
	}
	old := s.listeners[origin]
	s.listeners[origin] = &listener{path: path, srv: srv}
	s.mu.Unlock()
	if old != nil {
		old.close()
	}
	go srv.Serve(l)
	return nil
}

// Unlisten stops serving origin and removes its socket.
func (s *Server) Unlisten(origin string) {
	s.mu.Lock()
	l := s.listeners[origin]
	delete(s.listeners, origin)
	s.mu.Unlock()
	if l != nil {
		l.close()
	}
}

// Listening reports the origins served, for tests and the interface.
func (s *Server) Listening(origin string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listeners[origin] != nil
}

// Close stops serving everywhere and removes the sockets. Proposals still
// waiting end their long polls: the connections close with the servers.
func (s *Server) Close() {
	s.mu.Lock()
	s.closed = true
	ls := s.listeners
	s.listeners = map[string]*listener{}
	s.mu.Unlock()
	for _, l := range ls {
		l.close()
	}
}

func (l *listener) close() {
	l.srv.Close()
	os.Remove(l.path)
}

// answers reports whether something accepts connections on the socket at
// path.
func answers(path string) bool {
	c, err := net.DialTimeout("unix", path, 500*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// Accept answers a proposal: the user saved it as the project called name.
// An answer to a proposal that has already ended is dropped.
func (s *Server) Accept(id, name string) {
	s.end(id, StateAccepted, "", name)
}

// Decline answers a proposal: the user dismissed it, for reason when there
// is one.
func (s *Server) Decline(id, reason string) {
	s.end(id, StateDeclined, reason, "")
}

// declinedReason is why a reload ends declined, whether the user declined
// the change while it waited or before it was asked for.
const declinedReason = "the user declined this change; change .nearhostrc to ask again"

// ChangeAnswered ends the reloads waiting on the change with hash sha of
// the project called name: accepted when approved, declined otherwise. The
// user answered the change the file asks for now, so a reload waiting on
// another change of the project ends superseded.
func (s *Server) ChangeAnswered(name, sha string, approved bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.proposals {
		switch {
		case !p.reload || p.Name != name:
		case p.sha != sha:
			s.endLocked(p, StateSuperseded, "", "")
		case approved:
			s.endLocked(p, StateAccepted, "", name)
		default:
			s.endLocked(p, StateDeclined, declinedReason, "")
		}
	}
}

// supersedeStale ends p superseded when it is a reload whose change no
// longer waits for approval, so an agent is not left waiting on a change
// nobody can answer. Waiting is asked without s.mu held; an answer that
// ends p meanwhile stands, as endLocked leaves an ended record alone.
func (s *Server) supersedeStale(p *proposal) {
	if !p.reload || s.opts.Reload == nil || s.opts.Reload.Waiting(p.Name) == p.sha {
		return
	}
	s.mu.Lock()
	s.endLocked(p, StateSuperseded, "", "")
	s.mu.Unlock()
}

func (s *Server) end(id string, state State, reason, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.proposals[id]; p != nil && !p.reload {
		s.endLocked(p, state, reason, name)
	}
}

// endLocked answers p, unless it has already ended.
func (s *Server) endLocked(p *proposal, state State, reason, name string) {
	s.expireLocked(p)
	if p.state != StatePending {
		return
	}
	p.state, p.reason, p.project, p.ended = state, reason, name, s.opts.Now()
	close(p.done)
}

// expireLocked ends a pending proposal whose time has passed.
func (s *Server) expireLocked(p *proposal) {
	if p.state == StatePending && !s.opts.Now().Before(p.Expires) {
		p.state, p.ended = StateExpired, p.Expires
		close(p.done)
	}
}

// sweepLocked forgets proposals that ended long enough ago.
func (s *Server) sweepLocked() {
	now := s.opts.Now()
	for id, p := range s.proposals {
		s.expireLocked(p)
		if p.state != StatePending && now.Sub(p.ended) >= keep {
			delete(s.proposals, id)
		}
	}
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
