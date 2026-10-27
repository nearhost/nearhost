package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/transport"
)

// ErrNotConnected is returned, wrapped, when a passthrough is asked for on a
// server whose session is not connected. Nothing starts a session for it.
var ErrNotConnected = errors.New("server not connected")

// PassthroughSpec describes a passthrough to open: one port of a server,
// forwarded to this machine for a while on behalf of a coding agent.
type PassthroughSpec struct {
	ID        string // unique among open passthroughs
	Server    string // the configured server whose port is forwarded
	Requester string // the tool that asked, such as claude-code
	Session   string // the requester's session, which may be empty
	Label     string // what the port serves, as the requester described it
	Remote    int    // the port on the server
}

// Passthrough is an open passthrough.
type Passthrough struct {
	PassthroughSpec
	Local  int       // the port on this machine that reaches Remote
	Opened time.Time // when it was opened
}

// passthrough is a passthrough with the session that carries it. It is
// recorded as soon as its id is accepted, so its id and port are held while
// it opens. Its fields other than Passthrough are guarded by Engine.mu.
type passthrough struct {
	Passthrough
	sess *transport.Session
	// opening is true until the forward is in place. Passthroughs does not
	// list the passthrough until then.
	opening bool
	// closing is set by a close that arrives while the passthrough is
	// opening. The open then undoes itself and fails, so the caller's close
	// is honoured and nothing is left forwarded.
	closing bool
}

// passthroughOwnerPrefix starts the owner of a port held by a passthrough. It
// begins with a NUL byte, which no project name can contain.
const passthroughOwnerPrefix = "\x00passthrough:"

// passthroughOwner is the owner recorded in Engine.ports for the passthrough
// with the given id.
func passthroughOwner(id string) string {
	return passthroughOwnerPrefix + id
}

// ownerTextLocked describes the owner of a local port for a message: the
// project's name, or the requester, server and session of a passthrough. It
// must be called with e.mu held.
func (e *Engine) ownerTextLocked(owner string) string {
	id, ok := strings.CutPrefix(owner, passthroughOwnerPrefix)
	if !ok {
		return fmt.Sprintf("project %q", owner)
	}
	p := e.passthroughs[id]
	if p == nil {
		return "a passthrough"
	}
	text := fmt.Sprintf("a passthrough for %s on %s", p.Requester, p.Server)
	if p.Session != "" {
		text += fmt.Sprintf(" (session %s)", p.Session[:min(len(p.Session), 8)])
	}
	return text
}

// OpenPassthrough forwards spec.Remote on spec.Server to this machine. It
// uses the same port here when that is free and another one when not. It
// refuses unless the server is connected now, and never starts a session.
func (e *Engine) OpenPassthrough(ctx context.Context, spec PassthroughSpec) (Passthrough, error) {
	if spec.Server == config.LocalServer {
		return Passthrough{}, errors.New("a passthrough needs a server; this machine is already local")
	}
	e.mu.Lock()
	c := e.servers[spec.Server]
	if c == nil || c.sess.Status() != transport.StatusConnected {
		e.mu.Unlock()
		return Passthrough{}, fmt.Errorf("%w: Nearhost isn't connected to %s", ErrNotConnected, spec.Server)
	}
	if _, dup := e.passthroughs[spec.ID]; dup {
		e.mu.Unlock()
		return Passthrough{}, fmt.Errorf("passthrough %q is already open", spec.ID)
	}
	p := &passthrough{Passthrough: Passthrough{PassthroughSpec: spec}, sess: c.sess, opening: true}
	e.passthroughs[spec.ID] = p
	e.mu.Unlock()
	// forget drops the record of a passthrough that did not open.
	forget := func() {
		e.mu.Lock()
		delete(e.passthroughs, spec.ID)
		e.mu.Unlock()
	}
	local, err := e.claimPassthroughPort(ctx, passthroughOwner(spec.ID), spec.Remote)
	if err != nil {
		forget()
		return Passthrough{}, err
	}
	e.mu.Lock()
	p.Local = local
	e.mu.Unlock()
	if e.beforeForward != nil {
		e.beforeForward(spec.ID)
	}
	if err := c.sess.AddForward(ctx, local, spec.Remote); err != nil {
		e.releasePort(local)
		forget()
		return Passthrough{}, fmt.Errorf("forwarding port %d on %s: %w", spec.Remote, spec.Server, err)
	}
	e.mu.Lock()
	if p.closing {
		e.mu.Unlock()
		// The forward was just made, so the cancel uses a context that the
		// caller's cancellation cannot cut short.
		_ = c.sess.CancelForward(context.WithoutCancel(ctx), local, spec.Remote)
		e.releasePort(local)
		forget()
		return Passthrough{}, fmt.Errorf("passthrough %q was closed while it opened", spec.ID)
	}
	p.opening = false
	p.Opened = e.now()
	got := p.Passthrough
	e.mu.Unlock()
	e.bus.Publish(Event{Server: spec.Server, Message: fmt.Sprintf("opened port %d for %s on laptop port %d", spec.Remote, spec.Requester, local)})
	return got, nil
}

// claimPassthroughPort claims a local port for owner and starts its [::1]
// relay. It prefers the port the server uses, then tries free ports.
func (e *Engine) claimPassthroughPort(ctx context.Context, owner string, remote int) (int, error) {
	if e.tryLocalPort(owner, remote) == nil {
		return remote, nil
	}
	var last error
	for attempt := 0; attempt < webPortAttempts && ctx.Err() == nil; attempt++ {
		local, err := transport.FreePort()
		if err != nil {
			return 0, err
		}
		if last = e.tryLocalPort(owner, local); last == nil {
			return local, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("no free port on this machine: %w", last)
}

// tryLocalPort claims port for owner and checks it can be served on both
// loopback addresses, as forwardLoopback does. It releases the port if not.
func (e *Engine) tryLocalPort(owner string, port int) error {
	if holder, ok := e.claimPortFor(owner, port); !ok {
		return fmt.Errorf("local port %d is held by %s", port, holder)
	}
	l, err := e.listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		e.releasePort(port)
		return err
	}
	if err := l.Close(); err != nil {
		e.releasePort(port)
		return err
	}
	switch r, err := relayIPv6(port); {
	case err == nil:
		e.mu.Lock()
		e.relays[port] = r
		e.mu.Unlock()
	case errors.Is(err, syscall.EADDRNOTAVAIL) || errors.Is(err, syscall.EAFNOSUPPORT):
		// A machine without the IPv6 loopback has only 127.0.0.1 to serve.
	default:
		e.releasePort(port)
		return err
	}
	return nil
}

// ClosePassthrough cancels the passthrough with the given id and frees its
// port, telling the user why. An id that is not open is not an error. A
// passthrough still opening is marked, and its open then fails and undoes
// itself; the reason is not published, because nothing was announced.
func (e *Engine) ClosePassthrough(ctx context.Context, id, reason string) error {
	e.mu.Lock()
	p := e.passthroughs[id]
	if p == nil {
		e.mu.Unlock()
		return nil
	}
	if p.opening {
		p.closing = true
		e.mu.Unlock()
		return nil
	}
	// Taking the record out first means a second close finds nothing, so a
	// port is released once.
	delete(e.passthroughs, id)
	e.mu.Unlock()
	if err := p.sess.CancelForward(ctx, p.Local, p.Remote); err != nil {
		e.mu.Lock()
		if _, taken := e.passthroughs[id]; !taken {
			e.passthroughs[id] = p // so the close can be retried
		}
		e.mu.Unlock()
		return fmt.Errorf("cancelling port %d on %s: %w", p.Remote, p.Server, err)
	}
	e.releasePort(p.Local)
	e.bus.Publish(Event{Server: p.Server, Message: fmt.Sprintf("closed port %d for %s: %s", p.Remote, p.Requester, reason)})
	return nil
}

// Passthroughs lists the open passthroughs of server, or of every server when
// it is empty, ordered by the server's port.
func (e *Engine) Passthroughs(server string) []Passthrough {
	e.mu.Lock()
	var out []Passthrough
	for _, p := range e.passthroughs {
		if !p.opening && (server == "" || p.Server == server) {
			out = append(out, p.Passthrough)
		}
	}
	e.mu.Unlock()
	slices.SortFunc(out, func(a, b Passthrough) int {
		if a.Remote != b.Remote {
			return a.Remote - b.Remote
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// PortFree reports whether no forward holds the local port and 127.0.0.1 can
// bind it now.
func (e *Engine) PortFree(port int) bool {
	e.mu.Lock()
	_, held := e.ports[port]
	e.mu.Unlock()
	if held {
		return false
	}
	l, err := e.listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}
