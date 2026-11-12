package transport

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nearhost/nearhost/internal/oserr"
)

// perProcessForward is one desired forward of a per-process transport.
// Its fields other than local and remote are guarded by the transport's mu.
type perProcessForward struct {
	local, remote int
	// proc is the ssh carrying the forward, nil when none.
	proc *sshProc
	// applied is whether proc has said it listens. It is reported only
	// while the transport is connected.
	applied bool
	// retrying is whether the forward is unapplied with its supervisor
	// relaunching it, err is then ssh's error from the last attempt, and
	// portRefused whether that attempt could not bind the local port.
	retrying    bool
	err         string
	portRefused bool
	// cancel ends the forward's supervisor, and done closes once it has
	// ended with its process reaped; kick asks it for an attempt now,
	// answered on the channel sent. All nil when there is none.
	cancel context.CancelFunc
	done   chan struct{}
	kick   chan chan error
}

// AddForward records a desired -L forward and, while connected, starts the
// ssh that carries it and returns its error. Forwards recorded while
// disconnected are started once the link is up. For a forward whose
// supervisor is retrying it, AddForward has it try at once and returns
// that attempt's error; the forward stays tracked and retrying either way.
func (p *perProcess) AddForward(ctx context.Context, localPort, remotePort int) error {
	p.fwdMu.Lock()
	defer p.fwdMu.Unlock()
	key := forwardKey(localPort, remotePort)
	p.mu.Lock()
	f, existed := p.forwards[key]
	linkCtx := p.linkCtx
	connected := p.status == StatusConnected && linkCtx != nil
	if existed && connected && f.done != nil && f.retrying {
		kick, done := f.kick, f.done
		p.mu.Unlock()
		return retryNow(ctx, kick, done)
	}
	if existed && (f.done != nil || !connected) {
		p.mu.Unlock()
		return nil // running on this link already, or started once it is up
	}
	if !existed {
		f = &perProcessForward{local: localPort, remote: remotePort}
		p.forwards[key] = f
	}
	p.mu.Unlock()
	if !connected {
		return nil
	}
	first := p.superviseLocked(linkCtx, f, false)
	var err error
	select {
	case err = <-first:
	case <-ctx.Done():
		select {
		case err = <-first:
		default:
			err = ctx.Err()
		}
	}
	if err == nil {
		return nil
	}
	p.unsupervise(f)
	if ctx.Err() == nil && linkCtx.Err() != nil {
		return nil // the link went down: the forward starts when it is back
	}
	p.mu.Lock()
	wasApplied := f.applied
	if existed {
		f.applied = false
	} else {
		delete(p.forwards, key)
	}
	p.mu.Unlock()
	if wasApplied && !existed { // a forward kept tracked is not cancelled
		p.Logf("%s: cancel %s", p.Name, forwardSpec(localPort, remotePort))
	}
	return err
}

// retryNow asks the supervisor whose kick and done these are for an
// attempt now and returns its error. A supervisor that ends first, as the
// link went down, retries the forward once it is back, so that is nil.
func retryNow(ctx context.Context, kick chan chan error, done chan struct{}) error {
	reply := make(chan error, 1)
	select {
	case kick <- reply:
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-reply:
		return err
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// CancelForward removes a forward and ends the ssh that carries it.
func (p *perProcess) CancelForward(ctx context.Context, localPort, remotePort int) error {
	p.fwdMu.Lock()
	defer p.fwdMu.Unlock()
	key := forwardKey(localPort, remotePort)
	p.mu.Lock()
	f, ok := p.forwards[key]
	delete(p.forwards, key)
	p.mu.Unlock()
	if !ok {
		return nil
	}
	p.unsupervise(f)
	p.mu.Lock()
	wasApplied := f.applied
	p.mu.Unlock()
	if wasApplied {
		p.Logf("%s: cancel %s", p.Name, forwardSpec(localPort, remotePort))
	}
	return nil
}

// Forwards lists desired forwards as (local, remote) pairs sorted by local port.
func (p *perProcess) Forwards() [][2]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([][2]int, 0, len(p.forwards))
	for _, f := range p.forwards {
		out = append(out, [2]int{f.local, f.remote})
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

// ForwardStates returns the connection status together with the desired
// forwards, sorted by local port, each with whether its ssh listens, or
// whether it is being retried and why, from one snapshot. As with Session,
// the status is connected only after the re-apply pass has tried every
// forward once, so a forward that is not applied then has failed, or has
// been lost, and is being retried.
func (p *perProcess) ForwardStates() (Status, []ForwardState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]ForwardState, 0, len(p.forwards))
	for _, f := range p.forwards {
		out = append(out, ForwardState{
			Local:       f.local,
			Remote:      f.remote,
			Applied:     p.status == StatusConnected && f.applied,
			Retrying:    f.retrying,
			Err:         f.err,
			PortRefused: f.portRefused,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Local < out[j].Local })
	return p.status, out
}

// sortedForwardsLocked returns the desired forwards sorted by local port.
// The caller holds mu.
func (p *perProcess) sortedForwardsLocked() []*perProcessForward {
	out := make([]*perProcessForward, 0, len(p.forwards))
	for _, f := range p.forwards {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].local < out[j].local })
	return out
}

// markConnected starts every desired forward on the link whose context is
// linkCtx, at most maxForwardLaunches at a time, waits for each first
// attempt to listen or fail, then reports the transport connected. It
// holds fwdMu throughout, so AddForward and CancelForward run wholly before
// or after it, and whoever sees the connected status sees every forward
// settled. A forward that fails is logged and stays tracked but unapplied,
// while its supervisor goes on trying with its own backoff. It
// reports false, leaving the status alone, when the link went down or the
// transport is stopping during the pass.
func (p *perProcess) markConnected(linkCtx context.Context) bool {
	p.fwdMu.Lock()
	defer p.fwdMu.Unlock()
	p.mu.Lock()
	p.linkCtx = linkCtx
	todo := p.sortedForwardsLocked()
	p.mu.Unlock()
	firsts := make([]<-chan error, len(todo))
	for i, f := range todo {
		firsts[i] = p.superviseLocked(linkCtx, f, true)
	}
	for i := range todo {
		if err := <-firsts[i]; err != nil {
			if linkCtx.Err() == nil {
				p.Logf("%s: reapply failed: %v", p.Name, err)
			}
		}
	}
	if linkCtx.Err() != nil {
		return false
	}
	p.setStatus(StatusConnected, "")
	return true
}

// endForwards ends every forward's ssh, once the link has gone, and marks
// the forwards unapplied; they stay desired, to start with the next link.
func (p *perProcess) endForwards() {
	p.fwdMu.Lock()
	defer p.fwdMu.Unlock()
	p.mu.Lock()
	p.linkCtx = nil
	all := p.sortedForwardsLocked()
	p.mu.Unlock()
	for _, f := range all {
		p.unsupervise(f)
	}
	changed := false
	p.mu.Lock()
	for _, f := range all {
		if f.applied {
			f.applied = false
			changed = true
		}
	}
	p.mu.Unlock()
	if changed {
		p.Logf("%s: forwards closed with the link", p.Name)
	}
}

// superviseLocked starts f's supervisor on the link whose context is
// linkCtx and returns the channel that gets its first launch's result. A
// supervisor that keepTrying goes on after a first launch that fails. The
// caller holds fwdMu.
func (p *perProcess) superviseLocked(linkCtx context.Context, f *perProcessForward, keepTrying bool) <-chan error {
	ctx, cancel := context.WithCancel(linkCtx)
	first := make(chan error, 1)
	done := make(chan struct{})
	kick := make(chan chan error)
	p.mu.Lock()
	f.cancel, f.done, f.kick = cancel, done, kick
	p.mu.Unlock()
	go p.superviseForward(ctx, f, kick, first, done, keepTrying)
	return first
}

// unsupervise ends f's supervisor, if it has one, waits until its process
// is reaped, and forgets it. It leaves applied alone.
func (p *perProcess) unsupervise(f *perProcessForward) {
	p.mu.Lock()
	cancel, done := f.cancel, f.done
	f.cancel, f.done, f.kick = nil, nil, nil
	p.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

// superviseForward runs f's ssh until ctx ends. The first launch's result
// goes to first; a first launch that fails ends the supervisor unless
// keepTrying. Otherwise a launch that fails, or a process that exits, is
// relaunched after the forward's own backoff, for as long as ctx lasts,
// with the forward marked retrying meanwhile. A channel sent on kick asks
// for an attempt now and gets its result. A failed retry, asked for or
// not, is logged when its message differs from the last failure reported.
func (p *perProcess) superviseForward(ctx context.Context, f *perProcessForward, kick <-chan chan error, first chan<- error, done chan struct{}, keepTrying bool) {
	defer func() {
		p.mu.Lock()
		f.retrying, f.err, f.portRefused = false, "", false
		p.mu.Unlock()
		close(done)
	}()
	spec := forwardSpec(f.local, f.remote)
	backoff := p.baseBackoff
	// reply is the AddForward waiting for this attempt's result, nil for
	// none; logged is the failure last reported.
	var reply chan error
	logged := ""
	for {
		proc, err := p.launchForward(ctx, f.local, f.remote)
		if err != nil {
			if ctx.Err() != nil {
				if first != nil {
					first <- err
				}
				return
			}
			if first != nil && !keepTrying {
				first <- err // the caller says why
				return
			}
			var refused *portRefusedError
			p.mu.Lock()
			f.retrying, f.err, f.portRefused = true, err.Error(), errors.As(err, &refused)
			p.mu.Unlock()
			// The caller of the first attempt says why. Any other failure
			// is logged unless it fails as the last one reported did, and
			// is handed to the AddForward that asked for it too, since
			// only a log line has the engine look at the forward again.
			if first != nil {
				first <- err
				first = nil
			} else if err.Error() != logged {
				p.Logf("%s: forward %s failed: %v", p.Name, spec, err)
			}
			if reply != nil {
				reply <- err
				reply = nil
			}
			logged = err.Error()
			if reply = p.pause(ctx, &backoff, kick); ctx.Err() != nil {
				return
			}
			continue
		}
		backoff, logged = p.baseBackoff, ""
		p.mu.Lock()
		f.proc, f.applied, f.retrying, f.err, f.portRefused = proc, true, false, "", false
		p.mu.Unlock()
		p.Logf("%s: forward %s", p.Name, spec)
		if first != nil {
			first <- nil
			first = nil
		}
		if reply != nil {
			reply <- nil
			reply = nil
		}
	running:
		for {
			select {
			case <-ctx.Done():
				break running
			case <-proc.exited:
				break running
			case r := <-kick: // asked just as it got applied
				r <- nil
			}
		}
		if ctx.Err() != nil {
			proc.stop()
			p.mu.Lock()
			f.proc = nil
			p.mu.Unlock()
			return
		}
		msg := exitMessage(proc.stderr.Lines())
		p.mu.Lock()
		f.proc, f.applied, f.retrying, f.err, f.portRefused = nil, false, true, msg, false
		p.mu.Unlock()
		p.Logf("%s: forward %s lost: %s", p.Name, spec, msg)
		if reply = p.pause(ctx, &backoff, kick); ctx.Err() != nil {
			return
		}
	}
}

// pause waits out *backoff, then lengthens it, or until a channel sent on
// kick asks for an attempt now, which it returns with *backoff left as it
// is; or until ctx ends.
func (p *perProcess) pause(ctx context.Context, backoff *time.Duration, kick <-chan chan error) chan error {
	t := time.NewTimer(*backoff)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return nil
	case <-t.C:
		*backoff = p.nextBackoff(*backoff)
		return nil
	case r := <-kick:
		return r
	}
}

// maxForwardLaunches is how many forwards may be logging in at once. Each
// is a whole ssh login, and sshd by default (MaxStartups 10:30:100) starts
// dropping unauthenticated connections beyond 10.
const maxForwardLaunches = 4

// portRefusedError is a forward's launch failing because its local port
// could not be bound: the check before ssh starts could not listen there,
// or ssh said it could not.
type portRefusedError struct{ err error }

func (e *portRefusedError) Error() string { return e.err.Error() }
func (e *portRefusedError) Unwrap() error { return e.err }

// refusesPort reports whether msg, why a forward's ssh exited, is ssh
// saying it could not bind the local port.
func refusesPort(msg string) bool {
	return strings.Contains(msg, "Could not request local forwarding") || strings.Contains(msg, "bind [")
}

// launchForward starts an `ssh -N -L` for the forward from local to remote
// and waits until it listens, holding one of the launch slots while it
// does. It fails when the port is taken, when ssh exits first, after
// connectTimeout, or when ctx ends; the process is then reaped. A failure
// to bind the local port is a *portRefusedError.
func (p *perProcess) launchForward(ctx context.Context, local, remote int) (*sshProc, error) {
	select {
	case p.launchSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-p.launchSlots }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l, err := p.listen("tcp", fmt.Sprintf("127.0.0.1:%d", local))
	if oserr.AddrInUse(err) {
		return nil, &portRefusedError{fmt.Errorf("bind [127.0.0.1]:%d: Address already in use", local)}
	}
	if err != nil {
		return nil, &portRefusedError{fmt.Errorf("bind [127.0.0.1]:%d: %w", local, err)}
	}
	l.Close()
	spec := forwardSpec(local, remote)
	stderr := newForwardStderr(local)
	proc, err := p.startProc(p.args("-N", "-L", spec), stderr)
	if err != nil {
		return nil, fmt.Errorf("start ssh: %w", err)
	}
	timeout := time.NewTimer(p.connectTimeout)
	defer timeout.Stop()
	select {
	case <-stderr.ready:
		return proc, nil
	case <-proc.exited:
		msg := exitMessage(stderr.Lines())
		err := fmt.Errorf("ssh -L %s: %s", spec, msg)
		if refusesPort(msg) {
			return nil, &portRefusedError{err}
		}
		return nil, err
	case <-timeout.C:
		proc.stop()
		return nil, fmt.Errorf("ssh -L %s: timed out after %s", spec, p.connectTimeout)
	case <-ctx.Done():
		proc.stop()
		return nil, ctx.Err()
	}
}
