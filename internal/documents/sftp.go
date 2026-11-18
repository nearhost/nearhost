package documents

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"syscall"

	"github.com/pkg/sftp"
)

// sftpFS is the file system of a server, reached through an SFTP client.
type sftpFS struct{ c *sftp.Client }

// SFTPFS returns the FS that c reaches. Its errors match LocalFS's: a
// missing file is fs.ErrNotExist and a path through a file is ENOTDIR, so
// the handler answers alike for trees on servers and on the laptop. pkg/sftp
// takes no context, so a call whose ctx ends returns ctx's error at once and
// its reply, when it comes, is dropped.
func SFTPFS(c *sftp.Client) FS { return sftpFS{c: c} }

// RealPath resolves every symlink in p. Servers differ in what their
// realpath does with links (pkg/sftp's server leaves them, and OpenSSH's
// accepts a missing last element), so c.RealPath only makes p absolute, and
// the links are followed here with lstat and readlink, as
// filepath.EvalSymlinks does.
func (f sftpFS) RealPath(ctx context.Context, p string) (string, error) {
	return withContext(ctx, func() (string, error) { return f.realPath(p) }, nil)
}

// realPath is RealPath without the context.
func (f sftpFS) realPath(p string) (string, error) {
	if !path.IsAbs(p) {
		abs, err := f.c.RealPath(p)
		if err != nil {
			return "", mapError("realpath", p, err)
		}
		p = abs
	}
	return evalLinks(p,
		func(name string) (fs.FileInfo, error) {
			info, err := f.c.Lstat(name)
			return info, mapError("lstat", name, err)
		},
		func(name string) (string, error) {
			dest, err := f.c.ReadLink(name)
			return dest, mapError("readlink", name, err)
		})
}

// Stat returns the file information for p, following symlinks.
func (f sftpFS) Stat(ctx context.Context, p string) (fs.FileInfo, error) {
	return withContext(ctx, func() (fs.FileInfo, error) {
		info, err := f.c.Stat(p)
		return info, mapError("stat", p, err)
	}, nil)
}

// Open opens p and checks the opened file is the one at p. SFTP has no call
// that names an open file, so after the open p must still resolve to itself,
// and the file there, not following a last symlink, must have the open
// file's attributes.
func (f sftpFS) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	return withContext(ctx, func() (io.ReadCloser, error) {
		file, err := f.c.Open(p)
		if err != nil {
			return nil, mapError("open", p, err)
		}
		if err := f.checkOpened(file, p); err != nil {
			file.Close()
			return nil, err
		}
		return file, nil
	}, func(r io.ReadCloser) { r.Close() })
}

// checkOpened returns nil when file, opened by the path p, is the file at p
// now, and an error wrapping fs.ErrNotExist when it isn't. Errors talking
// to the server are kept.
func (f sftpFS) checkOpened(file *sftp.File, p string) error {
	opened, err := file.Stat()
	if err != nil {
		return mapError("fstat", p, err)
	}
	real, err := f.realPath(p)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	same := err == nil && path.Clean(real) == path.Clean(p)
	if same {
		at, err := f.c.Lstat(p)
		if err = mapError("lstat", p, err); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		same = err == nil && sameAttributes(opened, at)
	}
	if !same {
		return &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
	}
	return nil
}

// sameAttributes reports whether a and b, from SFTP, may be the same file:
// the same type, permissions, size and modification time, and the same owner
// where the server gives one. SFTP gives no inode number to compare.
func sameAttributes(a, b fs.FileInfo) bool {
	if a.Mode() != b.Mode() || a.Size() != b.Size() || !a.ModTime().Equal(b.ModTime()) {
		return false
	}
	sa, aok := a.Sys().(*sftp.FileStat)
	sb, bok := b.Sys().(*sftp.FileStat)
	return !aok || !bok || (sa.UID == sb.UID && sa.GID == sb.GID)
}

// Home returns the server's working folder, where an SFTP session starts:
// the user's home.
func (f sftpFS) Home(ctx context.Context) (string, error) {
	return withContext(ctx, func() (string, error) {
		home, err := f.c.Getwd()
		return home, mapError("getwd", ".", err)
	}, nil)
}

// withContext runs call and returns what it does, or ctx's error as soon as
// ctx ends. A result that comes after that is passed to discard, when it is
// set, so nothing opened is left behind.
func withContext[T any](ctx context.Context, call func() (T, error), discard func(T)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	type result struct {
		v   T
		err error
	}
	done := make(chan result, 1)
	go func() {
		v, err := call()
		done <- result{v, err}
	}()
	select {
	case r := <-done:
		return r.v, r.err
	case <-ctx.Done():
		go func() {
			if r := <-done; r.err == nil && discard != nil {
				discard(r.v)
			}
		}()
		return zero, ctx.Err()
	}
}

// mapError gives err, from op on p, the error LocalFS would: fs.ErrNotExist
// for a missing file, whether the client says so with os.ErrNotExist or
// with a raw SSH_FX_NO_SUCH_FILE status. Other errors are kept.
func mapError(op, p string, err error) error {
	if err == nil {
		return nil
	}
	var se *sftp.StatusError
	if errors.Is(err, os.ErrNotExist) || (errors.As(err, &se) && se.Code == sshFxNoSuchFile) {
		return &fs.PathError{Op: op, Path: p, Err: fs.ErrNotExist}
	}
	return err
}

// SFTP's status codes for a missing file, and for a client with no
// connection or one that lost it.
const (
	sshFxNoSuchFile     = 2
	sshFxNoConnection   = 6
	sshFxConnectionLost = 7
)

// maxLinks is how many symlinks one path may pass through, as many as
// filepath.EvalSymlinks follows for LocalFS.
const maxLinks = 255

// evalLinks resolves every symlink in the absolute slash-separated path p
// with lstat and readlink, as filepath.EvalSymlinks does on disk. Every
// element must exist: a missing one gives lstat's error. A file with more of
// the path after it gives ENOTDIR here, without asking the server, because
// servers report that as different statuses. A ".." steps back from the
// path resolved so far.
func evalLinks(p string, lstat func(string) (fs.FileInfo, error), readlink func(string) (string, error)) (string, error) {
	resolved := "/"
	rest := strings.Split(p, "/")
	for links := 0; len(rest) > 0; {
		elem := rest[0]
		rest = rest[1:]
		switch elem {
		case "", ".":
			continue
		case "..":
			resolved = path.Dir(resolved)
			continue
		}
		next := path.Join(resolved, elem)
		info, err := lstat(next)
		if err != nil {
			return "", err
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			if !info.IsDir() && len(rest) > 0 {
				return "", &fs.PathError{Op: "realpath", Path: p, Err: syscall.ENOTDIR}
			}
			resolved = next
			continue
		}
		if links++; links > maxLinks {
			return "", &fs.PathError{Op: "realpath", Path: p, Err: syscall.ELOOP}
		}
		dest, err := readlink(next)
		if err != nil {
			return "", err
		}
		if path.IsAbs(dest) {
			resolved = "/"
		}
		rest = append(strings.Split(dest, "/"), rest...)
	}
	return resolved, nil
}
