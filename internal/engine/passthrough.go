package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
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
	// is honoured and nothing is left forwarded. When that undo cannot
	// cancel the forward, closing stays set on a record that is no longer
	// opening, and the next check closes it.
	closing bool
	// checked is when a check last found the server connected, or when the
	// passthrough opened.
	checked time.Time
	// awaySince is when a check first found the server away, and zero
	// while it is connected.
	awaySince time.Time
	// failingSince is when the port check began to fail, moved on by any
	// time away, and zero while the port answers.
	failingSince time.Time
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
		// caller's cancellation cannot cut short, bounded like any other
		// close run on the engine's behalf.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), passCloseWait)
		err := c.sess.CancelForward(cctx, local, spec.Remote)
		cancel()
		if err != nil {
			err = fmt.Errorf("passthrough %q was closed while it opened, and cancelling port %d on %s failed: %w", spec.ID, spec.Remote, spec.Server, err)
			e.mu.Lock()
			if e.closed {
				// Shutdown may have let go of the passthroughs already, and
				// the check loop has ended, so nothing would close this one.
				// Its session is gone or going, and the forward with it.
				e.mu.Unlock()
				e.releasePort(local)
				forget()
				return Passthrough{}, err
			}
			// The session keeps a forward it could not cancel, and puts it
			// back on a reconnect, so the record and port stay. The record
			// is listed now, and the next check closes it again.
			p.opening = false
			p.Opened = e.now()
			p.checked = p.Opened
			e.mu.Unlock()
			return Passthrough{}, err
		}
		e.releasePort(local)
		forget()
		return Passthrough{}, fmt.Errorf("passthrough %q was closed while it opened", spec.ID)
	}
	p.opening = false
	p.Opened = e.now()
	p.checked = p.Opened
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

// When a passthrough ends on its own. The port is checked every
// passCheckEvery; a passthrough closes once nothing has answered on it for
// passFailFor of checks while its server was connected, or once its server
// has been away for passGoneFor.
const (
	passCheckEvery = 30 * time.Second
	passFailFor    = 5 * time.Minute
	passGoneFor    = 10 * time.Minute
)

// passProbeWait bounds each step of the default port check: the dial, and
// the wait for the first byte.
const passProbeWait = 2 * time.Second

// passCloseWait bounds a close run in the background, so Shutdown is not
// held up by an ssh that does not answer.
const passCloseWait = 10 * time.Second

// probePassthroughPort is the default port check. It dials the local end of
// a passthrough and waits for one byte. Data, or silence until the deadline,
// means something answered: a dev server may say nothing until it is asked.
// The connection closing first means nothing did, because ssh -L accepts the
// local connection and closes it when the remote port refuses. A local port
// that refuses is not answering either.
func probePassthroughPort(ctx context.Context, local int) bool {
	d := net.Dialer{Timeout: passProbeWait}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(local)))
	if err != nil {
		return false
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetReadDeadline(time.Now().Add(passProbeWait))
	_, err = conn.Read(make([]byte, 1))
	switch {
	case err == nil, errors.Is(err, os.ErrDeadlineExceeded):
		return true
	case ctx.Err() != nil:
		return true // the check was cut short, which says nothing of the port
	case errors.Is(err, io.EOF):
		return false
	}
	return false // reset, or another failure before the deadline
}

// passClose is a passthrough to close and why.
type passClose struct {
	id, reason string
}

// checkPassthroughs checks every open passthrough once, at e.now. Those of a
// server that is away count the time; one away for passGoneFor closes. On a
// connected server, a passthrough whose forward the session could not put
// back after a reconnect closes, and the others have their port checked;
// one that has not answered for passFailFor of connected time closes. A
// server that failed for good, or whose session is gone, closes its
// passthroughs at once, as does a close that its open could not finish. The
// loop runs this every passCheckEvery.
func (e *Engine) checkPassthroughs(ctx context.Context) {
	now := e.now()
	var closes []passClose
	var probes []*passthrough
	e.mu.Lock()
	ids := make([]string, 0, len(e.passthroughs))
	for id := range e.passthroughs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		p := e.passthroughs[id]
		if p.opening {
			continue
		}
		if p.closing {
			// A close arrived while it opened and the undo could not cancel
			// the forward, so the close is tried again.
			closes = append(closes, passClose{id, "it was closed while it opened"})
			continue
		}
		if c := e.servers[p.Server]; c == nil || c.sess != p.sess {
			// dropServer closes these as it drops the session. One whose
			// close failed is closed again here.
			closes = append(closes, passClose{id, e.droppedReasonLocked(p.Server)})
			continue
		}
		status, states := p.sess.ForwardStates()
		switch status {
		case transport.StatusConnected:
			if !p.awaySince.IsZero() {
				// Time away does not count against the port: neither does the
				// interval before the first check that found the server away,
				// so the shift runs from the last check that found it
				// connected.
				if !p.failingSince.IsZero() {
					p.failingSince = p.failingSince.Add(now.Sub(p.checked))
				}
				p.awaySince = time.Time{}
			}
			p.checked = now
			if !forwardApplied(states, p.Local, p.Remote) {
				closes = append(closes, passClose{id, fmt.Sprintf("laptop port %d was taken while %s was away", p.Local, p.Server)})
				continue
			}
			probes = append(probes, p)
		case transport.StatusAuthFailed:
			closes = append(closes, passClose{id, failedReason(p.Server, p.sess)})
		default:
			if p.awaySince.IsZero() {
				p.awaySince = now
			}
			if now.Sub(p.awaySince) >= passGoneFor {
				closes = append(closes, passClose{id, fmt.Sprintf("%s was disconnected for 10 minutes", p.Server)})
			}
		}
	}
	e.mu.Unlock()
	e.closePassthroughs(ctx, closes)

	for _, p := range probes {
		if ctx.Err() != nil {
			return
		}
		answered := e.probePass(ctx, p.Local)
		if ctx.Err() != nil {
			return
		}
		e.mu.Lock()
		if e.passthroughs[p.ID] != p {
			e.mu.Unlock()
			continue // closed meanwhile
		}
		switch {
		case answered:
			p.failingSince = time.Time{}
		case p.failingSince.IsZero():
			p.failingSince = now
		case now.Sub(p.failingSince) >= passFailFor:
			closes = []passClose{{p.ID, fmt.Sprintf("nothing answered on port %d for 5 minutes", p.Remote)}}
		}
		e.mu.Unlock()
		if len(closes) > 0 {
			e.closePassthroughs(ctx, closes)
			closes = nil
		}
	}
}

// forwardApplied reports whether states has the forward local:remote,
// applied on the current master.
func forwardApplied(states []transport.ForwardState, local, remote int) bool {
	for _, f := range states {
		if f.Local == local && f.Remote == remote {
			return f.Applied
		}
	}
	return false
}

// failedReason is why the passthroughs of a server whose connection failed
// for good are closed.
func failedReason(server string, sess *transport.Session) string {
	return fmt.Sprintf("%s's connection failed: %s", server, sess.LastError())
}

// droppedReasonLocked is why the passthroughs of a server whose session was
// dropped are closed: the server left the configuration, or its session was
// replaced. It must be called with e.mu held.
func (e *Engine) droppedReasonLocked(server string) string {
	if _, ok := e.cfg.Server(server); !ok {
		return fmt.Sprintf("%s was removed from Nearhost", server)
	}
	return fmt.Sprintf("%s was disconnected", server)
}

// closePassthroughs closes each passthrough in closes. One whose close fails
// stays open, and a later check tries again.
func (e *Engine) closePassthroughs(ctx context.Context, closes []passClose) {
	for _, c := range closes {
		_ = e.ClosePassthrough(ctx, c.id, c.reason)
	}
}

// sessionPassthroughsLocked lists the passthroughs carried by sess, opening
// ones included, each with reason. It must be called with e.mu held.
func (e *Engine) sessionPassthroughsLocked(sess *transport.Session, reason string) []passClose {
	var out []passClose
	for id, p := range e.passthroughs {
		if p.sess == sess {
			out = append(out, passClose{id, reason})
		}
	}
	slices.SortFunc(out, func(a, b passClose) int { return strings.Compare(a.id, b.id) })
	return out
}

// closeLaterLocked closes the passthroughs in closes on a goroutine of its
// own, counted in passWork, so a caller that must not block (serverLog,
// which may run under the session's forward lock) does not wait for the
// session. After Shutdown has begun it does nothing: Shutdown releases every
// passthrough itself. It must be called with e.mu held.
func (e *Engine) closeLaterLocked(closes []passClose) {
	if len(closes) == 0 || e.closed {
		return
	}
	e.passWork.Add(1) // under mu and before closed is set, so before Shutdown waits
	go func() {
		defer e.passWork.Done()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(e.ctx), passCloseWait)
		defer cancel()
		e.closePassthroughs(ctx, closes)
	}()
}

// closeOnFailureLocked closes the passthroughs of server once its session
// has failed for good, as serverLog finds it. It must be called with e.mu
// held.
func (e *Engine) closeOnFailureLocked(server string) {
	c := e.servers[server]
	if c == nil || c.sess.Status() != transport.StatusAuthFailed {
		return
	}
	e.closeLaterLocked(e.sessionPassthroughsLocked(c.sess, failedReason(server, c.sess)))
}

// watchPassthroughs runs checkPassthroughs every e.passEvery, by default
// passCheckEvery, until ctx ends. It is counted in passWork.
func (e *Engine) watchPassthroughs(ctx context.Context) {
	defer e.passWork.Done()
	t := time.NewTicker(e.passEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.checkPassthroughs(ctx)
		}
	}
}

// releasePassthroughs lets go of every passthrough at Shutdown, once the
// sessions that carried them have ended: their ports and relays are freed
// and their records dropped. One still opening is told to undo itself.
func (e *Engine) releasePassthroughs() {
	e.mu.Lock()
	var ports []int
	for id, p := range e.passthroughs {
		if p.opening {
			p.closing = true
			continue
		}
		delete(e.passthroughs, id)
		ports = append(ports, p.Local)
	}
	e.mu.Unlock()
	for _, port := range ports {
		e.releasePort(port)
	}
}
