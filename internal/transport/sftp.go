package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"

	"github.com/pkg/sftp"

	"github.com/nearhost/nearhost/internal/discovery"
)

// SFTP is an SFTP client running over the session's master connection.
type SFTP struct {
	Client *sftp.Client
	cmd    *exec.Cmd
}

// OpenSFTP starts `ssh -s sftp` multiplexed over the control socket. ctx
// bounds the handshake only; the subsystem lives until Close.
func (s *Session) OpenSFTP(ctx context.Context) (*SFTP, error) {
	// A session opened through the master also asks it for the forwards in
	// the user's configuration unless they are cleared, and with ControlMaster
	// set there it could take the master's place on a socket that has died.
	cmd := s.command(context.Background(), "-S", s.SocketPath(), "-o", "BatchMode=yes",
		"-o", "RemoteCommand=none",
		"-o", "ClearAllForwardings=yes",
		"-o", "ControlMaster=no",
		"-s", s.Host, "sftp")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start sftp: %w", err)
	}
	handshakeDone := make(chan struct{})
	go killOnCancel(ctx, handshakeDone, func() {
		cmd.Process.Kill() // unblocks the handshake read
	})
	client, err := sftp.NewClientPipe(stdout, stdin)
	close(handshakeDone)
	if err == nil && ctx.Err() != nil {
		client.Close()
		err = ctx.Err()
		client = nil
	}
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return nil, fmt.Errorf("sftp handshake with %s: %w", s.Host, err)
	}
	return &SFTP{Client: client, cmd: cmd}, nil
}

// killOnCancel calls kill if ctx ends before done is closed. When both are
// ready, done wins, so a finished handshake is never killed.
func killOnCancel(ctx context.Context, done <-chan struct{}, kill func()) {
	select {
	case <-ctx.Done():
		select {
		case <-done:
		default:
			kill()
		}
	case <-done:
	}
}

// Close ends the SFTP subsystem. It kills ssh first so a stuck ssh cannot
// block closing the client.
func (f *SFTP) Close() error {
	f.cmd.Process.Kill()
	err := f.Client.Close()
	f.cmd.Wait()
	return err
}

// Reader returns a FileReader rooted at a remote directory.
func (f *SFTP) Reader(root string) discovery.FileReader {
	return NewSFTPReader(f.Client, root)
}

type sftpReader struct {
	c    *sftp.Client
	root string
}

// NewSFTPReader adapts an SFTP client to discovery.FileReader.
func NewSFTPReader(c *sftp.Client, root string) discovery.FileReader {
	return sftpReader{c: c, root: root}
}

func (r sftpReader) ReadFile(name string) ([]byte, error) {
	f, err := r.c.Open(path.Join(r.root, name))
	if err != nil {
		if isNotExist(err) {
			return nil, fmt.Errorf("%s: %w", name, fs.ErrNotExist)
		}
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func isNotExist(err error) bool {
	if errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err) {
		return true
	}
	var se *sftp.StatusError
	return errors.As(err, &se) && se.Code == 2 // SSH_FX_NO_SUCH_FILE
}
