package cli

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/testutil"
)

// TestMain points every directory Caddy, ssh or the config package might
// touch at a throwaway location before any test runs, so tests never read or
// write the real user's directories.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "nh-cli-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Setenv("XDG_DATA_HOME", dir)
	os.Setenv("XDG_CONFIG_HOME", dir)
	os.Setenv("HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// syncBuffer collects output that `up` writes while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	// broken makes every write fail, as writes to a pipe whose reader is gone.
	broken bool
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.broken {
		return 0, syscall.EPIPE
	}
	return b.buf.Write(p)
}

func (b *syncBuffer) breakPipe() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.broken = true
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// execute runs cmd under a root that has the shared --config flag and no
// other command, so nothing but cmd's own backend can be reached. Standard
// output and standard error are collected together.
func execute(t *testing.T, cmd *cobra.Command, args ...string) (string, error) {
	t.Helper()
	var out syncBuffer
	err := executeInto(context.Background(), &out, cmd, args...)
	return out.String(), err
}

func executeInto(ctx context.Context, out *syncBuffer, cmd *cobra.Command, args ...string) error {
	root := newRootCmd()
	root.AddCommand(cmd)
	root.SetOut(out)
	root.SetErr(out)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(args)
	return root.ExecuteContext(ctx)
}

// configHome gives the test its own short config directory (config file, run
// directory and Caddy storage live under it) and returns the config path. The
// prefix is as short as it is because a control socket path may be 86 bytes
// at most, and on macOS the temporary directory alone takes 49 of them and
// "/nearhost/run/mini.sock" another 23.
func configHome(t *testing.T) string {
	t.Helper()
	dir := testutil.TempDir(t, "nh")
	t.Setenv("XDG_CONFIG_HOME", dir)
	return filepath.Join(dir, "nearhost", "config.yaml")
}

func saveConfig(t *testing.T, path string, cfg *config.Config) {
	t.Helper()
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
}

func loadSaved(t *testing.T, path string) *config.Config {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "discovery", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func wantContains(t *testing.T, out string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if !strings.Contains(out, sub) {
			t.Errorf("output lacks %q:\n%s", sub, out)
		}
	}
}

func portOpen(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func TestNewRootRegistersEveryCommandAndTheConfigFlag(t *testing.T) {
	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"\n  add ", "\n  doctor ", "\n  hosts ", "\n  ls ", "\n  trust ", "\n  untrust ", "\n  up ", "\n  version ", "--config string"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help lacks %q:\n%s", want, out.String())
		}
	}
}

func TestConfigFlagOverridesTheDefaultPath(t *testing.T) {
	defaultPath := configHome(t)
	saveConfig(t, defaultPath, &config.Config{Projects: []config.Project{{
		Name: "fromdefault", Server: config.LocalServer, Alias: "fromdefault.test", Enabled: true,
		Routes: []config.Route{{Port: 8000, Role: config.RoleWeb}},
	}}})
	out, err := execute(t, newLsCmd(), "ls")
	if err != nil || !strings.Contains(out, "fromdefault") {
		t.Fatalf("ls with the default path: %v\n%s", err, out)
	}
	other := filepath.Join(t.TempDir(), "other.yaml")
	out, err = execute(t, newLsCmd(), "--config", other, "ls")
	if err != nil || strings.Contains(out, "fromdefault") || !strings.Contains(out, "No projects") {
		t.Fatalf("ls --config: %v\n%s", err, out)
	}
}

// A seam left nil would only show as a panic on a real machine, so check the
// production backends without calling anything in them.
func TestSystemBackendsLeaveNoSeamUnset(t *testing.T) {
	for name, backend := range map[string]any{"up": systemUp(), "add": systemAdd(), "doctor": systemDoctor(), "trust": systemTrust()} {
		v := reflect.ValueOf(backend)
		for i := 0; i < v.NumField(); i++ {
			if f := v.Field(i); f.Kind() == reflect.Func && f.IsNil() {
				t.Errorf("%s backend: %s is not set", name, v.Type().Field(i).Name)
			}
		}
	}
	if systemDoctor().goos == "" {
		t.Error("doctor backend: goos is not set")
	}
}
