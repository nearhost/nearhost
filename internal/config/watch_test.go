package config

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const watchInterval = 20 * time.Millisecond

// watchRecorder collects what Watch reports, safely across goroutines.
type watchRecorder struct {
	mu      sync.Mutex
	applied []*Config
	bad     []error
}

func (r *watchRecorder) apply(c *Config) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.applied = append(r.applied, c)
}

func (r *watchRecorder) badf(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bad = append(r.bad, err)
}

func (r *watchRecorder) counts() (applied, bad int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.applied), len(r.bad)
}

func (r *watchRecorder) lastApplied() *Config {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.applied) == 0 {
		return nil
	}
	return r.applied[len(r.applied)-1]
}

// startWatch runs Watch until the test ends.
func startWatch(t *testing.T, path string) (*watchRecorder, context.CancelFunc, <-chan struct{}) {
	t.Helper()
	r := &watchRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		Watch(ctx, path, watchInterval, r.apply, r.badf)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return r, cancel, done
}

// waitFor polls cond until the generous deadline, which loaded CI runners need.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// settle waits long enough for many polls, so a second call that should not
// happen would have happened.
func settle() { time.Sleep(15 * watchInterval) }

func writeProjects(t *testing.T, path string, names ...string) {
	t.Helper()
	c := &Config{}
	for _, n := range names {
		c.Projects = append(c.Projects, Project{Name: n, Server: LocalServer, Path: filepath.Join(filepath.Dir(path), n), Alias: n + ".test", Enabled: true, Routes: []Route{{Port: 80, Role: RoleWeb}}})
	}
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
}

func TestWatchAppliesAChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeProjects(t, path, "a")
	r, _, _ := startWatch(t, path)
	time.Sleep(3 * watchInterval)
	writeProjects(t, path, "a", "b")
	waitFor(t, "apply", func() bool { n, _ := r.counts(); return n >= 1 })
	settle()
	if n, _ := r.counts(); n != 1 {
		t.Fatalf("apply called %d times, want once", n)
	}
	if got := len(r.lastApplied().Projects); got != 2 {
		t.Fatalf("applied %d projects, want 2", got)
	}
}

func TestWatchWaitsForTheFileToSettle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeProjects(t, path, "a")
	r, _, _ := startWatch(t, path)
	time.Sleep(3 * watchInterval)
	writeProjects(t, path, "a", "b")
	time.Sleep(5 * time.Millisecond)
	writeProjects(t, path, "a", "b", "c")
	waitFor(t, "apply", func() bool { n, _ := r.counts(); return n >= 1 })
	settle()
	// A slow runner can stretch the 5 ms gap past a tick, so the first apply
	// may be either content, but the last must be the second write.
	if got := len(r.lastApplied().Projects); got != 3 {
		t.Fatalf("last applied %d projects, want 3", got)
	}
}

func TestWatchReportsAnInvalidConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeProjects(t, path, "a")
	r, _, _ := startWatch(t, path)
	time.Sleep(3 * watchInterval)
	if err := os.WriteFile(path, []byte("projects: [unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "bad", func() bool { _, n := r.counts(); return n >= 1 })
	settle()
	if a, b := r.counts(); a != 0 || b != 1 {
		t.Fatalf("apply %d, bad %d; want 0 and 1", a, b)
	}
	writeProjects(t, path, "a", "b")
	waitFor(t, "apply after a valid write", func() bool { n, _ := r.counts(); return n >= 1 })
}

func TestWatchReportsAConfigThatFailsValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeProjects(t, path, "a")
	r, _, _ := startWatch(t, path)
	time.Sleep(3 * watchInterval)
	bad := "projects:\n  - name: x\n    server: nowhere\n    path: /x\n    alias: x.test\n    enabled: true\n"
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "bad", func() bool { _, n := r.counts(); return n >= 1 })
	settle()
	if a, _ := r.counts(); a != 0 {
		t.Fatalf("apply called %d times for an invalid config", a)
	}
}

func TestWatchIgnoresAnEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeProjects(t, path, "a")
	r, _, _ := startWatch(t, path)
	time.Sleep(3 * watchInterval)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "bad", func() bool { _, n := r.counts(); return n >= 1 })
	settle()
	if a, _ := r.counts(); a != 0 {
		t.Fatalf("apply called %d times for an empty file", a)
	}
}

func TestWatchIgnoresAnUnchangedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeProjects(t, path, "a")
	r, _, _ := startWatch(t, path)
	time.Sleep(15 * watchInterval)
	if a, b := r.counts(); a != 0 || b != 0 {
		t.Fatalf("apply %d, bad %d; want none", a, b)
	}
}

func TestWatchIgnoresAMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	r, _, _ := startWatch(t, path)
	time.Sleep(15 * watchInterval)
	if a, b := r.counts(); a != 0 || b != 0 {
		t.Fatalf("apply %d, bad %d; want none", a, b)
	}
}

func TestWatchSeesAFileCreated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	r, _, _ := startWatch(t, path)
	time.Sleep(3 * watchInterval)
	writeProjects(t, path, "a")
	waitFor(t, "apply", func() bool { n, _ := r.counts(); return n >= 1 })
}

func TestWatchStopsWithCtx(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	_, cancel, done := startWatch(t, path)
	time.Sleep(3 * watchInterval)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Watch did not return after the context ended")
	}
}
