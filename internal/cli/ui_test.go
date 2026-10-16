package cli

import (
	"bytes"
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/hosts"
	"github.com/nearhost/nearhost/internal/tui"
)

// uiRoot is a root command whose bare form opens the interface through b.
func uiRoot(b uiBackend) *cobra.Command {
	root := newRootCmd()
	root.AddCommand(newVersionCmd())
	bindUI(root, b)
	return root
}

func runRoot(t *testing.T, root *cobra.Command, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return out.String(), err
}

// fakeUI is a backend whose interface run is recorded instead of drawn.
func fakeUI(t *testing.T, terminal bool, ran *bool) uiBackend {
	t.Helper()
	return uiBackend{
		interactive: func() bool { return terminal },
		run: func(m *tui.Model, attach func(*tea.Program)) error {
			*ran = true
			return nil
		},
		applier:   func(context.Context, bool) hosts.Applier { return func([]string) error { return nil } },
		readHosts: func() (string, error) { return "", nil },
		open:      func(string) error { return nil },
		workDir:   func() (string, error) { return t.TempDir(), nil },
	}
}

func TestBareNearhostWithoutATerminalPrintsHelp(t *testing.T) {
	ran := false
	out, err := runRoot(t, uiRoot(fakeUI(t, false, &ran)))
	if err != nil || ran || !strings.Contains(out, "Usage:") {
		t.Fatalf("err %v, ran %v, out:\n%s", err, ran, out)
	}
}

func TestBareNearhostOpensTheInterfaceWithNoConfigFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // the engine's run and CA directories
	ran := false
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if _, err := runRoot(t, uiRoot(fakeUI(t, true, &ran)), "--config", cfgPath, "--https-port", "8443", "--http-port", "8080"); err != nil || !ran {
		t.Fatalf("err %v, ran %v", err, ran)
	}
}

func TestBareNearhostRefusesWhatUpRefuses(t *testing.T) {
	ran := false
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("projects: [{name: a}, {name: a}]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runRoot(t, uiRoot(fakeUI(t, true, &ran)), "--config", cfgPath); err == nil || ran {
		t.Fatalf("invalid config: err %v, ran %v", err, ran)
	}
	if _, err := runRoot(t, uiRoot(fakeUI(t, true, &ran)), "--https-port", "80", "--http-port", "80"); err == nil || ran {
		t.Fatalf("same ports: err %v, ran %v", err, ran)
	}
	if _, err := runRoot(t, uiRoot(fakeUI(t, true, &ran)), "nosuchcommand"); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("unknown command: %v", err)
	}
}

func TestHostsThroughAsksOnlyWhenTheBlockChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(path, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeRoot{needed: true, sticks: true}
	var wrote [][]string
	var prompted []bool
	applier := func(_ context.Context, prompt bool) hosts.Applier {
		return func(aliases []string) error {
			wrote = append(wrote, aliases)
			prompted = append(prompted, prompt)
			return hosts.Apply(path, aliases)
		}
	}
	ensure := hostsThrough(f.root(), path, applier)
	if err := ensure(context.Background(), []string{"a.test", "b.test"}); err != nil {
		t.Fatal(err)
	}
	if err := ensure(context.Background(), []string{"a.test", "b.test"}); err != nil {
		t.Fatal(err)
	}
	if want := "Nearhost needs your password to add a.test, b.test to " + path + "."; strings.Join(f.asked, "|") != want {
		t.Errorf("asked %q, want %q", f.asked, want)
	}
	if len(wrote) != 1 || prompted[0] || f.suspended != 0 {
		t.Fatalf("wrote %v, prompted %v, suspended %d", wrote, prompted, f.suspended)
	}
	// A start that has ended by the time the block needs changing asks
	// nothing.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ensure(ctx, []string{"d.test"}); err == nil {
		t.Error("an ended start updated the hosts block")
	}
	if len(f.asked) != 1 {
		t.Error("an ended start asked for the password")
	}
	// A password cancelled says what was not done.
	cancelled := hostsThrough((&fakeRoot{needed: true, askErr: tui.ErrPasswordCancelled}).root(), path, applier)
	if err := cancelled(context.Background(), []string{"c.test"}); err == nil || err.Error() != "you cancelled the password, so c.test was not added to "+path {
		t.Errorf("cancelled = %v", err)
	}
}

func TestHostsState(t *testing.T) {
	u := &uiEngine{flags: serveFlags{noHosts: true}}
	if got := u.HostsState(); got != "not managed (--no-hosts)" {
		t.Errorf("--no-hosts: %q", got)
	}
}

// The first HostsState call happens while the interface is built, and must
// not wait on DNS; later calls apply the lookup.
func TestHostsStateFirstCallSkipsTheLookup(t *testing.T) {
	cfg := &config.Config{Projects: []config.Project{localProject("web", 8000, true)}}
	var asked []string
	u := &uiEngine{
		Engine: engine.New(cfg, engine.Options{}),
		b: uiBackend{
			readHosts: func() (string, error) { return "127.0.0.1 localhost\n", nil },
			lookup: func(_ context.Context, name string) ([]netip.Addr, error) {
				asked = append(asked, name)
				return nil, nil
			},
		},
	}
	if got := u.HostsState(); got != "checking" || len(asked) != 0 {
		t.Fatalf("first call = %q, lookups %v", got, asked)
	}
	if got := u.HostsState(); got != "missing web.test" {
		t.Errorf("second call = %q", got)
	}
}
