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
	return withContext(ctx, func() (string, error) {
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
	}, nil)
}

// Stat returns the file information for p, following symlinks.
func (f sftpFS) Stat(ctx context.Context, p string) (fs.FileInfo, error) {
	return withContext(ctx, func() (fs.FileInfo, error) {
		info, err := f.c.Stat(p)
		return info, mapError("stat", p, err)
	}, nil)
}

// Open opens p below root and checks the opened file is the one at p, so the
// path below root it returns is p's. SFTP has no call that names an open
// file, so after the open every element of p must still be what it names,
// with no symlink, and the file p names must have the open file's
// attributes. Someone who can write in the tree can still beat this by
// swapping a folder back and forth while giving a file in the tree the
// attributes of the one outside; the owner comparison stops another
// account's swap. A tree on a server has no RootHandle, so rootHandle is
// not used.
func (f sftpFS) Open(ctx context.Context, root, _, p string) (io.ReadCloser, string, error) {
	rel, ok := Below(root, p)
	if !ok {
		return nil, "", &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
	}
	r, err := withContext(ctx, func() (io.ReadCloser, error) {
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
	if err != nil {
		return nil, "", err
	}
	return r, rel, nil
}

// errMoved and errChanged are what compareOpened finds: the open file isn't
// the one at the path, or it or a folder on the way changed in size or
// modification time, as a file being written does.
var (
	errMoved   = errors.New("the opened file isn't the one at its path")
	errChanged = errors.New("the opened file changed while it was checked")
)

// checkOpened returns nil when file, opened by the path p, is the file at p
// now, and an error wrapping fs.ErrNotExist when it isn't. A change in size
// or modification time alone is checked once more first. Errors talking to
// the server are kept.
func (f sftpFS) checkOpened(file *sftp.File, p string) error {
	err := f.compareOpened(file, p)
	if errors.Is(err, errChanged) {
		err = f.compareOpened(file, p)
	}
	if errors.Is(err, errMoved) || errors.Is(err, errChanged) {
		return &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
	}
	return err
}

// sftpCheckHook, when set, runs in compareOpened at stage "walk", before the
// path is walked, and "fstat", between the walk's last lstat and the open
// file's stat. Tests set it to change the tree in those windows.
var sftpCheckHook func(stage string)

// compareOpened walks p with lstat, requiring each element to be what it
// names, a folder up to the last and no symlink; then stats the open file at
// once and compares it with the last element; then lstats each folder again
// and compares it with what the walk saw. It returns errMoved, errChanged,
// or the server's error.
func (f sftpFS) compareOpened(file *sftp.File, p string) error {
	if sftpCheckHook != nil {
		sftpCheckHook("walk")
	}
	type seen struct {
		name string
		info fs.FileInfo
	}
	var folders []seen
	var last fs.FileInfo
	name := "/"
	elems := strings.Split(strings.Trim(path.Clean(p), "/"), "/")
	for i, elem := range elems {
		name = path.Join(name, elem)
		info, err := f.c.Lstat(name)
		if err = mapError("lstat", name, err); errors.Is(err, fs.ErrNotExist) {
			return errMoved
		} else if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 || (i < len(elems)-1 && !info.IsDir()) {
			return errMoved
		}
		if i < len(elems)-1 {
			folders = append(folders, seen{name, info})
		}
		last = info
	}
	if sftpCheckHook != nil {
		sftpCheckHook("fstat")
	}
	opened, err := file.Stat()
	if err != nil {
		return mapError("fstat", p, err)
	}
	result := compareAttributes(opened, last)
	if result == errMoved {
		return result
	}
	for _, d := range folders {
		info, err := f.c.Lstat(d.name)
		if err = mapError("lstat", d.name, err); errors.Is(err, fs.ErrNotExist) {
			return errMoved
		} else if err != nil {
			return err
		}
		switch m := compareAttributes(d.info, info); m {
		case errMoved:
			return m
		case errChanged:
			result = m
		}
	}
	return result
}

// compareAttributes compares a and b, from SFTP, which gives no inode number
// to compare: errMoved when their type, permissions or owner differ, and
// errChanged when only their size or modification time does.
func compareAttributes(a, b fs.FileInfo) error {
	sa, aok := a.Sys().(*sftp.FileStat)
	sb, bok := b.Sys().(*sftp.FileStat)
	if a.Mode() != b.Mode() || (aok && bok && (sa.UID != sb.UID || sa.GID != sb.GID)) {
		return errMoved
	}
	if a.Size() != b.Size() || !a.ModTime().Equal(b.ModTime()) {
		return errChanged
	}
	return nil
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
