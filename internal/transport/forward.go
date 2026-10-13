package transport

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
)

func forwardKey(local, remote int) string {
	return fmt.Sprintf("%d:%d", local, remote)
}

func forwardSpec(local, remote int) string {
	return fmt.Sprintf("127.0.0.1:%d:localhost:%d", local, remote)
}

// AddForward records a desired -L forward and applies it if connected.
// Forwards recorded while disconnected are applied on connect.
func (s *Session) AddForward(ctx context.Context, localPort, remotePort int) error {
	s.fwdMu.Lock()
	defer s.fwdMu.Unlock()
	key := forwardKey(localPort, remotePort)
	s.mu.Lock()
	prev, existed := s.forwards[key]
	isConnected := s.status == StatusConnected
	gen := s.launches
	if existed && isConnected && prev.appliedGen == gen {
		s.mu.Unlock()
		return nil // already active on this master; asking again would be rejected
	}
	s.forwards[key] = forward{local: localPort, remote: remotePort}
	s.mu.Unlock()
	if !isConnected {
		return nil
	}
	if err := s.controlForward(ctx, "forward", localPort, remotePort); err != nil {
		s.mu.Lock()
		if existed {
			s.forwards[key] = prev
		} else {
			delete(s.forwards, key)
		}
		s.mu.Unlock()
		return err
	}
	s.mu.Lock()
	if f, ok := s.forwards[key]; ok {
		f.appliedGen = gen
		s.forwards[key] = f
	}
	s.mu.Unlock()
	return nil
}

// CancelForward removes a forward, cancelling it on the master if connected.
// A forward that is active on the current master stays tracked until ssh
// confirms the cancel, so a failed cancel can be retried.
func (s *Session) CancelForward(ctx context.Context, localPort, remotePort int) error {
	s.fwdMu.Lock()
	defer s.fwdMu.Unlock()
	key := forwardKey(localPort, remotePort)
	s.mu.Lock()
	f, known := s.forwards[key]
	active := known && s.status == StatusConnected && f.appliedGen == s.launches
	if !active {
		delete(s.forwards, key)
	}
	s.mu.Unlock()
	if !active {
		return nil
	}
	if err := s.controlForward(ctx, "cancel", localPort, remotePort); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.forwards, key)
	s.mu.Unlock()
	return nil
}

// Forwards lists desired forwards as (local, remote) pairs sorted by local port.
func (s *Session) Forwards() [][2]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][2]int, 0, len(s.forwards))
	for _, f := range s.forwards {
		out = append(out, [2]int{f.local, f.remote})
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

// ForwardState is one desired forward and whether the current master has it.
type ForwardState struct {
	Local, Remote int
	// Applied is true when the session is connected and ssh confirmed this
	// forward on the master that is running now. A forward that is only
	// tracked (added while disconnected, or whose re-apply failed after a
	// reconnect) is not applied.
	Applied bool
}

// ForwardStates returns the connection status together with the desired
// forwards, sorted by local port, each with whether it is applied on the
// current master. Both come from one consistent snapshot. When the status is
// connected every forward is settled: the status only becomes connected after
// the re-apply pass, so a forward that is not applied then has failed, and
// stays that way until the next reconnect.
func (s *Session) ForwardStates() (Status, []ForwardState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ForwardState, 0, len(s.forwards))
	for _, f := range s.forwards {
		out = append(out, ForwardState{
			Local:   f.local,
			Remote:  f.remote,
			Applied: s.status == StatusConnected && f.appliedGen == s.launches,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Local < out[j].Local })
	return s.status, out
}

// controlForward runs one `ssh -O forward|cancel`. It holds no session lock.
func (s *Session) controlForward(ctx context.Context, op string, local, remote int) error {
	cmd := s.command(ctx, "-S", s.SocketPath(), "-O", op, "-L", forwardSpec(local, remote), s.Host)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("ssh -O %s %s: %s", op, forwardSpec(local, remote), msg)
	}
	s.Logf("%s: %s %s", s.Name, op, forwardSpec(local, remote))
	return nil
}

// markConnected re-applies the desired forwards on the new master and then
// reports the session connected. Both happen under fwdMu, so AddForward and
// CancelForward run either wholly before (and are covered by the re-apply) or
// wholly after (and talk to the master themselves), and whoever sees the
// connected status sees every forward settled.
//
// It reports false, and leaves the status alone, when the pass does not count:
// the session is being stopped, or the master is gone (it has exited, or a
// re-apply failed and it no longer answers on its socket). Failures of such a
// pass say nothing about the forwards, so they must not be seen with a
// connected status.
func (s *Session) markConnected(ctx context.Context, exited <-chan struct{}) bool {
	s.fwdMu.Lock()
	defer s.fwdMu.Unlock()
	failed := s.reapplyLocked(ctx)
	if ctx.Err() != nil || hasExited(exited) {
		return false
	}
	// A dying master is often noticed here first, a moment before its
	// process is reaped.
	if failed > 0 && s.command(ctx, "-S", s.SocketPath(), "-O", "check", s.Host).Run() != nil {
		return false
	}
	s.setStatus(StatusConnected, "")
	return true
}

// reapplyForwards adds desired forwards that the current master does not yet
// have.
func (s *Session) reapplyForwards(ctx context.Context) {
	s.fwdMu.Lock()
	defer s.fwdMu.Unlock()
	s.reapplyLocked(ctx)
}

// reapplyLocked does the work of reapplyForwards and returns how many forwards
// failed. The caller holds fwdMu. A forward that fails is logged and stays
// tracked but unapplied.
func (s *Session) reapplyLocked(ctx context.Context) (failed int) {
	s.mu.Lock()
	gen := s.launches
	var todo []forward
	for _, f := range s.forwards {
		if f.appliedGen != gen {
			todo = append(todo, f)
		}
	}
	s.mu.Unlock()
	sort.Slice(todo, func(i, j int) bool { return todo[i].local < todo[j].local })
	for _, f := range todo {
		if err := s.controlForward(ctx, "forward", f.local, f.remote); err != nil {
			s.Logf("%s: reapply failed: %v", s.Name, err)
			failed++
			continue
		}
		s.mu.Lock()
		key := forwardKey(f.local, f.remote)
		if cur, ok := s.forwards[key]; ok {
			cur.appliedGen = gen
			s.forwards[key] = cur
		}
		s.mu.Unlock()
	}
	return failed
}

// FreePort returns a currently unused loopback TCP port.
func FreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
