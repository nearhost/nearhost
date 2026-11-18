//go:build unix

package cli

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/sudo"
	"github.com/nearhost/nearhost/internal/tui"
)

// slowSudo points sudo at the stand-in in internal/sudo/testdata, with secret
// as the password, and makes its next validation slow to leave once it is
// asked to terminate. It returns the file that means the credentials are
// cached, and the file that appears once that slow validation waits.
func slowSudo(t *testing.T) (cache, waiting string) {
	t.Helper()
	src, err := os.ReadFile("../sudo/testdata/sudo")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "sudo")
	if err := os.WriteFile(p, src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}
	origCandidates, origOwned := sudo.Candidates, sudo.OwnedByRoot
	sudo.Candidates = []string{p}
	sudo.OwnedByRoot = func(fs.FileInfo) bool { return true }
	t.Cleanup(func() { sudo.Candidates, sudo.OwnedByRoot = origCandidates, origOwned })
	slow := filepath.Join(dir, "slow")
	if err := os.WriteFile(slow, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cache = filepath.Join(dir, "cache")
	t.Setenv("FAKE_SUDO_PASSWORD", "secret")
	t.Setenv("FAKE_SUDO_CACHE", cache)
	t.Setenv("FAKE_SUDO_SLOW", slow)
	return cache, slow + ".waiting"
}

// waitFor waits up to 10 s for the file at path to exist.
func waitFor(t *testing.T, path string) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
	t.Fatalf("%s never appeared", path)
}

// A check the user cancelled while sudo was slow to leave cannot drop the
// credentials the next step's password cached: the next step runs with
// them still cached after the cancelled check, and the sudo -k it runs as
// it ends, have finished.
func TestAsRootCancelledCheckKeepsTheNextStepsCredentials(t *testing.T) {
	_, waiting := slowSudo(t)
	firstEnded := make(chan struct{})
	root := asRoot{
		sudo: rootBackend{
			needed:        func() bool { return true },
			takesPassword: func() bool { return true },
			cached:        sudo.Cached,
			validate:      sudo.Validate,
			forget:        sudo.Forget,
		},
		width: func() int { return 60 },
		turn:  make(chan struct{}, 1),
		suspend: func(context.Context, func(io.Reader, io.Writer) error) error {
			return errors.New("the step was sent to the terminal")
		},
		ask: func(ctx context.Context, why string, check func(context.Context, string) error) error {
			if why != "First." {
				return check(ctx, "secret")
			}
			// As the password box does: the check runs on the side, and
			// esc ends it and answers at once.
			checkCtx, cancel := context.WithCancel(ctx)
			go func() {
				_ = check(checkCtx, "secret")
				close(firstEnded)
			}()
			waitFor(t, waiting)
			cancel()
			return tui.ErrPasswordCancelled
		},
	}
	if err := root.run(context.Background(), "First.", "Done.", (&step{}).run); !errors.Is(err, tui.ErrPasswordCancelled) {
		t.Fatalf("first step: %v", err)
	}
	var cachedAfter bool
	err := root.run(context.Background(), "Second.", "Done.", func(ctx context.Context, terminal io.Writer) error {
		select {
		case <-firstEnded:
		case <-time.After(10 * time.Second):
			return errors.New("the cancelled check never ended")
		}
		cachedAfter = sudo.Cached(ctx)
		return nil
	})
	if err != nil {
		t.Fatalf("second step: %v", err)
	}
	if !cachedAfter {
		t.Error("the cancelled check's sudo -k dropped the credentials the second step cached")
	}
}
