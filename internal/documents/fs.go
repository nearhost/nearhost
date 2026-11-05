package documents

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// FS is the file system of the server a tree is on: the laptop's own, or a
// server's reached over SFTP. A missing file's error satisfies
// errors.Is(err, fs.ErrNotExist).
type FS interface {
	// RealPath returns p with every symlink resolved.
	RealPath(ctx context.Context, p string) (string, error)
	// Stat returns the file information for p, following symlinks.
	Stat(ctx context.Context, p string) (fs.FileInfo, error)
	// Open opens p for reading.
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

// Open returns os.Open(p).
func (LocalFS) Open(_ context.Context, p string) (io.ReadCloser, error) {
	return os.Open(p)
}

// Home returns os.UserHomeDir() in slash form.
func (LocalFS) Home(context.Context) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(home), nil
}
