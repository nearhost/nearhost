package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/discovery"
	"github.com/nearhost/nearhost/internal/transport"
)

// sshClient says which OpenSSH client the commands run. The zero value is the
// ssh on PATH; tests point it at a fake.
type sshClient struct {
	path string
	env  []string
}

const (
	// checkTimeout bounds the connectivity check, which itself gives ssh ten
	// seconds to connect.
	checkTimeout = 15 * time.Second
	// remoteOpenTimeout bounds connecting to a server and opening SFTP on it.
	remoteOpenTimeout = 30 * time.Second
)

func (c sshClient) session(srv config.Server, runDir string) *transport.Session {
	s := transport.NewSession(srv.Name, srv.SSHHost, runDir)
	if c.path != "" {
		s.SSHPath = c.path
	}
	s.Env = c.env
	return s
}

// check reports whether ssh reaches srv without prompting. A refusal is a
// *transport.CheckError carrying what ssh printed.
func (c sshClient) check(ctx context.Context, srv config.Server) error {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	return c.session(srv, "").Check(ctx)
}

// sshHint says how to repair access to host; then is what to do afterwards.
func sshHint(host, then string) string {
	return fmt.Sprintf("run `ssh %s` once in a terminal to fix access, then %s", host, then)
}

// shortTempDir is where a private run directory goes. A Unix socket path
// holds 104 bytes on macOS, of which OpenSSH needs 17 beyond the control
// path while it binds, and the usual temporary directory there
// (/var/folders/...) already takes about fifty. /tmp exists on macOS and
// Linux alike.
const shortTempDir = "/tmp"

// privateSession returns a session to srv with a run directory of its own,
// and a function that stops the session and removes the directory. The
// directory is created for this one session, readable by this user only, and
// its socket has a fixed short name: the path does not grow with the
// server's name.
func (c sshClient) privateSession(srv config.Server) (*transport.Session, func() error, error) {
	runDir, err := os.MkdirTemp(shortTempDir, "nh-")
	if err != nil {
		return nil, nil, fmt.Errorf("create run directory: %w", err)
	}
	sess := c.session(config.Server{Name: "add", SSHHost: srv.SSHHost}, runDir)
	// `add` does not handle signals, so Ctrl-C ends it on the spot. Its ssh
	// stays in its process group and ends with it.
	sess.Foreground = true
	return sess, func() error {
		sess.Stop()
		return os.RemoveAll(runDir)
	}, nil
}

// connectOnce waits for sess, which has just been started, to connect. A
// session keeps trying after a failure; a one-off command has no use for
// that, so this returns what ssh printed on the first attempt that failed, as
// a *transport.CheckError.
func connectOnce(ctx context.Context, sess *transport.Session) error {
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if sess.Status() == transport.StatusConnected {
			return nil
		}
		// A new session has no last error until an attempt fails.
		if msg := sess.LastError(); msg != "" {
			return &transport.CheckError{Host: sess.Host, Stderr: msg}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// openRemote opens projectPath on srv for reading over SFTP, on an SSH
// connection of its own that the returned function ends. It also returns the
// directory it opened, as projectPathOn finds it. It runs no command
// on the server, not even to test the connection (an account may be allowed
// SFTP and nothing else), and writes nothing there.
//
// The connection uses a private run directory. A session takes over whatever
// master it finds on its control socket, so sharing the directory of a
// running `nearhost up` would cut that one off.
func (c sshClient) openRemote(ctx context.Context, srv config.Server, projectPath string) (discovery.FileReader, string, func() error, error) {
	sess, end, err := c.privateSession(srv)
	if err != nil {
		return nil, "", nil, err
	}
	fail := func(err error) (discovery.FileReader, string, func() error, error) {
		return nil, "", nil, errors.Join(err, end())
	}

	sess.Start()
	ctx, cancel := context.WithTimeout(ctx, remoteOpenTimeout)
	defer cancel()
	if err := connectOnce(ctx, sess); err != nil {
		var refused *transport.CheckError
		if errors.As(err, &refused) {
			return fail(fmt.Errorf("%w; %s", err, sshHint(srv.SSHHost, "add the project again")))
		}
		return fail(fmt.Errorf("connect to %s: %w", srv.SSHHost, err))
	}
	client, err := sess.OpenSFTP(ctx)
	if err != nil {
		return fail(err)
	}
	// The home directory is where the SFTP server starts.
	localHome, _ := os.UserHomeDir()
	isDir := func(p string) bool {
		info, err := client.Client.Stat(p)
		return err == nil && info.IsDir()
	}
	projectPath, err = projectPathOn(projectPath, localHome, client.Client.Getwd, isDir)
	if err != nil {
		return fail(errors.Join(fmt.Errorf("find the home directory on server %s: %w", srv.Name, err), client.Close()))
	}
	if info, err := client.Client.Stat(projectPath); err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("not a directory")
		}
		return fail(errors.Join(fmt.Errorf("%s on server %s: %w", projectPath, srv.Name, err), client.Close()))
	}
	return client.Reader(projectPath), projectPath, func() error {
		return errors.Join(client.Close(), end())
	}, nil
}

// projectPathOn works out which directory on a server the path given to add
// means. "~" and "~/..." are under the home directory there. An absolute path
// under this machine's home that is not a directory on the server is taken
// under the server's home instead, when that is one: the shell expanded an
// unquoted ~ before add saw it. Otherwise the path is returned as given.
func projectPathOn(given, localHome string, remoteHome func() (string, error), isDir func(string) bool) (string, error) {
	if given == "~" || strings.HasPrefix(given, "~/") {
		home, err := remoteHome()
		if err != nil {
			return "", err
		}
		return path.Join(home, given[1:]), nil
	}
	rel, under := strings.CutPrefix(given, strings.TrimSuffix(localHome, "/")+"/")
	if localHome == "" || !under || isDir(given) {
		return given, nil
	}
	if home, err := remoteHome(); err == nil {
		if candidate := path.Join(home, rel); isDir(candidate) {
			return candidate, nil
		}
	}
	return given, nil
}
