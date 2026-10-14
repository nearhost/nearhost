// Package discovery finds a project's ports from its configuration files.
package discovery

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// FileReader reads files relative to a project root. Paths are slash
// separated. A missing file must return an error satisfying
// errors.Is(err, fs.ErrNotExist).
type FileReader interface {
	ReadFile(name string) ([]byte, error)
}

// DirReader reads a project on the local filesystem.
type DirReader struct {
	Root string
}

// ReadFile implements FileReader.
func (d DirReader) ReadFile(name string) ([]byte, error) {
	return os.ReadFile(filepath.Join(d.Root, filepath.FromSlash(name)))
}

// memo reads each file once per discovery run. It remembers the first
// failure that was not a missing file, so that a file a preset could not
// read fails the run instead of looking absent.
type memo struct {
	r     FileReader
	files map[string]memoFile
	err   error
}

type memoFile struct {
	data []byte
	err  error
}

func newMemo(r FileReader) *memo {
	return &memo{r: r, files: map[string]memoFile{}}
}

// ReadFile implements FileReader.
func (m *memo) ReadFile(name string) ([]byte, error) {
	if f, ok := m.files[name]; ok {
		return f.data, f.err
	}
	data, err := m.r.ReadFile(name)
	m.files[name] = memoFile{data, err}
	if err != nil && !errors.Is(err, fs.ErrNotExist) && m.err == nil {
		m.err = fmt.Errorf("read %s: %w", name, err)
	}
	return data, err
}
