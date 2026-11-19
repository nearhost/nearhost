package documents

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// FS is the file system of the server a tree is on: the laptop's own, or a
// server's reached over SFTP. A missing file's error satisfies
// errors.Is(err, fs.ErrNotExist).
type FS interface {
	// RealPath returns p with every symlink resolved.
	RealPath(ctx context.Context, p string) (string, error)
	// Stat returns the file information for p, following symlinks.
	Stat(ctx context.Context, p string) (fs.FileInfo, error)
	// Open opens p, a path RealPath returned below the tree's resolved
	// root, for reading. rootHandle is the tree's RootHandle. It returns
	// the path below root of the file it actually opened, which differs
	// from p's when a folder on the way was swapped for a symlink after p
	// was resolved, so the caller can apply its rules to that. It gives an
	// error wrapping fs.ErrNotExist when the opened file isn't below root,
	// or the root folder isn't the one the tree was made with.
	Open(ctx context.Context, root, rootHandle, p string) (io.ReadCloser, string, error)
	// Home returns the user's home folder.
	Home(ctx context.Context) (string, error)
}

// ErrNotConnected is wrapped by errors for a server Nearhost isn't connected
// to.
var ErrNotConnected = errors.New("the server isn't connected")

// Opener returns the FS for server. It returns an error wrapping
// ErrNotConnected for a server that isn't connected.
type Opener func(ctx context.Context, server string) (FS, error)

// LocalFS is the laptop's own file system.
type LocalFS struct{}

// RealPath resolves p's symlinks with filepath.EvalSymlinks and returns the
// result in slash form, as the documents package works in slash form. The
// other methods take slash or native form, as Windows' file APIs accept both.
func (LocalFS) RealPath(_ context.Context, p string) (string, error) {
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(real), nil
}

// Stat returns os.Stat(p).
func (LocalFS) Stat(_ context.Context, p string) (fs.FileInfo, error) {
	return os.Stat(p)
}

// Open opens p and returns the path below root of the file it opened, as
// the system names the open file. The root folder is opened as well, and its
// path must be rootHandle, which the same call gave when the tree was made,
// so both share one spelling of drives, links, case and Unicode form. With no
// rootHandle, or where the system can't name the open file, the file at p,
// which must still resolve to itself, must be the opened one.
func (LocalFS) Open(_ context.Context, root, rootHandle, p string) (io.ReadCloser, string, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, "", err
	}
	rel, err := openedBelowRoot(f, root, rootHandle, p)
	if err != nil {
		f.Close()
		return nil, "", err
	}
	return f, rel, nil
}

// openedBelowRoot returns the path below root of the open file f, which was
// opened by the path p, or an error wrapping fs.ErrNotExist when f isn't
// below root or the root folder's path is no longer rootHandle.
func openedBelowRoot(f *os.File, root, rootHandle, p string) (string, error) {
	missing := &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
	opened, err := openedPath(f)
	if err != nil || rootHandle == "" {
		if !sameFileAt(f, p) {
			return "", missing
		}
		rel, ok := Below(root, filepath.ToSlash(p))
		if !ok {
			return "", missing
		}
		return rel, nil
	}
	current := rootHandlePath(root)
	if !sameRootHandle(runtime.GOOS, current, rootHandle) {
		return "", missing
	}
	rel, ok := openedBelow(runtime.GOOS, current, opened)
	if !ok {
		return "", missing
	}
	return rel, nil
}

// rootHandlePath opens the folder root and returns the path the system gives
// for it, or "" when it can't be opened or the system can't name it.
func rootHandlePath(root string) string {
	d, err := os.Open(root)
	if err != nil {
		return ""
	}
	defer d.Close()
	p, err := openedPath(d)
	if err != nil {
		return ""
	}
	return p
}

// sameRootHandle reports whether current, the path goos gives now for a
// tree's root folder, is stored, the path it gave when the tree was made.
// Both come from the same call, so they are compared as they are: without
// case on Windows, as its file systems do, and exactly elsewhere. An empty
// current, from a root that can't be opened now, is never the same.
func sameRootHandle(goos, current, stored string) bool {
	if current == "" {
		return false
	}
	return current == stored || (goos == "windows" && strings.EqualFold(current, stored))
}

// openedBelow returns the path of file below root, where both are paths goos
// gives for open handles, in one spelling. Windows compares without case, as
// its file systems do. ok is false when file isn't below root, a segment of
// the path below starts with ".", or, on Linux, the path ends " (deleted)",
// which /proc gives for a file deleted since it was opened.
func openedBelow(goos, root, file string) (rel string, ok bool) {
	if goos == "linux" && strings.HasSuffix(file, " (deleted)") {
		return "", false
	}
	root, file = slashed(goos, root), slashed(goos, file)
	prefix := root
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	if len(file) <= len(prefix) {
		return "", false
	}
	head := file[:len(prefix)]
	if head != prefix && (goos != "windows" || !strings.EqualFold(head, prefix)) {
		return "", false
	}
	rel = file[len(prefix):]
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || strings.HasPrefix(seg, ".") {
			return "", false
		}
	}
	return rel, true
}

// slashed returns p, a path goos gives for an open handle, in slash form:
// on Windows without the \\?\ or \\?\UNC\ prefix GetFinalPathNameByHandle
// adds.
func slashed(goos, p string) string {
	if goos != "windows" {
		return p
	}
	return strings.ReplaceAll(cleanFinalPath(p), `\`, "/")
}

// cleanFinalPath turns the \\?\C:\x or \\?\UNC\srv\share\x that Windows'
// GetFinalPathNameByHandle returns into C:\x or \\srv\share\x. Other paths
// are returned as they are.
func cleanFinalPath(p string) string {
	if rest, ok := strings.CutPrefix(p, `\\?\UNC\`); ok {
		return `\\` + rest
	}
	return strings.TrimPrefix(p, `\\?\`)
}

// sameFileAt reports whether the open file f is the one at p now: p still
// resolves to itself, and the file there, not following a last symlink, is
// f. It is the check where the system can't name an open file or the tree
// has no RootHandle, and it can be beaten by a folder on the way that is
// swapped back and forth between its two steps.
func sameFileAt(f *os.File, p string) bool {
	real, err := filepath.EvalSymlinks(p)
	if err != nil || filepath.Clean(real) != filepath.Clean(p) {
		return false
	}
	opened, err := f.Stat()
	if err != nil {
		return false
	}
	at, err := os.Lstat(p)
	return err == nil && os.SameFile(opened, at)
}

// Home returns os.UserHomeDir() in slash form.
func (LocalFS) Home(context.Context) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(home), nil
}
