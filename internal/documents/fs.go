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
	// Open opens p, a path RealPath returned, for reading. It then checks
	// that the file it opened is the one at p, and gives an error wrapping
	// fs.ErrNotExist when it isn't, as when a folder on the way was swapped
	// for a symlink after p was resolved.
	Open(ctx context.Context, p string) (io.ReadCloser, error)
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

// Open opens p and checks the opened file is the one at p. The path the
// system gives for the open file decides where it can: on Linux always, and
// on macOS and Windows when it spells p the same way. Otherwise the file's
// identity is compared with the file at p, which must still resolve to
// itself.
func (LocalFS) Open(_ context.Context, p string) (io.ReadCloser, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		f.Close()
		return nil, err
	}
	m := matchUnsure
	if opened, err := openedPath(f); err == nil {
		m = compareOpened(runtime.GOOS, opened, filepath.ToSlash(abs))
	}
	if m == matchUnsure && sameFileAt(f, abs) {
		m = matchSame
	}
	if m != matchSame {
		f.Close()
		return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
	}
	return f, nil
}

// openedMatch is what the path of an open file says about whether it is the
// file at the path that was checked.
type openedMatch int

// The answers of compareOpened.
const (
	// matchSame: the open file is the one at the checked path.
	matchSame openedMatch = iota
	// matchDiffers: the open file is elsewhere.
	matchDiffers
	// matchUnsure: the paths differ in a way the same file can, so the
	// file's identity decides.
	matchUnsure
)

// compareOpened compares opened, the path goos gives for an open file, with
// checked, the slash-form path it was opened by. Linux's path is the file's
// own, so any difference means another file. macOS's may differ in case or
// Unicode normalization, which its file systems ignore by default. Windows'
// may name another drive for a subst or mapped one.
func compareOpened(goos, opened, checked string) openedMatch {
	if goos == "windows" {
		opened = strings.ReplaceAll(cleanFinalPath(opened), `\`, "/")
	}
	switch {
	case opened == checked:
		return matchSame
	case goos == "windows":
		return matchUnsure
	case goos == "darwin" && (strings.EqualFold(opened, checked) || !isASCII(opened) || !isASCII(checked)):
		return matchUnsure
	}
	return matchDiffers
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

// isASCII reports whether s is all ASCII.
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// sameFileAt reports whether the open file f is the one at p now: p still
// resolves to itself, and the file there, not following a last symlink, is
// f.
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
