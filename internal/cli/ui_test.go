package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

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
		run: func(m *tui.Model, s *tui.Suspender) error {
			*ran = true
			return nil
		},
		applier:   func(context.Context) hosts.Applier { return func([]string) error { return nil } },
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
	var said bytes.Buffer
	suspended := 0
	suspend := func(_ context.Context, fn func(io.Reader, io.Writer) error) error {
		suspended++
		return fn(strings.NewReader(""), &said)
	}
	var wrote [][]string
	applier := func(context.Context) hosts.Applier {
		return func(aliases []string) error {
			wrote = append(wrote, aliases)
			return hosts.Apply(path, aliases)
		}
	}
	ensure := hostsThrough(suspend, path, applier)
	if err := ensure(context.Background(), []string{"a.test", "b.test"}); err != nil {
		t.Fatal(err)
	}
	if err := ensure(context.Background(), []string{"a.test", "b.test"}); err != nil {
		t.Fatal(err)
	}
	if suspended != 1 || len(wrote) != 1 {
		t.Fatalf("suspended %d times, wrote %v", suspended, wrote)
	}
	if want := "Nearhost needs your password to add a.test, b.test to " + path + ".\n"; said.String() != want {
		t.Errorf("said %q, want %q", said.String(), want)
	}
	// A start that has ended by the time the block needs changing does not
	// take the terminal.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := suspended
	if err := ensure(ctx, []string{"d.test"}); err == nil {
		t.Error("an ended start updated the hosts block")
	}
	if suspended != before {
		t.Error("an ended start took the terminal")
	}
	refused := hostsThrough(func(context.Context, func(io.Reader, io.Writer) error) error {
		return errors.New("sudo: a password is required")
	}, path, applier)
	if err := refused(context.Background(), []string{"c.test"}); err == nil || !strings.Contains(err.Error(), "password") {
		t.Errorf("refused = %v", err)
	}
}

func TestHostsState(t *testing.T) {
	u := &uiEngine{flags: serveFlags{noHosts: true}}
	if got := u.HostsState(); got != "not managed (--no-hosts)" {
		t.Errorf("--no-hosts: %q", got)
	}
}
