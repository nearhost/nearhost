// Package discovery finds a project's ports from its configuration files.
package discovery

import (
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
