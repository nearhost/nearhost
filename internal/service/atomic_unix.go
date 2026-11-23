//go:build darwin || linux

package service

import (
	"os"
	"path/filepath"

	"github.com/nearhost/nearhost/internal/config"
)

// writeFileAtomic writes b to path, creating its folder, the way the config
// is written: through a synced temporary file and a rename, so a reader or a
// crash never sees half a file, and through a link, so a service file that
// is a link stays one.
func writeFileAtomic(path string, b []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return config.WriteFileAtomic(path, b, perm)
}
