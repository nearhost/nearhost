package config

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// appendProject is the change each writer in TestUpdateSerialisesWriters
// makes: it adds a project of its own, so a save that undid another's would
// leave that one's project out.
func appendProject(name string) func(*Config) error {
	return func(c *Config) error {
		c.Projects = append(c.Projects, Project{
			Name: name, Server: LocalServer, Path: "/srv/" + name, Alias: name + ".test", Enabled: true,
			Routes: []Route{{Port: 8000, Role: RoleWeb}},
		})
		return nil
	}
}

// TestUpdateHelper is not a test: run by TestUpdateSerialisesWriters as a
// second process, it appends the projects it is told to the config it is
// told, one Update each. In its first it creates the file held and waits for
// the file release, so the test can run its own writers while it holds the
// lock.
func TestUpdateHelper(t *testing.T) {
	path := os.Getenv("NEARHOST_UPDATE_HELPER")
	if path == "" {
		t.Skip("run by TestUpdateSerialisesWriters")
	}
	dir := os.Getenv("NEARHOST_UPDATE_SIGNALS")
	if err := Update(path, func(c *Config) error {
		if err := os.WriteFile(filepath.Join(dir, "held"), nil, 0o600); err != nil {
			return err
		}
		for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			if _, err := os.Stat(filepath.Join(dir, "release")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				return errors.New("never released")
			}
		}
		return appendProject("other-held")(c)
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	n, _ := strconv.Atoi(os.Getenv("NEARHOST_UPDATE_COUNT"))
	for i := range n {
		if err := Update(path, appendProject(fmt.Sprintf("other-%d", i))); err != nil {
			t.Fatalf("Update: %v", err)
		}
	}
}

// Writers in this process and in another one each load, change and save the
// config through Update, and none of them loses another's change. The local
// writers start while the other process holds the lock, and none of their
// changes runs until it lets go.
func TestUpdateSerialisesWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nearhost", "config.yaml")
	signals := t.TempDir()

	helper := exec.Command(os.Args[0], "-test.run=^TestUpdateHelper$")
	helper.Env = append(os.Environ(), "NEARHOST_UPDATE_HELPER="+path, "NEARHOST_UPDATE_COUNT=10", "NEARHOST_UPDATE_SIGNALS="+signals)
	out := make(chan []byte, 1)
	errc := make(chan error, 1)
	go func() {
		b, err := helper.CombinedOutput()
		out <- b
		errc <- err
	}()
	held := make(chan struct{})
	go func() {
		for {
			if _, err := os.Stat(filepath.Join(signals, "held")); err == nil {
				close(held)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	select {
	case <-held:
	case <-time.After(30 * time.Second):
		t.Fatal("the second process never took the lock")
	}

	var holding atomic.Bool
	holding.Store(true)
	ranEarly := make(chan struct{}, 20)
	var wg sync.WaitGroup
	errs := make([]error, 20)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			add := appendProject(fmt.Sprintf("app-%d", i))
			errs[i] = Update(path, func(c *Config) error {
				if holding.Load() {
					ranEarly <- struct{}{}
				}
				return add(c)
			})
		}()
	}
	// While the other process holds the lock, no local change may run.
	select {
	case <-ranEarly:
		t.Error("a local change ran while the other process held the lock")
	case <-time.After(200 * time.Millisecond):
	}
	holding.Store(false)
	if err := os.WriteFile(filepath.Join(signals, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if b, err := <-out, <-errc; err != nil {
		t.Fatalf("second process: %v\n%s", err, b)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for i := range 20 {
		want = append(want, fmt.Sprintf("app-%d", i))
	}
	for i := range 10 {
		want = append(want, fmt.Sprintf("other-%d", i))
	}
	want = append(want, "other-held")
	for _, name := range want {
		if _, ok := cfg.Project(name); !ok {
			t.Errorf("project %s was lost", name)
		}
	}
	if len(cfg.Projects) != len(want) {
		t.Errorf("got %d projects, want %d", len(cfg.Projects), len(want))
	}
}

// A change that fails, or leaves the config invalid, saves nothing.
func TestUpdateSavesNothingOnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Update(path, appendProject("myapp")); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	if err := Update(path, func(c *Config) error {
		c.Projects = nil
		return boom
	}); !errors.Is(err, boom) {
		t.Errorf("Update = %v, want %v", err, boom)
	}
	if err := Update(path, appendProject("myapp")); err == nil {
		t.Error("Update saved a duplicate project")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("config changed:\n%s\nwant\n%s", after, before)
	}
	info, err := os.Stat(path + ".lock")
	if err != nil {
		t.Fatalf("lock file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("lock file mode = %v, want 0600", perm)
	}
}

// A lock file left with a wider mode is narrowed to 0600.
func TestUpdateNarrowsTheLockFileMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path+".lock", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path+".lock", 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Update(path, appendProject("myapp")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("lock file mode = %v, want 0600", perm)
	}
}
