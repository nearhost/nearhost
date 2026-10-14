package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/preset"
	"github.com/nearhost/nearhost/internal/registry"
	"github.com/nearhost/nearhost/internal/testutil"
)

// TestMain points every directory Caddy, ssh or the config package might
// touch at a throwaway location before any test runs, so tests never read or
// write the real user's directories.
func TestMain(m *testing.M) {
	// The interface runs `nearhost add` as a child process. Tests point it
	// at this test binary, which then acts as nearhost.
	if os.Getenv("NEARHOST_TEST_AS_CLI") == "1" {
		os.Exit(Main(os.Args[1:]))
	}
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

// enablePresets copies the named test presets beside the config and enables
// them, as local presets.
func enablePresets(t *testing.T, cfgPath string, names ...string) {
	t.Helper()
	cfg := loadSaved(t, cfgPath)
	dir := filepath.Join(filepath.Dir(cfgPath), "presets")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join("..", "discovery", "testdata", "presets", name+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".yaml"), data, 0o644); err != nil {
			t.Fatal(err)
		}
		cfg.UpsertPreset(config.PresetRef{Name: name})
	}
	saveConfig(t, cfgPath, cfg)
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
	for _, want := range []string{"\n  add ", "\n  doctor ", "\n  hosts ", "\n  ls ", "\n  preset ", "\n  trust ", "\n  untrust ", "\n  up ", "\n  version ", "--config string"} {
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

// Registries the tests trust. The fake serves each at its repo.
var (
	testReg = config.Registry{Alias: "test", Repo: "test/presets"}
	acmeReg = config.Registry{Alias: "acme", Repo: "Acme/web-presets"}
)

// fakeRegistries serves preset registries from memory, each at /<repo>/ as
// raw.githubusercontent.com does, with the test presets of the discovery
// package. It counts requests by path and is safe for registries read at the
// same time.
type fakeRegistries struct {
	t     *testing.T
	mu    sync.Mutex
	files map[string]string // by path; change only while no command runs
	hits  map[string]int
	extra map[string]string // text added to the named preset when it is published
	down  map[string]bool   // repos that answer nothing
	held  map[string]string // repo whose answers wait for a request to another repo
	seen  map[string]chan struct{}
}

func newFakeRegistries(t *testing.T) (*fakeRegistries, presetBackend) {
	t.Helper()
	f := &fakeRegistries{t: t, files: map[string]string{}, hits: map[string]int{}, extra: map[string]string{}, down: map[string]bool{}, held: map[string]string{}, seen: map[string]chan struct{}{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		repo := strings.Join(strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 3)[:2], "/")
		f.mu.Lock()
		f.hits[r.URL.Path]++
		arrived := f.arrival(repo)
		select {
		case <-arrived:
		default:
			close(arrived)
		}
		var wait chan struct{}
		if other, ok := f.held[repo]; ok {
			wait = f.arrival(other)
		}
		body, ok := f.files[r.URL.Path]
		down := f.down[repo]
		f.mu.Unlock()
		if wait != nil {
			select {
			case <-wait:
			case <-r.Context().Done():
				return
			}
		}
		if down || !ok {
			http.Error(w, "nope", http.StatusNotFound)
			return
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return f, presetBackend{registry: func(reg config.Registry, dir string) *registry.Client {
		return &registry.Client{BaseURL: srv.URL + "/" + reg.Repo, Dir: dir, HTTP: srv.Client(), Now: time.Now}
	}}
}

// arrival is closed once a request for repo has arrived. f.mu must be held.
func (f *fakeRegistries) arrival(repo string) chan struct{} {
	if f.seen[repo] == nil {
		f.seen[repo] = make(chan struct{})
	}
	return f.seen[repo]
}

// hold makes the answers of repo wait until a request for other has arrived,
// or until the client gives up.
func (f *fakeRegistries) hold(repo, other string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.held[repo] = other
}

// publish replaces what repo serves with the named test presets at ref.
func (f *fakeRegistries) publish(repo, ref string, names ...string) {
	f.t.Helper()
	idx := registry.Index{Schema: registry.IndexSchema, Ref: ref}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join("..", "discovery", "testdata", "presets", name+".yaml"))
		if err != nil {
			f.t.Fatal(err)
		}
		data = append(data, f.extra[name]...)
		p, err := preset.Parse(name, data)
		if err != nil {
			f.t.Fatal(err)
		}
		f.files["/"+repo+"/"+ref+"/presets/"+name+".yaml"] = string(data)
		idx.Presets = append(idx.Presets, registry.Entry{Name: name, Title: p.Title, PresetSchema: p.Schema, Detect: p.Detect, SHA256: registry.Sum(data)})
	}
	out, err := yaml.Marshal(idx)
	if err != nil {
		f.t.Fatal(err)
	}
	f.files["/"+repo+"/HEAD/index.yaml"] = string(out)
}

// hit is how many requests path has had.
func (f *fakeRegistries) hit(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[path]
}

// requests is how many requests repo has had.
func (f *fakeRegistries) requests(repo string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for path, hits := range f.hits {
		if strings.HasPrefix(path, "/"+repo+"/") {
			n += hits
		}
	}
	return n
}

// total is how many requests every registry has had together.
func (f *fakeRegistries) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, hits := range f.hits {
		n += hits
	}
	return n
}

// trust adds registries to the config at cfgPath.
func trust(t *testing.T, cfgPath string, regs ...config.Registry) {
	t.Helper()
	cfg := loadSaved(t, cfgPath)
	cfg.Registries = append(cfg.Registries, regs...)
	saveConfig(t, cfgPath, cfg)
}

// executeWithInput runs a command with input waiting on its standard input.
func executeWithInput(t *testing.T, input string, cmd *cobra.Command, args ...string) (string, error) {
	t.Helper()
	var out syncBuffer
	root := newRootCmd()
	root.AddCommand(cmd)
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(input))
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return out.String(), err
}
