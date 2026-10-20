package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
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
	// be at that path, and that sees the same files there, shares this
	// machine's home: it is not forwarded to, and its agents reach Nearhost
	// on Own as this machine's. Another machine can have a home at the same
	// path, as two Macs with one user name do; it is forwarded to.
	Own string
}

// AgentProblem is why agents on a server cannot reach Nearhost.
type AgentProblem int

const (
	// AgentOK: none, or not tried yet.
	AgentOK AgentProblem = iota
	// AgentNotReady: ~/.nearhost could not be readied over SFTP.
	AgentNotReady
	// AgentRefused: sshd refused the forward, as AllowStreamLocalForwarding
	// no does.
	AgentRefused
	// AgentTooLong: sshd refused the forward, and the socket's path is
	// longer than a Unix socket path can be on most systems.
	AgentTooLong
)

// maxRemoteSocket is the longest socket path every common system binds:
// sun_path holds 104 bytes on macOS and the BSDs and 108 on Linux, the
// final NUL included.
const maxRemoteSocket = 103

// AgentError is why agents on a server cannot reach Nearhost, with the
// kind of problem it is.
type AgentError struct {
	Problem AgentProblem
	Msg     string
}

func (e *AgentError) Error() string { return e.Msg }

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
		s.agentPath, s.agentErr, s.agentProblem = "", "", AgentOK
		s.mu.Unlock()
		s.Logf("%s: agents here reach nearhost on this machine's own socket", s.Name)
		return
	}
	problem := AgentNotReady
	if err == nil {
		problem = AgentRefused
		err = s.controlAgent(ctx, "forward", remote, f.Local)
		if err != nil && len(remote) > maxRemoteSocket {
			// The mux client says only that the forward failed. A path this
			// long is the likely reason, and one no sshd setting fixes.
			problem = AgentTooLong
			err = fmt.Errorf("the socket path %s is %d bytes, longer than a Unix socket path can be on most systems (103 on macOS, 107 on Linux)", remote, len(remote))
		}
	}
	if ctx.Err() != nil {
		return // stopping: what this pass found says nothing
	}
	s.mu.Lock()
	if err != nil {
		s.agentPath, s.agentErr, s.agentProblem = "", err.Error(), problem
	} else {
		s.agentPath, s.agentErr, s.agentProblem = remote, "", AgentOK
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
	probe := ""
	if fwd.Own != "" {
		// A file made here for the call: the server is this machine only if
		// it sees it too. Without it the server cannot be this machine,
		// whose own socket's directory would be there.
		if p, err := os.CreateTemp(filepath.Dir(fwd.Own), ".probe-*"); err == nil {
			p.Close()
			defer os.Remove(p.Name())
			probe = filepath.Base(p.Name())
		}
	}
	return PrepareAgentDir(f.Client, home, fwd.Name, fwd.Own, probe)
}

// PrepareAgentDir makes ~/.nearhost in home with mode 0700 if it is not
// there, and removes the socket called name in it, left by an earlier run of
// this install, so that sshd can bind it again. It returns the socket's
// path. Only that one socket is ever removed: the others are other installs'.
// When that path is own, this machine's own socket, and the server sees
// probe, a file the caller has made beside own on this machine, the server
// is this machine: the socket is left alone and ErrThisMachine returned. A
// server that does not see probe is another machine with its home at the
// same path, and is readied as any other.
func PrepareAgentDir(c *sftp.Client, home, name, own, probe string) (string, error) {
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
	if own != "" && sock == own && probe != "" {
		if _, err := c.Stat(path.Join(dir, probe)); err == nil {
			return "", ErrThisMachine
		}
	}
	if err := c.Remove(sock); err != nil && !isNotExist(err) && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("remove the old socket %s: %w", sock, err)
	}
	return sock, nil
}
