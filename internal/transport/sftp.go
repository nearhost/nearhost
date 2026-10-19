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
	// posixRename records whether the server can replace a file atomically.
	posixRename bool
	// rename stands in for the client's plain rename, so tests can fail it.
	rename func(oldname, newname string) error
}

// NewSFTPWriter adapts an SFTP client to FileWriter.
func NewSFTPWriter(c *sftp.Client, root string) FileWriter {
	_, ok := c.HasExtension("posix-rename@openssh.com")
	return sftpWriter{c: c, root: root, posixRename: ok, rename: c.Rename}
}

// resolve follows symlinks at the target, resolving relative links against
// the link's directory, so a link is written through rather than replaced.
func (w sftpWriter) resolve(target string) (string, error) {
	for i := 0; i < 16; i++ {
		info, err := w.c.Lstat(target)
		if err != nil || info.Mode()&fs.ModeSymlink == 0 {
			return target, nil
		}
		dest, err := w.c.ReadLink(target)
		if err != nil {
			return "", err
		}
		if !path.IsAbs(dest) {
			dest = path.Join(path.Dir(target), dest)
		}
		target = dest
	}
	return "", fmt.Errorf("%s: too many levels of symbolic links", target)
}

func randomSuffix() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// WriteFile writes to a temporary file beside the target and renames it into
// place. Same directory keeps the rename on one filesystem, and the leading
// dot keeps the temporary file out of the way of project tooling. The mode is
// set explicitly because the server's umask would otherwise decide it. A
// symlink at name is written through: the file it points to is replaced and
// the link stays.
func (w sftpWriter) WriteFile(name string, data []byte) (err error) {
	target, err := w.resolve(path.Join(w.root, name))
	if err != nil {
		return err
	}
	mode := defaultFileMode
	if info, statErr := w.c.Stat(target); statErr == nil {
		mode = info.Mode().Perm()
	} else if !isNotExist(statErr) {
		return statErr
	}

	suffix, err := randomSuffix()
	if err != nil {
		return err
	}
	dir, base := path.Dir(target), path.Base(target)
	tmp := path.Join(dir, "."+base+".nearhost-"+suffix)
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
	if w.posixRename {
		return w.c.PosixRename(tmp, target)
	}
	return w.replaceWithoutPosixRename(tmp, target, dir, base, suffix)
}

// replaceWithoutPosixRename moves tmp over target on a server whose plain
// rename refuses to replace a file. The original is renamed aside rather than
// deleted, so a failure can put it back and neither version is lost.
func (w sftpWriter) replaceWithoutPosixRename(tmp, target, dir, base, suffix string) error {
	err := w.rename(tmp, target)
	if err == nil {
		return nil
	}
	info, statErr := w.c.Lstat(target)
	if statErr != nil || info.IsDir() {
		return err // not blocked by an existing file, or not ours to replace
	}
	backup := path.Join(dir, "."+base+".nearhost-old-"+suffix)
	if err := w.rename(target, backup); err != nil {
		return err
	}
	if err := w.rename(tmp, target); err != nil {
		if restoreErr := w.rename(backup, target); restoreErr != nil {
			return fmt.Errorf("%w (and restoring the original from %s failed: %v)", err, backup, restoreErr)
		}
		return err
	}
	w.c.Remove(backup)
	return nil
}

type dirWriter struct{ root string }

// DirWriter returns a FileWriter rooted at a local directory.
func DirWriter(root string) FileWriter { return dirWriter{root: root} }

// WriteFile follows the same temp-file-then-rename scheme as the SFTP writer,
// including writing through a symlink at name. os.CreateTemp makes the file
// 0600, so the mode is set before the rename.
func (w dirWriter) WriteFile(name string, data []byte) (err error) {
	target := filepath.Join(w.root, name)
	if resolved, evalErr := filepath.EvalSymlinks(target); evalErr == nil {
		target = resolved
	}
	mode := defaultFileMode
	if info, statErr := os.Stat(target); statErr == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return statErr
	}
	f, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".nearhost-*")
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
