package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/pkg/sftp"
)

// AgentDir is the directory in the server account's home that holds the
// sockets agents reach nearhost on.
const AgentDir = ".nearhost"

// AgentForward is the socket a session forwards to its server, so that a
// coding agent there reaches nearhost on this machine.
type AgentForward struct {
	// Name is the socket's file name in ~/.nearhost on the server: the
	// install id and ".sock".
	Name string
	// Local is the socket on this machine that it reaches.
	Local string
	// Own is this machine's own agent socket. A server whose socket would
	// be at that path shares this machine's home: it is not forwarded to,
	// and its agents reach Nearhost on Own as this machine's.
	Own string
}

// ErrThisMachine is returned by PrepareAgentDir when the server's socket
// would be this machine's own.
var ErrThisMachine = errors.New("the server shares this machine's home")

// SetAgentForward makes the session forward f to the server each time it
// connects. Call it before Start.
func (s *Session) SetAgentForward(f AgentForward) {
	s.mu.Lock()
	s.agent = &f
	s.mu.Unlock()
}

// applyAgent prepares ~/.nearhost on the server and forwards the agent
// socket there, on the master that has just connected. A failure leaves the
// master and its forwards as they are: it only means agents cannot reach
// nearhost from this server, which Health says.
func (s *Session) applyAgent(ctx context.Context) {
	s.mu.Lock()
	f := s.agent
	s.mu.Unlock()
	if f == nil {
		return
	}
	remote, err := s.prepareAgent(ctx, *f)
	if errors.Is(err, ErrThisMachine) {
		s.mu.Lock()
		s.agentPath, s.agentErr = "", ""
		s.mu.Unlock()
		s.Logf("%s: agents here reach nearhost on this machine's own socket", s.Name)
		return
	}
	if err == nil {
		err = s.controlAgent(ctx, "forward", remote, f.Local)
	}
	if ctx.Err() != nil {
		return // stopping: what this pass found says nothing
	}
	s.mu.Lock()
	if err != nil {
		s.agentPath, s.agentErr = "", err.Error()
	} else {
		s.agentPath, s.agentErr = remote, ""
	}
	s.mu.Unlock()
	if err != nil {
		s.Logf("%s: agents unavailable: %v", s.Name, err)
		return
	}
	s.Logf("%s: agents reach nearhost at %s", s.Name, remote)
}

// cancelAgent asks the master to stop forwarding the agent socket, before
// it exits. Whatever it answers, the master is ended next.
func (s *Session) cancelAgent(ctx context.Context) {
	s.mu.Lock()
	f, remote := s.agent, s.agentPath
	s.agentPath = ""
	s.mu.Unlock()
	if f == nil || remote == "" {
		return
	}
	s.controlAgent(ctx, "cancel", remote, f.Local)
}

// controlAgent runs one `ssh -O forward|cancel -R remote:local`.
func (s *Session) controlAgent(ctx context.Context, op, remote, local string) error {
	spec := remote + ":" + local
	cmd := s.control(ctx, "-O", op, "-R", spec, s.Host)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("the server refused to forward %s: %s", remote, msg)
	}
	return nil
}

// prepareAgentOverSFTP is the production prepareAgent: over an SFTP session
// on the master, it finds the home directory and readies the socket there.
func (s *Session) prepareAgentOverSFTP(ctx context.Context, fwd AgentForward) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	f, err := s.OpenSFTP(ctx)
	if err != nil {
		return "", err
	}
	defer f.Close()
	home, err := f.Client.Getwd()
	if err != nil {
		return "", fmt.Errorf("find the home directory on %s: %w", s.Name, err)
	}
	return PrepareAgentDir(f.Client, home, fwd.Name, fwd.Own)
}

// PrepareAgentDir makes ~/.nearhost in home with mode 0700 if it is not
// there, and removes the socket called name in it, left by an earlier run of
// this install, so that sshd can bind it again. It returns the socket's
// path. Only that one socket is ever removed: the others are other installs'.
// When that path is own, this machine's own socket, it is left alone and
// ErrThisMachine returned.
func PrepareAgentDir(c *sftp.Client, home, name, own string) (string, error) {
	if name == "" || strings.ContainsAny(name, "/\x00") || name == "." || name == ".." {
		return "", fmt.Errorf("agent socket name %q is not a file name", name)
	}
	dir := path.Join(home, AgentDir)
	if err := c.Mkdir(dir); err != nil {
		info, statErr := c.Stat(dir) // a symlink to a directory will do
		if statErr != nil || !info.IsDir() {
			return "", fmt.Errorf("make %s: %w", dir, err)
		}
	}
	if err := c.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("make %s private: %w", dir, err)
	}
	sock := path.Join(dir, name)
	if own != "" && sock == own {
		return "", ErrThisMachine
	}
	if err := c.Remove(sock); err != nil && !isNotExist(err) && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("remove the old socket %s: %w", sock, err)
	}
	return sock, nil
}
