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
	s.forwards[key] = forward{local: localPort, remote: remotePort}
	isConnected := s.status == StatusConnected
	gen := s.launches
	s.mu.Unlock()
	if !isConnected {
		return nil
	}
	if err := s.controlForward(ctx, "forward", localPort, remotePort); err != nil {
		s.mu.Lock()
		delete(s.forwards, key)
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
func (s *Session) CancelForward(ctx context.Context, localPort, remotePort int) error {
	s.fwdMu.Lock()
	defer s.fwdMu.Unlock()
	key := forwardKey(localPort, remotePort)
	s.mu.Lock()
	f, known := s.forwards[key]
	delete(s.forwards, key)
	isConnected := s.status == StatusConnected
	s.mu.Unlock()
	if !known || !isConnected || f.appliedGen == 0 {
		return nil
	}
	return s.controlForward(ctx, "cancel", localPort, remotePort)
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

// reapplyForwards adds desired forwards that the current master does not yet
// have. The supervisor calls it after each (re)connect.
func (s *Session) reapplyForwards(ctx context.Context) {
	s.fwdMu.Lock()
	defer s.fwdMu.Unlock()
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
