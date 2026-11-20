package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/hosts"
	"github.com/nearhost/nearhost/internal/testutil"
)

// updateHarness points `hosts update` at a hosts file of its own, with an
// applier that records what it is asked to write instead of elevating.
type updateHarness struct {
	t        *testing.T
	cfgPath  string
	hostsTo  string
	mu       sync.Mutex
	applied  [][]string
	terminal bool
	// applyErr, when set, is what the applier returns.
	applyErr error
}

func newUpdateHarness(t *testing.T, cfg *config.Config, existing string) *updateHarness {
	t.Helper()
	h := &updateHarness{t: t, cfgPath: configHome(t), terminal: true}
	saveConfig(t, h.cfgPath, cfg)
	h.hostsTo = filepath.Join(testutil.TempDir(t, "hosts"), "hosts")
	if err := os.WriteFile(h.hostsTo, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	origPath, origApplier, origTerm := hosts.DefaultPath, newHostsUpdateApplier, stdinIsTerminal
	t.Cleanup(func() {
		hosts.DefaultPath, newHostsUpdateApplier, stdinIsTerminal = origPath, origApplier, origTerm
	})
	hosts.DefaultPath = h.hostsTo
	stdinIsTerminal = func() bool { return h.terminal }
	newHostsUpdateApplier = func(_ context.Context, terminal bool) hosts.Applier {
		return func(aliases []string) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.applied = append(h.applied, slices.Clone(aliases))
			if h.applyErr != nil {
				return h.applyErr
			}
			return hosts.Apply(h.hostsTo, aliases)
		}
	}
	return h
}

func (h *updateHarness) run() (string, error) {
	h.t.Helper()
	var out syncBuffer
	err := executeInto(context.Background(), &out, newHostsCmd(nil), "hosts", "update", "--config", h.cfgPath)
	return out.String(), err
}

func updateConfig() *config.Config {
	return &config.Config{Projects: []config.Project{
		localProject("one", 3000, true),
		localProject("off", 3001, false),
	}}
}

func TestHostsUpdateWritesTheBlock(t *testing.T) {
	h := newUpdateHarness(t, updateConfig(), "127.0.0.1 localhost\n")
	out, err := h.run()
	if err != nil {
		t.Fatalf("hosts update: %v\n%s", err, out)
	}
	if want := "Updated " + h.hostsTo + ".\n"; out != want {
		t.Errorf("printed %q, want %q", out, want)
	}
	if len(h.applied) != 1 {
		t.Fatalf("applied %v, want one write", h.applied)
	}
	want := hosts.Wanted(context.Background(), loadSaved(t, h.cfgPath).HostsFileNames(), nil)
	for _, a := range want {
		if !slices.Contains(h.applied[0], a) {
			t.Errorf("the block lacks %q: %v", a, h.applied[0])
		}
	}
	if !slices.Contains(h.applied[0], "one.test") {
		t.Errorf("block %v, want one.test", h.applied[0])
	}
	if b, _ := os.ReadFile(h.hostsTo); !strings.Contains(string(b), "one.test") {
		t.Errorf("the file reads:\n%s", b)
	}
}

func TestHostsUpdateUpToDate(t *testing.T) {
	h := newUpdateHarness(t, updateConfig(), "127.0.0.1 localhost\n")
	if _, err := h.run(); err != nil {
		t.Fatal(err)
	}
	h.applied = nil
	out, err := h.run()
	if err != nil {
		t.Fatal(err)
	}
	if len(h.applied) != 0 {
		t.Errorf("elevated for a block that was up to date: %v", h.applied)
	}
	if want := h.hostsTo + " is already up to date.\n"; out != want {
		t.Errorf("printed %q, want %q", out, want)
	}
}

// fakeCore listens on the control socket of the test's run directory and
// counts the HostsChanged calls.
type fakeCore struct {
	info    control.Info
	mu      sync.Mutex
	changed int
}

func (f *fakeCore) Info() control.Info     { return f.info }
func (f *fakeCore) Status() control.Status { return control.Status{} }
func (f *fakeCore) Stop()                  {}
func (f *fakeCore) HostsChanged(context.Context) {
	f.mu.Lock()
	f.changed++
	f.mu.Unlock()
}

// calls is how many HostsChanged calls the core has had.
func (f *fakeCore) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.changed
}

func TestHostsUpdateTellsTheCore(t *testing.T) {
	h := newUpdateHarness(t, updateConfig(), "127.0.0.1 localhost\n")

	// No core: not an error, and nothing more is said.
	out, err := h.run()
	if err != nil || out != "Updated "+h.hostsTo+".\n" {
		t.Fatalf("with no core: %v, %q", err, out)
	}

	core := &fakeCore{}
	srv, err := control.Listen(control.SocketPath(filepath.Join(filepath.Dir(h.cfgPath), "run")), core)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	// Out of date again, so the update writes.
	if err := os.WriteFile(h.hostsTo, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.run(); err != nil {
		t.Fatal(err)
	}
	if n := core.calls(); n != 1 {
		t.Errorf("HostsChanged called %d times, want once", n)
	}
}

func TestHostsUpdateFailureLeavesTheCoreAlone(t *testing.T) {
	h := newUpdateHarness(t, updateConfig(), "127.0.0.1 localhost\n")
	h.applyErr = errors.New("sudo: no")
	core := &fakeCore{}
	srv, err := control.Listen(control.SocketPath(filepath.Join(filepath.Dir(h.cfgPath), "run")), core)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	if _, err := h.run(); err == nil {
		t.Fatal("hosts update succeeded")
	}
	if core.calls() != 0 {
		t.Errorf("the core was told after a failed update")
	}
}
