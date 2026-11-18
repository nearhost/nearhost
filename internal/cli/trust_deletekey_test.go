package cli

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
)

// runIn is run with input on stdin.
func runIn(t *testing.T, cmd *cobra.Command, input string, args ...string) (string, error) {
	t.Helper()
	root := &cobra.Command{Use: "nearhost", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(cmd)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetIn(strings.NewReader(input))
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

// localCA sets up config and data directories for the test with a local CA
// in Caddy's layout and a sibling file beside it, and returns the CA's
// directory and the sibling's path.
func localCA(t *testing.T) (ca, sibling string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir, err := config.CaddyDir()
	if err != nil {
		t.Fatal(err)
	}
	ca = filepath.Join(dir, "pki", "authorities", "local")
	if err := os.MkdirAll(ca, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"root.crt", "root.key", "intermediate.crt", "intermediate.key"} {
		if err := os.WriteFile(filepath.Join(ca, f), []byte(f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sibling = filepath.Join(dir, "pki", "authorities", "other", "keep")
	if err := os.MkdirAll(filepath.Dir(sibling), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sibling, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return ca, sibling
}

// present reports whether path is there, without following a final link.
func present(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

const deletedLine = "Deleted the local CA and its key."

func TestUntrustDeleteKeyRefusesWhileNearhostRuns(t *testing.T) {
	ca, _ := localCA(t)
	f := newFake()
	f.trusted = true
	f.running = true
	f.interactive = true
	_, err := runIn(t, newUntrustCmd(f.backend()), "y\n", "untrust", "--delete-key", "--yes")
	if err == nil || !strings.Contains(err.Error(), "Stop Nearhost") {
		t.Fatalf("err = %v", err)
	}
	if !present(filepath.Join(ca, "root.key")) || f.uninstall != 0 || f.nssUninstalls != 0 || len(f.rootDirs) != 0 {
		t.Fatalf("something ran: uninstalls=%d nssUninstalls=%d rootDirs=%v", f.uninstall, f.nssUninstalls, f.rootDirs)
	}
}

func TestUntrustDeleteKeyRemovesTheCAOnYes(t *testing.T) {
	ca, sibling := localCA(t)
	f := newFake()
	f.trusted = true
	f.interactive = true
	out, err := runIn(t, newUntrustCmd(f.backend()), "y\n", "untrust", "--delete-key")
	if err != nil {
		t.Fatal(err)
	}
	if f.uninstall != 1 {
		t.Fatalf("untrust did not run first: uninstalls=%d", f.uninstall)
	}
	question := "Delete the local CA and its key in " + ca + "? Nearhost makes a new one, which you trust again, the next time it runs. [y/N]"
	if !strings.Contains(out, question) || !strings.Contains(out, deletedLine) {
		t.Fatalf("output %q", out)
	}
	if strings.Index(out, "removed from the trust store") > strings.Index(out, question) {
		t.Fatalf("asked before untrusting: %q", out)
	}
	if present(ca) {
		t.Fatal("the CA's directory is still there")
	}
	if !present(sibling) {
		t.Fatal("a sibling of the CA's directory was removed")
	}
}

func TestUntrustDeleteKeyKeepsTheCAOnNo(t *testing.T) {
	for _, answer := range []string{"\n", "n\n", ""} {
		ca, _ := localCA(t)
		f := newFake()
		f.interactive = true
		out, err := runIn(t, newUntrustCmd(f.backend()), answer, "untrust", "--delete-key")
		if err != nil {
			t.Fatal(err)
		}
		if !present(filepath.Join(ca, "root.key")) || strings.Contains(out, deletedLine) {
			t.Fatalf("answer %q deleted the CA: %q", answer, out)
		}
		if !strings.Contains(out, "Kept the local CA and its key.") {
			t.Fatalf("output %q", out)
		}
	}
}

func TestUntrustDeleteKeyWithoutATerminalNeedsYes(t *testing.T) {
	ca, _ := localCA(t)
	f := newFake()
	f.trusted = true
	_, err := runIn(t, newUntrustCmd(f.backend()), "y\n", "untrust", "--delete-key")
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("err = %v", err)
	}
	if !present(filepath.Join(ca, "root.key")) || f.uninstall != 0 {
		t.Fatalf("something ran: uninstalls=%d", f.uninstall)
	}
}

// --yes deletes without asking, here with a root that was never trusted:
// untrust has nothing to do and the key still goes.
func TestUntrustDeleteKeyYesDeletesWithoutAsking(t *testing.T) {
	for _, flag := range []string{"--yes", "-y"} {
		ca, _ := localCA(t)
		f := newFake()
		out, err := runIn(t, newUntrustCmd(f.backend()), "", "untrust", "--delete-key", flag)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "[y/N]") || !strings.Contains(out, untrustNotInStore) || !strings.Contains(out, deletedLine) {
			t.Fatalf("output %q", out)
		}
		if present(ca) {
			t.Fatal("the CA's directory is still there")
		}
	}
}

// Trust that untrust could not remove does not stop the key going: a trusted
// root with no key on disk can sign nothing new. The command still fails and
// says both things.
func TestUntrustDeleteKeyDeletesWhenTrustRemains(t *testing.T) {
	ca, _ := localCA(t)
	f := newFake()
	f.trusted = true
	f.stillTrusted = true
	f.interactive = true
	out, err := runIn(t, newUntrustCmd(f.backend()), "yes\n", "untrust", "--delete-key")
	if err == nil || !strings.Contains(err.Error(), "still") || !strings.Contains(err.Error(), "by hand") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, deletedLine) || present(ca) {
		t.Fatalf("the CA was not deleted: %q", out)
	}
}

// A failed untrust, such as a declined password prompt, also leaves the key
// to be deleted, and the error says the trust remains.
func TestUntrustDeleteKeyDeletesWhenUntrustFails(t *testing.T) {
	ca, _ := localCA(t)
	f := newFake()
	f.trusted = true
	f.uninstErr = errors.New("sudo: 3 incorrect password attempts")
	out, err := runIn(t, newUntrustCmd(f.backend()), "", "untrust", "--delete-key", "--yes")
	if err == nil || !strings.Contains(err.Error(), "incorrect password") || !strings.Contains(err.Error(), "by hand") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, deletedLine) || present(ca) {
		t.Fatalf("the CA was not deleted: %q", out)
	}
}

// An authority directory that links out of the data directory is left alone
// and the command fails.
func TestUntrustDeleteKeyRefusesALinkOutOfTheDataDir(t *testing.T) {
	ca, _ := localCA(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "root.key"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(ca); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, ca); err != nil {
		t.Skipf("symlink: %v", err)
	}
	f := newFake()
	out, err := runIn(t, newUntrustCmd(f.backend()), "", "untrust", "--delete-key", "--yes")
	if err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(out, deletedLine) || !present(filepath.Join(outside, "root.key")) || !present(ca) {
		t.Fatalf("output %q", out)
	}
}

// A deletion that fails says so and fails the command.
func TestUntrustDeleteKeyReportsAFailedDeletion(t *testing.T) {
	localCA(t)
	f := newFake()
	f.removeErr = errors.New("could not delete all of the CA")
	out, err := runIn(t, newUntrustCmd(f.backend()), "", "untrust", "--delete-key", "--yes")
	if err == nil || !strings.Contains(err.Error(), "could not delete all of the CA") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(out, deletedLine) {
		t.Fatalf("output %q", out)
	}
}

func TestUntrustHelpNamesDeleteKeyAndYes(t *testing.T) {
	f := newFake()
	out, err := run(t, newUntrustCmd(f.backend()), "untrust", "--help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "--delete-key") || !strings.Contains(out, "-y, --yes") {
		t.Fatalf("help %q", out)
	}
}

// --yes means nothing without --delete-key.
func TestUntrustYesNeedsDeleteKey(t *testing.T) {
	localCA(t)
	f := newFake()
	if _, err := run(t, newUntrustCmd(f.backend()), "untrust", "--yes"); err == nil || !strings.Contains(err.Error(), "--delete-key") {
		t.Fatalf("err = %v", err)
	}
}

// A key whose root cannot be read still goes, and the command fails because
// nothing could check what trusts the root.
func TestUntrustDeleteKeyDeletesAKeyWithAnUnreadableRoot(t *testing.T) {
	ca, _ := localCA(t)
	f := newFake()
	f.rootErr = errors.New("root.crt is not PEM")
	out, err := runIn(t, newUntrustCmd(f.backend()), "", "untrust", "--delete-key", "--yes")
	if err == nil || !strings.Contains(err.Error(), "not PEM") || f.uninstall != 0 {
		t.Fatalf("err=%v uninstalls=%d", err, f.uninstall)
	}
	if !strings.Contains(out, deletedLine) || present(ca) {
		t.Fatalf("the CA was not deleted: %q", out)
	}
}

// With no CA at all there is nothing to delete or ask about.
func TestUntrustDeleteKeyWithNoCASaysSo(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	f := newFake()
	f.rootErr = fs.ErrNotExist
	f.interactive = true
	out, err := runIn(t, newUntrustCmd(f.backend()), "y\n", "untrust", "--delete-key")
	if err == nil || !strings.Contains(err.Error(), "no local CA found") || strings.Contains(out, "[y/N]") {
		t.Fatalf("err=%v output %q", err, out)
	}
}
