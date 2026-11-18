// Package discovery finds a project's ports from its configuration files.
package discovery

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/nearhost/nearhost/internal/oserr"
)

// MaxProjectFile is the most a FileReader reads of one project file. The
// files come with a cloned repository or sit on a server, so one that is
// larger, or a link to a device that never ends, must not fill memory.
const MaxProjectFile = 1 << 20

// FileReader reads files relative to a project root. Paths are slash
// separated. A missing file must return an error satisfying
// errors.Is(err, fs.ErrNotExist). ReadFile reads a regular file of at most
// MaxProjectFile bytes and refuses anything else.
type FileReader interface {
	ReadFile(name string) ([]byte, error)
	// Exists reports whether name is a regular file, following links,
	// without reading it. A missing file, a directory and a path under a
	// regular file give false and no error.
	Exists(name string) (bool, error)
}

// CheckFile returns an error when info, the stat of the project file name,
// is not a regular file of at most MaxProjectFile bytes. A directory gives
// an error that isAbsent counts as no file, as reading one always has.
func CheckFile(name string, info fs.FileInfo) error {
	switch {
	case info.IsDir():
		return &fs.PathError{Op: "read", Path: name, Err: syscall.EISDIR}
	case !info.Mode().IsRegular():
		return &FileError{Name: name, Problem: "is not a regular file"}
	case info.Size() > MaxProjectFile:
		return tooLarge(name)
	}
	return nil
}

// ReadLimited reads r, the opened project file name, and fails when it holds
// more than MaxProjectFile bytes, whatever its stat said.
func ReadLimited(name string, r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxProjectFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxProjectFile {
		return nil, tooLarge(name)
	}
	return data, nil
}

func tooLarge(name string) error {
	return &FileError{Name: name, Problem: "is larger than 1 MiB"}
}

// FileError is a project file a FileReader refuses to read. Its message
// names the file, so ReadError does not name it again.
type FileError struct {
	Name    string // the file, relative to the project
	Problem string // what is wrong with it, such as "is not a regular file"
}

func (e *FileError) Error() string { return e.Name + " " + e.Problem }

// ReadError is err, from reading the project file name, with the file named
// once: a FileError for name already says which file, and any other error
// is put as "read <name>: <err>".
func ReadError(name string, err error) error {
	var fe *FileError
	if errors.As(err, &fe) && fe.Name == name {
		return err
	}
	return fmt.Errorf("read %s: %w", name, err)
}

// DirReader reads a project on the local filesystem.
type DirReader struct {
	Root string
}

// ReadFile implements FileReader. A link is followed. The file is checked
// before it is opened, so a FIFO is not opened and left waiting for a
// writer, and again through the open handle, so a file swapped in between
// is caught too.
func (d DirReader) ReadFile(name string) ([]byte, error) {
	p := d.path(name)
	info, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if err := CheckFile(name, info); err != nil {
		return nil, err
	}
	// A FIFO put in place between the stat and here would still block this
	// open; the check of the handle below catches every other swap. Opening
	// without blocking needs O_NONBLOCK, which only Unix has.
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info, err = f.Stat(); err != nil {
		return nil, err
	}
	if err := CheckFile(name, info); err != nil {
		return nil, err
	}
	return ReadLimited(name, f)
}

// Exists implements FileReader.
func (d DirReader) Exists(name string) (bool, error) {
	info, err := os.Stat(d.path(name))
	if err != nil {
		if isAbsent(err) {
			return false, nil
		}
		return false, err
	}
	return info.Mode().IsRegular(), nil
}

func (d DirReader) path(name string) string {
	return filepath.Join(d.Root, filepath.FromSlash(name))
}

// memo reads each file once per discovery run. It remembers the first
// failure that was not a missing file, so that a file a preset could not
// read fails the run instead of looking absent. A failure is remembered
// again when a cached failed read is asked for once more.
type memo struct {
	r      FileReader
	files  map[string]memoFile
	exists map[string]memoExists
	err    error
}

type memoFile struct {
	data []byte
	err  error
}

type memoExists struct {
	ok  bool
	err error
}

func newMemo(r FileReader) *memo {
	return &memo{r: r, files: map[string]memoFile{}, exists: map[string]memoExists{}}
}

// ReadFile implements FileReader.
func (m *memo) ReadFile(name string) ([]byte, error) {
	f, ok := m.files[name]
	if !ok {
		data, err := m.r.ReadFile(name)
		f = memoFile{data, err}
		m.files[name] = f
	}
	m.fail(name, f.err)
	return f.data, f.err
}

// Exists implements FileReader. A failure fails the run as a failed read
// does.
func (m *memo) Exists(name string) (bool, error) {
	e, ok := m.exists[name]
	if !ok {
		found, err := m.r.Exists(name)
		e = memoExists{found, err}
		m.exists[name] = e
	}
	m.fail(name, e.err)
	return e.ok, e.err
}

// fail remembers err, from reading name, unless it means the file is absent
// or a failure is already remembered.
func (m *memo) fail(name string, err error) {
	if err != nil && !isAbsent(err) && m.err == nil {
		m.err = ReadError(name, err)
	}
}

// isAbsent reports whether err means the file is not there: missing, a
// directory, or under a parent that is a regular file.
func isAbsent(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || oserr.NotAFile(err)
}
