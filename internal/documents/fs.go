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
	// root, for reading. It returns the path below root of the file it
	// actually opened, which differs from p's when a folder on the way was
	// swapped for a symlink after p was resolved, so the caller can apply
	// its rules to that. It gives an error wrapping fs.ErrNotExist when the
	// opened file isn't below root.
	Open(ctx context.Context, root, p string) (io.ReadCloser, string, error)
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
// the system names the open file. The folder root is opened as well and
// named the same way, so both paths share one spelling of drives, links,
// case and Unicode form. Where the system can't name an open file, the file
// at p, which must still resolve to itself, must be the opened one.
func (LocalFS) Open(_ context.Context, root, p string) (io.ReadCloser, string, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, "", err
	}
	rel, err := openedBelowRoot(f, root, p)
	if err != nil {
		f.Close()
		return nil, "", err
	}
	return f, rel, nil
}

// openedBelowRoot returns the path below root of the open file f, which was
// opened by the path p, or an error wrapping fs.ErrNotExist when f isn't
// below root.
func openedBelowRoot(f *os.File, root, p string) (string, error) {
	missing := &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
	opened, err := openedPath(f)
	if err != nil {
		if !sameFileAt(f, p) {
			return "", missing
		}
		rel, ok := Below(root, filepath.ToSlash(p))
		if !ok {
			return "", missing
		}
		return rel, nil
	}
	rootPath, ok := openedRoot(root)
	if !ok {
		return "", missing
	}
	rel, ok := openedBelow(runtime.GOOS, rootPath, opened)
	if !ok {
		return "", missing
	}
	return rel, nil
}

// openedRoot opens the folder root and returns the path the system gives for
// it. When that path spells root another way, as a subst drive or a firmlink
// does, root must still resolve to itself and be the opened folder. ok is
// false when it isn't.
func openedRoot(root string) (string, bool) {
	d, err := os.Open(root)
	if err != nil {
		return "", false
	}
	defer d.Close()
	opened, err := openedPath(d)
	if err != nil {
		return root, sameFileAt(d, root)
	}
	switch sameSpelling(runtime.GOOS, opened, root) {
	case matchSame:
		return opened, true
	case matchUnsure:
		return opened, sameFileAt(d, root)
	}
	return "", false
}

// openedMatch is what the path the system gives for an open folder says
// about whether it is the folder at the path it was opened by.
type openedMatch int

// The answers of sameSpelling.
const (
	// matchSame: the open folder is the one at the path.
	matchSame openedMatch = iota
	// matchDiffers: the open folder is elsewhere.
	matchDiffers
	// matchUnsure: the paths differ in a way the same folder's can, so
	// the folder's identity decides.
	matchUnsure
)

// sameSpelling compares opened, the path goos gives for an open folder, with
// checked, the slash-form path it was opened by. Linux names an open file by
// the path it resolves to, so any difference means another folder. macOS's
// may differ in case, Unicode form or a firmlink, and Windows' in case or
// the drive of a subst or mapped one.
func sameSpelling(goos, opened, checked string) openedMatch {
	opened = slashed(goos, opened)
	switch {
	case opened == checked, goos == "windows" && strings.EqualFold(opened, checked):
		return matchSame
	case goos == "darwin", goos == "windows":
		return matchUnsure
	}
	return matchDiffers
}

// openedBelow returns the path of file below root, where both are paths goos
// gives for open handles, in one spelling. Windows compares without case, as
// its file systems do. ok is false when file isn't below root or a segment
// of the path below starts with ".".
func openedBelow(goos, root, file string) (rel string, ok bool) {
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
// f. It is the check where the system can't name an open file, and it can be
// beaten by a folder on the way that is swapped back and forth between its
// two steps.
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
