package transport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"

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
	cmd := s.longLived(context.Background(), "-S", s.SocketPath(), "-o", "BatchMode=yes",
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
	// The client closes its end of the pipe by itself when it sees ssh go,
	// which the kill above can make happen first. The pipe is closed either
	// way.
	if errors.Is(err, os.ErrClosed) {
		err = nil
	}
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

// FileWriter writes a file under a project root. Implementations replace the
// target atomically, so a reader never sees a half-written file.
type FileWriter interface {
	WriteFile(name string, data []byte) error
}

// defaultFileMode is the mode of a file the writer creates; a file it
// replaces keeps its own.
const defaultFileMode fs.FileMode = 0o644

// Writer returns a FileWriter rooted at a remote directory.
func (f *SFTP) Writer(root string) FileWriter {
	return NewSFTPWriter(f.Client, root)
}

type sftpWriter struct {
	c    *sftp.Client
	root string
}

// NewSFTPWriter adapts an SFTP client to FileWriter.
func NewSFTPWriter(c *sftp.Client, root string) FileWriter {
	return sftpWriter{c: c, root: root}
}

// WriteFile writes to a temporary file beside the target and renames it into
// place. Same directory keeps the rename on one filesystem, and the leading
// dot keeps the temporary file out of the way of project tooling. The mode is
// set explicitly because the server's umask would otherwise decide it.
func (w sftpWriter) WriteFile(name string, data []byte) (err error) {
	target := path.Join(w.root, name)
	mode := defaultFileMode
	if info, statErr := w.c.Stat(target); statErr == nil {
		mode = info.Mode().Perm()
	} else if !isNotExist(statErr) {
		return statErr
	}

	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	tmp := path.Join(w.root, "."+name+".nearhost-"+hex.EncodeToString(suffix[:]))
	f, err := w.c.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			w.c.Remove(tmp)
		}
	}()
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if _, ok := w.c.HasExtension("posix-rename@openssh.com"); ok {
		return w.c.PosixRename(tmp, target)
	}
	// Plain SFTP rename refuses to replace an existing file, so the target
	// has to go first. That leaves a brief window with no file; it is the
	// best a server without the extension allows.
	if err = w.c.Rename(tmp, target); err != nil {
		if rmErr := w.c.Remove(target); rmErr != nil && !isNotExist(rmErr) {
			return err
		}
		err = w.c.Rename(tmp, target)
	}
	return err
}

type dirWriter struct{ root string }

// DirWriter returns a FileWriter rooted at a local directory.
func DirWriter(root string) FileWriter { return dirWriter{root: root} }

// WriteFile follows the same temp-file-then-rename scheme as the SFTP writer.
// os.CreateTemp makes the file 0600, so the mode is set before the rename.
func (w dirWriter) WriteFile(name string, data []byte) (err error) {
	target := filepath.Join(w.root, name)
	mode := defaultFileMode
	if info, statErr := os.Stat(target); statErr == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return statErr
	}
	f, err := os.CreateTemp(w.root, "."+name+".nearhost-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			os.Remove(f.Name())
		}
	}()
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), target)
}
