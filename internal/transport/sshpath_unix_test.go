//go:build unix

package transport

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeSSHIn writes an ssh in dir that prints word, and returns its path.
func fakeSSHIn(t *testing.T, dir, word string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ssh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho "+word+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSSHIsFoundOnce checks that the ssh found on PATH at first use is the
// one every later command runs, after PATH has changed to name another.
func TestSSHIsFoundOnce(t *testing.T) {
	resetSSHPath(t)
	first := fakeSSHIn(t, t.TempDir(), "first")
	t.Setenv("PATH", filepath.Dir(first))
	ctx := context.Background()
	if cmd := SSHCommand(ctx, "", nil, "-V"); cmd.Path != first || cmd.Err != nil {
		t.Fatalf("first command runs %q (err %v), want %q", cmd.Path, cmd.Err, first)
	}

	other := fakeSSHIn(t, t.TempDir(), "other")
	t.Setenv("PATH", filepath.Dir(other))
	cmd := SSHCommand(ctx, "", nil, "-V")
	if cmd.Path != first {
		t.Fatalf("after PATH changed the command runs %q, want %q", cmd.Path, first)
	}
	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) != "first" {
		t.Fatalf("ran %q, %v; want the first ssh", out, err)
	}
	for name, tr := range map[string]*exec.Cmd{
		"session":     NewSession("mini", "mini", "").command(ctx, "-V"),
		"per-process": newPerProcess(Config{Name: "mini", Host: "mini"}).command(ctx, "-V"),
	} {
		if tr.Path != first {
			t.Errorf("%s runs %q, want %q", name, tr.Path, first)
		}
	}
}

// TestSSHNotFoundIsNotCached checks that a failed lookup keeps today's error
// and that the next command looks again.
func TestSSHNotFoundIsNotCached(t *testing.T) {
	resetSSHPath(t)
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	want := exec.Command("ssh").Err
	if want == nil {
		t.Fatal("exec found an ssh on an empty PATH")
	}
	ctx := context.Background()
	err := SSHCommand(ctx, "", nil, "-V").Run()
	if err == nil || err.Error() != want.Error() {
		t.Fatalf("Run() = %v, want %v", err, want)
	}
	if !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("Run() = %v, want it to wrap exec.ErrNotFound", err)
	}

	installed := fakeSSHIn(t, dir, "installed")
	cmd := SSHCommand(ctx, "", nil, "-V")
	if cmd.Path != installed || cmd.Err != nil {
		t.Fatalf("after install the command runs %q (err %v), want %q", cmd.Path, cmd.Err, installed)
	}
}

// TestSSHFromARelativePATHEntryIsRefused checks that an ssh found through a
// relative PATH entry is not run, and not remembered.
func TestSSHFromARelativePATHEntryIsRefused(t *testing.T) {
	resetSSHPath(t)
	work := t.TempDir()
	t.Chdir(work)
	fakeSSHIn(t, filepath.Join(work, "bin"), "relative")
	t.Setenv("PATH", "bin")
	ctx := context.Background()
	err := SSHCommand(ctx, "", nil, "-V").Run()
	if !errors.Is(err, exec.ErrDot) {
		t.Fatalf("Run() = %v, want exec.ErrDot", err)
	}

	abs := fakeSSHIn(t, t.TempDir(), "absolute")
	t.Setenv("PATH", filepath.Dir(abs))
	if cmd := SSHCommand(ctx, "", nil, "-V"); cmd.Path != abs {
		t.Fatalf("command runs %q, want %q", cmd.Path, abs)
	}
}

// TestSSHFoundThroughASymlinkKeepsTheLink checks that the path found on PATH
// is run as it is, without following a symlink to its target.
func TestSSHFoundThroughASymlinkKeepsTheLink(t *testing.T) {
	resetSSHPath(t)
	target := fakeSSHIn(t, t.TempDir(), "target")
	bin := t.TempDir()
	link := filepath.Join(bin, "ssh")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if cmd := SSHCommand(context.Background(), "", nil, "-V"); cmd.Path != link {
		t.Fatalf("command runs %q, want the link %q", cmd.Path, link)
	}
}

// TestExplicitSSHPathIsUsedAsGiven checks that a given ssh path is run as it
// is and is not remembered for commands without one.
func TestExplicitSSHPathIsUsedAsGiven(t *testing.T) {
	resetSSHPath(t)
	explicit := fakeSSHIn(t, t.TempDir(), "explicit")
	onPath := fakeSSHIn(t, t.TempDir(), "path")
	t.Setenv("PATH", filepath.Dir(onPath))
	ctx := context.Background()
	if cmd := SSHCommand(ctx, explicit, nil, "-V"); cmd.Path != explicit {
		t.Fatalf("command runs %q, want %q", cmd.Path, explicit)
	}
	if cmd := SSHCommand(ctx, "", nil, "-V"); cmd.Path != onPath {
		t.Fatalf("command without a path runs %q, want %q", cmd.Path, onPath)
	}
	if cmd := SSHCommand(ctx, explicit, nil, "-V"); cmd.Path != explicit {
		t.Fatalf("command runs %q after the lookup, want %q", cmd.Path, explicit)
	}
	s := NewSession("mini", "mini", "")
	s.SSHPath = explicit
	if cmd := s.command(ctx, "-V"); cmd.Path != explicit {
		t.Errorf("session runs %q, want %q", cmd.Path, explicit)
	}
	if cmd := newPerProcess(Config{SSHPath: explicit}).command(ctx, "-V"); cmd.Path != explicit {
		t.Errorf("per-process runs %q, want %q", cmd.Path, explicit)
	}
}

// TestSSHIsFoundOnceAcrossGoroutines checks that commands built at once from
// several goroutines all get the same path.
func TestSSHIsFoundOnceAcrossGoroutines(t *testing.T) {
	resetSSHPath(t)
	want := fakeSSHIn(t, t.TempDir(), "one")
	t.Setenv("PATH", filepath.Dir(want))
	var wg sync.WaitGroup
	paths := make([]string, 16)
	for i := range paths {
		wg.Go(func() {
			paths[i] = SSHCommand(context.Background(), "", nil, "-V").Path
		})
	}
	wg.Wait()
	for i, got := range paths {
		if got != want {
			t.Errorf("goroutine %d runs %q, want %q", i, got, want)
		}
	}
}
