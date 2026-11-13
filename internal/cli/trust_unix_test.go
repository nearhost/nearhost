//go:build unix

package cli

// A chmod to 0o500 does not stop writes on Windows, so the CA's move to the
// data directory cannot be made to fail this way there.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
)

func TestTrustCarriesOnWhenTheMoveFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to read-only directories")
	}
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	old := filepath.Join(cfgHome, "nearhost", "caddy")
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := config.DataDir()
	if err != nil {
		t.Fatal(err)
	}
	// The data directory's parent can't be written, so the move fails.
	parent := filepath.Dir(data)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o700); os.RemoveAll(data) })
	f := newFake()
	root := &cobra.Command{Use: "nearhost", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(newTrustCmd(f.backend()))
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs([]string{"trust"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(f.ensured) != 1 || f.ensured[0] != old {
		t.Fatalf("CA made in %v, want the old %s", f.ensured, old)
	}
	if !strings.Contains(errOut.String(), "Warning") || !strings.Contains(errOut.String(), old) {
		t.Fatalf("stderr %q", errOut.String())
	}
}
