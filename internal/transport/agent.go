package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"
)

// AgentDir is the directory in the server account's home that holds the
// sockets agents reach nearhost on.
const AgentDir = ".nearhost"

// RuntimeRoot holds each user's runtime directory, /run/user/<uid>, on a
// server with systemd. Where the server account has a private one, the
// socket goes in it and ~/.nearhost holds a link to it: SELinux on Fedora
// and its relatives keeps sshd from making sockets in home folders, but not
// there.
const RuntimeRoot = "/run/user"

// AgentForward is the socket a session forwards to its server, so that a
// coding agent there reaches nearhost on this machine.
type AgentForward struct {
	// Name is the socket's file name in ~/.nearhost on the server: the
	// install id and ".sock".
	Name string
	// Local is what it reaches on this machine: a Unix socket, or on
	// Windows, where ssh cannot forward to one, a loopback address.
	Local string
	// Token is what a request must carry to be served at a loopback Local,
	// and "" for a socket. It is left on the server beside the socket, in
	// a file only the account can read, for the skill's script to send.
	Token string
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

// tokenHeader is the header a request to a loopback agent listener carries
// its token in, as agentapi checks it.
const tokenHeader = "X-Nearhost-Token"

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
			err = errSocketTooLong(remote)
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
	cmd := s.control(ctx, "-O", op, "-R", spec, "--", s.host)
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

// errSocketTooLong is why sshd refused to forward the agent socket to
// remote, a path longer than a Unix socket path can be.
func errSocketTooLong(remote string) error {
	return fmt.Errorf("the socket path %s is %d bytes, longer than a Unix socket path can be on most systems (103 on macOS, 107 on Linux)", remote, len(remote))
}

// prepareAgentOverSFTP is the production prepareAgent: over an SFTP session
// on the master, it finds the home directory and readies the socket there.
func (s *Session) prepareAgentOverSFTP(ctx context.Context, fwd AgentForward) (string, error) {
	return prepareAgentWith(ctx, s.OpenSFTP, s.Name, fwd)
}

// prepareAgentWith readies the agent socket on the server called name over
// an SFTP session from open: it finds the home directory and readies the
// socket there, and returns its path.
func prepareAgentWith(ctx context.Context, open func(context.Context) (*SFTP, error), name string, fwd AgentForward) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	f, err := open(ctx)
	if err != nil {
		return "", err
	}
	defer f.Close()
	home, err := f.Client.Getwd()
	if err != nil {
		return "", fmt.Errorf("find the home directory on %s: %w", name, err)
	}
	probe := ""
	if fwd.Own != "" {
		// A file made here for the call: the server is this machine only if
		// it sees it too. Without it nothing tells this machine from another,
		// and going on could remove this machine's own socket, so it fails.
		p, err := os.CreateTemp(filepath.Dir(fwd.Own), ".probe-*")
		if err != nil {
			return "", fmt.Errorf("make a file to tell %s from this machine: %w", name, err)
		}
		defer os.Remove(p.Name())
		if err := p.Close(); err != nil {
			return "", fmt.Errorf("make a file to tell %s from this machine: %w", name, err)
		}
		probe = filepath.Base(p.Name())
	}
	return PrepareAgentDir(f.Client, home, RuntimeRoot, fwd.Name, fwd.Own, probe, fwd.Token)
}

// PrepareAgentDir makes ~/.nearhost in home with mode 0700 if it is not
// there, and removes the socket called name in it, left by an earlier run of
// this install, so that sshd can bind it again. It returns the socket's
// path. Only that one socket is ever removed: the others are other installs'.
// When the server account has a private runtime directory under runRoot,
// the socket is made ready in a nearhost directory there instead, and its
// path returned, with a link to it at its place in ~/.nearhost; an empty
// runRoot uses none.
// When that path is own, this machine's own socket, and the server sees
// probe, a file the caller has made beside own on this machine, the server
// is this machine: the socket is left alone and ErrThisMachine returned. A
// server that reports probe not found is another machine with its home at
// the same path, and is readied as any other. Without a probe, or when the
// server cannot say, nothing is removed and an error returned.
// When token is not "", the socket's token file, its name with .token in
// place of .sock, is written in ~/.nearhost with mode 0600, holding the
// header that carries token; when it is "", that file is removed if there.
func PrepareAgentDir(c *sftp.Client, home, runRoot, name, own, probe, token string) (string, error) {
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
		// Only a probe the server does not have shows it is another
		// machine. Anything else could be this one, whose socket must stay.
		if probe == "" {
			return "", fmt.Errorf("cannot tell whether the server is this machine: no probe")
		}
		_, err := c.Stat(path.Join(dir, probe))
		switch {
		case err == nil:
			return "", ErrThisMachine
		case !isNotExist(err):
			return "", fmt.Errorf("cannot tell whether the server is this machine: %w", err)
		}
	}
	if err := removeIfThere(c, sock); err != nil {
		return "", fmt.Errorf("remove the old socket %s: %w", sock, err)
	}
	if err := writeAgentToken(c, path.Join(dir, strings.TrimSuffix(name, ".sock")+".token"), token); err != nil {
		return "", err
	}
	run := privateRuntimeDir(c, home, runRoot)
	if run == "" {
		return sock, nil
	}
	dir = path.Join(run, "nearhost")
	if err := c.Mkdir(dir); err != nil {
		info, statErr := c.Lstat(dir)
		if statErr != nil || !info.IsDir() {
			return "", fmt.Errorf("make %s: %w", dir, err)
		}
	}
	if err := c.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("make %s private: %w", dir, err)
	}
	runSock := path.Join(dir, name)
	if err := removeIfThere(c, runSock); err != nil {
		return "", fmt.Errorf("remove the old socket %s: %w", runSock, err)
	}
	if err := c.Symlink(runSock, sock); err != nil {
		return "", fmt.Errorf("link %s to %s: %w", sock, runSock, err)
	}
	return runSock, nil
}

// writeAgentToken writes the token file at p, holding the header line
// that carries token, as curl reads headers from a file: so the token
// never shows on a command line. It is written beside p, readable only by
// the account, and renamed into place, so the script never reads half of
// it. An empty token removes the file, if there is one.
func writeAgentToken(c *sftp.Client, p, token string) error {
	if token == "" {
		if err := removeIfThere(c, p); err != nil {
			return fmt.Errorf("remove the old token %s: %w", p, err)
		}
		return nil
	}
	tmp := p + ".new"
	if err := removeIfThere(c, tmp); err != nil {
		return fmt.Errorf("write the token %s: %w", p, err)
	}
	f, err := c.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return fmt.Errorf("write the token %s: %w", p, err)
	}
	// The mode is set before anything is written: the server's umask
	// decides the mode a file is created with.
	err = f.Chmod(0o600)
	if err == nil {
		_, err = f.Write([]byte(tokenHeader + ": " + token + "\n"))
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		if err = c.PosixRename(tmp, p); err != nil {
			// An SFTP server without the posix-rename extension renames
			// only onto nothing.
			if rerr := removeIfThere(c, p); rerr == nil {
				err = c.Rename(tmp, p)
			}
		}
	}
	if err != nil {
		c.Remove(tmp)
		return fmt.Errorf("write the token %s: %w", p, err)
	}
	return nil
}

// removeIfThere removes the file at p, a socket or a link, if there is one.
func removeIfThere(c *sftp.Client, p string) error {
	if err := c.Remove(p); err != nil && !isNotExist(err) && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// privateRuntimeDir is the runtime directory under root of the account
// that owns home, when it is a directory, not a link, that the account owns
// and only it can open; otherwise "". The server's environment is not seen
// over SFTP, so the account is told by its home's owner.
func privateRuntimeDir(c *sftp.Client, home, root string) string {
	if root == "" {
		return ""
	}
	h, err := c.Stat(home)
	if err != nil {
		return ""
	}
	owner, ok := h.Sys().(*sftp.FileStat)
	if !ok {
		return ""
	}
	dir := path.Join(root, strconv.FormatUint(uint64(owner.UID), 10))
	info, err := c.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return ""
	}
	if st, ok := info.Sys().(*sftp.FileStat); !ok || st.UID != owner.UID {
		return ""
	}
	return dir
}
