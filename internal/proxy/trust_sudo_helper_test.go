package proxy

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/nearhost/nearhost/internal/sudo"
)

// Shared by trust_sudo_test.go (Unix only) and keychain_test.go (every OS).

// fakeTrustSudo makes a root-owned-looking sudo in its own directory the one
// sudo.Binary finds, puts a fake sudo ahead of it on PATH, and returns the
// trusted path.
func fakeTrustSudo(t *testing.T) string {
	t.Helper()
	trusted := filepath.Join(t.TempDir(), "sudo")
	fake := filepath.Join(t.TempDir(), "sudo")
	for _, p := range []string{trusted, fake} {
		if err := os.WriteFile(p, nil, 0o755); err != nil {
			t.Fatal(err)
		}
		// The umask can take the execute bits WriteFile asks for.
		if err := os.Chmod(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	origCandidates, origOwned := sudo.Candidates, sudo.OwnedByRoot
	sudo.Candidates = []string{trusted}
	sudo.OwnedByRoot = func(fs.FileInfo) bool { return true }
	t.Cleanup(func() { sudo.Candidates, sudo.OwnedByRoot = origCandidates, origOwned })
	t.Setenv("PATH", filepath.Dir(fake)+string(os.PathListSeparator)+os.Getenv("PATH"))
	return trusted
}
