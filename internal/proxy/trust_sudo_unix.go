//go:build !windows

package proxy

import (
	"os"
	"path/filepath"

	"github.com/nearhost/nearhost/internal/elevate"
	"github.com/nearhost/nearhost/internal/sudo"
)

// withTrustedSudo runs fn with the directory of sudo.Binary first on PATH.
// The trust store library runs plain "sudo", which asks for the password on
// the terminal, so a fake sudo earlier on PATH would be handed it. Without a
// root-owned sudo it refuses, unless Nearhost runs as root. PATH is
// process-wide, which is fine for the one-shot trust and untrust commands,
// the only callers.
func withTrustedSudo(fn func() error) error {
	path, err := sudo.Binary()
	if err != nil {
		if elevate.Elevated() {
			return fn()
		}
		return err
	}
	orig, had := os.LookupEnv("PATH")
	// An empty entry means the current directory, so a missing PATH
	// becomes sudo's directory alone.
	next := filepath.Dir(path)
	if orig != "" {
		next += string(os.PathListSeparator) + orig
	}
	if err := os.Setenv("PATH", next); err != nil {
		return err
	}
	defer func() {
		if had {
			os.Setenv("PATH", orig)
		} else {
			os.Unsetenv("PATH")
		}
	}()
	return fn()
}
