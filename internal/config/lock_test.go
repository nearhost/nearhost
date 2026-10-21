package config

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
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
// told, one Update each.
func TestUpdateHelper(t *testing.T) {
	path := os.Getenv("NEARHOST_UPDATE_HELPER")
	if path == "" {
		t.Skip("run by TestUpdateSerialisesWriters")
	}
	n, _ := strconv.Atoi(os.Getenv("NEARHOST_UPDATE_COUNT"))
	for i := range n {
		if err := Update(path, appendProject(fmt.Sprintf("other-%d", i))); err != nil {
			t.Fatalf("Update: %v", err)
		}
	}
}

// Writers in this process and in another one each load, change and save the
// config through Update, and none of them loses another's change.
func TestUpdateSerialisesWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nearhost", "config.yaml")

	helper := exec.Command(os.Args[0], "-test.run=^TestUpdateHelper$")
	helper.Env = append(os.Environ(), "NEARHOST_UPDATE_HELPER="+path, "NEARHOST_UPDATE_COUNT=10")
	out := make(chan []byte, 1)
	errc := make(chan error, 1)
	go func() {
		b, err := helper.CombinedOutput()
		out <- b
		errc <- err
	}()

	var wg sync.WaitGroup
	errs := make([]error, 20)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = Update(path, appendProject(fmt.Sprintf("app-%d", i)))
		}()
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
