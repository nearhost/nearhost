package agentapi

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Passthrough is one of a server's ports forwarded to the laptop for a
// coding agent, until its session ends or the port goes away.
type Passthrough struct {
	ID     string
	Origin string // the server's name
	// Requester names the tool that asked, such as claude-code. It is what
	// Always allow is given to, not proof of who asked.
	Requester string
	Session   string // groups passthroughs for closing; may be empty
	Label     string // what it is for, for the prompt and the list
	Remote    int    // the port on the server
	Local     int    // the port on the laptop
	Opened    time.Time
}

// Passer opens and closes the forwards behind passthroughs. The engine
// answers it, adapted by the caller, so agentapi needs neither.
type Passer interface {
	// Open forwards p.Remote on p.Origin to the laptop and returns the
	// laptop port. It returns an error wrapping ErrNotConnected when the
	// server isn't connected.
	Open(ctx context.Context, p Passthrough) (local int, err error)
	// Close closes the passthrough with the given ID.
	Close(ctx context.Context, id string) error
	// List is origin's open passthroughs. It must not block on the network.
	List(origin string) []Passthrough
	// Free reports whether port is free on the laptop.
	Free(port int) bool
}

// Grants keeps the passthrough grants given with Always allow, one per
// requester on a server.
type Grants interface {
	// Allow gives requester a grant on server, replacing any it had, and
	// returns its new token.
	Allow(server, requester string) (string, error)
	// Check reports whether token is requester's on server.
	Check(server, requester, token string) (bool, error)
}

// PassRequest is a passthrough waiting for the user's answer.
type PassRequest struct {
	ID        string
	Origin    string
	Requester string
	Session   string
	Label     string
	Remote    int
	// LocalTaken is set when the laptop port of the same number is taken,
	// so another one will be used.
	LocalTaken bool
	// CanAlways is set when Always allow may be offered, which it never is
	// to RequesterUnknown.
	CanAlways bool
	// Expires is when the request stops waiting for an answer.
	Expires time.Time
}

// Answer is the user's answer to a PassRequest.
type Answer int

const (
	// AnswerNo declines the request.
	AnswerNo Answer = iota
	// AnswerYes opens the passthrough once.
	AnswerYes
	// AnswerAlways opens it and lets its requester on its server open more
	// without asking, with the token it is given.
	AnswerAlways
)

// RequesterUnknown is the requester a tool that can't tell what it is
// sends. It is never offered Always allow.
const RequesterUnknown = "agent"

const (
	// StateOpen is a passthrough that is open.
	StateOpen State = "open"
	// StateClosed is a passthrough that was open, or a request whose
	// session was closed while it waited.
	StateClosed State = "closed"
)

// ErrNotConnected is wrapped by Passer.Open's error when the server isn't
// connected.
var ErrNotConnected = errors.New("the server isn't connected")

// ErrAllowNotSaved is wrapped by AnswerPassthrough's error when the
// passthrough opened, or was closed again with its session, but saving the
// Always allow grant failed.
var ErrAllowNotSaved = errors.New("always allow was not saved")

// ErrPassEnded is wrapped by AnswerPassthrough's error when the request is
// no longer waiting for an answer: it expired, its session ended, it was
// answered already, or opening it failed, which ends it declined.
var ErrPassEnded = errors.New("the request has ended")

// passEnded is an error that ended the request it answered, or found it
// ended. Its message is the error's own; it also wraps ErrPassEnded.
type passEnded struct{ error }

// Unwrap is the error and ErrPassEnded.
func (e passEnded) Unwrap() []error { return []error{e.error, ErrPassEnded} }

// passRecord is a passthrough request and what became of it. done is
// closed when it leaves StatePending.
type passRecord struct {
	PassRequest
	// prompted is set for a request shown to the reviewer; a request with
	// a valid token opens without one and does not count against
	// maxPending.
	prompted bool
	// opening is set while Passer.Open runs for it. It does not expire
	// meanwhile, and can't be answered again.
	opening bool
	state   State
	reason  string
	local   int
	// token is the Always allow token, until the agent has read it.
	token string
	// gen orders opens against Passer.List snapshots: see sync.
	gen   uint64
	seq   uint64 // creation order, for lists
	ended time.Time
	done  chan struct{}
}

// passthrough is the record as a Passthrough.
func (r *passRecord) passthrough(opened time.Time) Passthrough {
	return Passthrough{
		ID: r.ID, Origin: r.Origin, Requester: r.Requester, Session: r.Session, Label: r.Label,
		Remote: r.Remote, Local: r.local, Opened: opened,
	}
}

// AnswerPassthrough answers the request id with the user's answer. On Yes
// and Always allow it opens the passthrough, blocking while it does, and
// returns Open's error, which also ends the request declined with it as the
// reason. It errors for Yes or Always allow on an id that is unknown or has
// ended, and for Always allow on a request that may not have it. No on an
// unknown id does nothing and returns nil, as the request is over either
// way; No on a request that has ended returns the same error Yes would, so
// the caller can say the answer came too late. When the grant for Always
// allow fails to save, the error wraps ErrAllowNotSaved. Every error for a
// request that is no longer waiting wraps ErrPassEnded.
func (s *Server) AnswerPassthrough(ctx context.Context, id string, a Answer) error {
	if s.opts.Passer == nil || s.opts.Grants == nil {
		return errors.New("this nearhost has no passthroughs")
	}
	s.mu.Lock()
	s.sweepPassesLocked()
	r := s.passes[id]
	switch {
	case a == AnswerNo && (r == nil || !r.prompted):
		s.mu.Unlock()
		return nil
	case r == nil || !r.prompted:
		s.mu.Unlock()
		return passEnded{fmt.Errorf("no passthrough request %s; it may have ended over 10 minutes ago", id)}
	case r.opening:
		s.mu.Unlock()
		return fmt.Errorf("the request for port %d is already being answered", r.Remote)
	case r.state != StatePending:
		state := r.state
		s.mu.Unlock()
		return passEnded{fmt.Errorf("the request for port %d has ended: %s", r.Remote, state)}
	case a == AnswerAlways && !r.CanAlways:
		s.mu.Unlock()
		return fmt.Errorf("%s can't be always allowed, as it didn't say which tool it is", r.Requester)
	case a == AnswerNo:
		s.endPassLocked(r, StateDeclined, "")
		s.mu.Unlock()
		return nil
	}
	r.opening = true
	p := r.passthrough(s.opts.Now())
	s.mu.Unlock()

	local, err := s.opts.Passer.Open(ctx, p)
	s.mu.Lock()
	if err != nil {
		r.opening = false
		s.endPassLocked(r, StateDeclined, err.Error())
		s.mu.Unlock()
		return passEnded{err}
	}
	if r.state != StatePending {
		// Its session was closed while it opened: close the forward, and
		// give no grant, which would replace the one the agent holds.
		r.opening = false
		s.mu.Unlock()
		return passEnded{closedWhileOpening(r, s.closeOpened(ctx, r))}
	}
	s.mu.Unlock()

	var token string
	var allowErr error
	if a == AnswerAlways {
		token, allowErr = s.opts.Grants.Allow(r.Origin, r.Requester)
	}

	s.mu.Lock()
	r.opening = false
	// A token is given even to a request its session closed meanwhile, as
	// the grant it belongs to is saved.
	r.token = token
	if r.state != StatePending {
		s.mu.Unlock()
		err := closedWhileOpening(r, s.closeOpened(ctx, r))
		if allowErr != nil {
			err = errors.Join(err, allowNotSaved(allowErr))
		}
		return passEnded{err}
	}
	s.openPassLocked(r, local, token)
	s.mu.Unlock()
	if allowErr != nil {
		return fmt.Errorf("port %d is open, but %w", r.Remote, allowNotSaved(allowErr))
	}
	return nil
}

// closeTimeout bounds closing one forward. Closes run detached from the
// caller's context: an agent's session-end hook hangs up within about 1.5
// seconds, and a close it cancelled would leave the forward open with
// nothing left to close it. Each close gets its own time, so one that hangs
// does not leave the rest with none. It is a variable so tests can shorten it.
var closeTimeout = 10 * time.Second

// openTimeout bounds opening a forward for a request with a token, which
// runs detached from the agent's hang-up so a forward is never left half
// applied; the agent finds the result by asking again.
const openTimeout = 45 * time.Second

// detached is ctx's values without its cancellation, bounded by d.
func detached(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), d)
}

// allowNotSaved is err, the reason saving a grant failed, as an error that
// wraps ErrAllowNotSaved.
func allowNotSaved(err error) error {
	return fmt.Errorf("%w: %w", ErrAllowNotSaved, err)
}

// closeOpened closes the forward opened for r after its session was
// closed. It returns Close's error, or nil.
func (s *Server) closeOpened(ctx context.Context, r *passRecord) error {
	ctx, cancel := detached(ctx, closeTimeout)
	defer cancel()
	return s.opts.Passer.Close(ctx, r.ID)
}

// closedWhileOpening is why a request whose session was closed while its
// forward opened did not open, given closeErr from closing that forward.
func closedWhileOpening(r *passRecord, closeErr error) error {
	if closeErr != nil {
		return fmt.Errorf("its session was closed while port %d opened, and closing it failed: %w", r.Remote, closeErr)
	}
	return fmt.Errorf("its session was closed while port %d opened", r.Remote)
}

// endPassLocked ends a pending request, unless it has already ended.
func (s *Server) endPassLocked(r *passRecord, state State, reason string) {
	if r.state != StatePending {
		return
	}
	r.state, r.reason, r.ended = state, reason, s.opts.Now()
	close(r.done)
}

// openPassLocked records a pending request as open on laptop port local.
func (s *Server) openPassLocked(r *passRecord, local int, token string) {
	if r.state != StatePending {
		return
	}
	s.passGen++
	r.state, r.local, r.token, r.gen = StateOpen, local, token, s.passGen
	close(r.done)
}

// closePassLocked records an open passthrough as closed.
func (s *Server) closePassLocked(r *passRecord) {
	if r.state == StateOpen {
		r.state, r.ended = StateClosed, s.opts.Now()
	}
}

// expirePassLocked ends a pending request whose time has passed, unless it
// is being opened.
func (s *Server) expirePassLocked(r *passRecord) {
	if r.state == StatePending && !r.opening && !s.opts.Now().Before(r.Expires) {
		r.state, r.ended = StateExpired, r.Expires
		close(r.done)
	}
}

// sweepPassesLocked expires requests and forgets those that ended long
// enough ago. Open ones are kept until they are seen closed.
func (s *Server) sweepPassesLocked() {
	now := s.opts.Now()
	for id, r := range s.passes {
		s.expirePassLocked(r)
		if r.state != StatePending && r.state != StateOpen && now.Sub(r.ended) >= keep {
			delete(s.passes, id)
		}
	}
}

// snapshot is origin's open passthroughs as the Passer has them, taken
// without s.mu held, and the open count before it was taken.
type snapshot struct {
	origin string
	gen    uint64
	live   map[string]bool
}

// live asks the Passer which of origin's passthroughs are open. The Passer
// closes them on its own, when the server or the port goes away, so the
// state of an open record is read from it.
func (s *Server) live(origin string) snapshot {
	s.mu.Lock()
	gen := s.passGen
	s.mu.Unlock()
	snap := snapshot{origin: origin, gen: gen, live: map[string]bool{}}
	for _, p := range s.opts.Passer.List(origin) {
		snap.live[p.ID] = true
	}
	return snap
}

// syncLocked records as closed origin's open passthroughs the Passer no
// longer lists. One opened after the snapshot was taken may not be in it
// yet, and is left alone.
func (s *Server) syncLocked(snap snapshot) {
	for _, r := range s.passes {
		if r.Origin == snap.origin && r.state == StateOpen && r.gen <= snap.gen && !snap.live[r.ID] {
			s.closePassLocked(r)
		}
	}
}
