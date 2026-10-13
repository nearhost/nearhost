package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
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

// openRemote opens projectPath on srv for reading over SFTP, on an SSH
// connection of its own that the returned function ends. It runs no command
// on the server and writes nothing there.
//
// The connection uses a private run directory. A session takes over whatever
// master it finds on its control socket, so sharing the directory of a
// running `nearhost up` would cut that one off.
func (c sshClient) openRemote(ctx context.Context, srv config.Server, projectPath string) (discovery.FileReader, func() error, error) {
	if err := c.check(ctx, srv); err != nil {
		return nil, nil, fmt.Errorf("%w; %s", err, sshHint(srv.SSHHost, "add the project again"))
	}
	runDir, err := os.MkdirTemp("", "nh-add-")
	if err != nil {
		return nil, nil, fmt.Errorf("create run directory: %w", err)
	}
	sess := c.session(srv, runDir)
	end := func() error {
		sess.Stop()
		return os.RemoveAll(runDir)
	}
	fail := func(err error) (discovery.FileReader, func() error, error) {
		return nil, nil, errors.Join(err, end())
	}

	sess.Start()
	ctx, cancel := context.WithTimeout(ctx, remoteOpenTimeout)
	defer cancel()
	if err := sess.WaitConnected(ctx); err != nil {
		err = fmt.Errorf("connect to %s: %w", srv.SSHHost, err)
		if last := sess.LastError(); last != "" && errors.Is(err, context.DeadlineExceeded) {
			err = fmt.Errorf("%w: %s", err, last)
		}
		return fail(err)
	}
	client, err := sess.OpenSFTP(ctx)
	if err != nil {
		return fail(err)
	}
	if info, err := client.Client.Stat(projectPath); err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("not a directory")
		}
		return fail(errors.Join(fmt.Errorf("%s on server %s: %w", projectPath, srv.Name, err), client.Close()))
	}
	return client.Reader(projectPath), func() error {
		return errors.Join(client.Close(), end())
	}, nil
}
