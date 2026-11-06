//go:build unix

package elevate

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/sudo"
)

// hostsPrompt is the prompt the hosts applier of `nearhost up` passes.
const hostsPrompt = "nearhost needs your password to update /etc/hosts: "

// useSudo makes path the sudo that Nearhost runs, root-owned or not. The
// file must exist and be a regular file.
func useSudo(t *testing.T, path string) {
	t.Helper()
	// The umask can take the execute bits the caller's WriteFile asked for,
	// and sudo.Binary skips a file no one can run.
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	origCandidates, origOwned := sudo.Candidates, sudo.OwnedByRoot
	sudo.Candidates = []string{path}
	sudo.OwnedByRoot = func(fs.FileInfo) bool { return true }
	t.Cleanup(func() { sudo.Candidates, sudo.OwnedByRoot = origCandidates, origOwned })
}

func TestSudoCommandCarriesArgsAndInput(t *testing.T) {
	fakeSudo := filepath.Join(t.TempDir(), "sudo")
	if err := os.WriteFile(fakeSudo, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	useSudo(t, fakeSudo)
	cmd, err := sudoCommand(context.Background(), Command{
		Path:        "/bin/nearhost",
		Args:        []string{"hosts", "apply", "--file", "/tmp/other-hosts"},
		Input:       "a.test\nb.test\n",
		Prompt:      hostsPrompt,
		Interactive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(cmd.Args, " ")
	if !strings.HasSuffix(args, "/bin/nearhost hosts apply --file /tmp/other-hosts") {
		t.Fatalf("args: %s", args)
	}
	in, _ := io.ReadAll(cmd.Stdin)
	if string(in) != "a.test\nb.test\n" {
		t.Fatalf("stdin: %q", in)
	}
}

func TestSudoRunStopsSudoWhenCancelled(t *testing.T) {
	// A stand-in for sudo that, like sudo at its password prompt, does not
	// finish by itself.
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	fake := filepath.Join(dir, "sudo")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n: > '"+started+"'\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	useSudo(t, fake)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- System().Run(ctx, Command{Path: "/bin/nearhost", Prompt: hostsPrompt, Interactive: true})
	}()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the stand-in for sudo never started")
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v, want the context's error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run kept waiting for sudo after its context was cancelled")
	}
}

func TestSudoRunDoesNotStartSudoWhenAlreadyCancelled(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	fake := filepath.Join(dir, "sudo")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n: > '"+started+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	useSudo(t, fake)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := System().Run(ctx, Command{Path: "/bin/nearhost", Prompt: hostsPrompt, Interactive: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want the context's error", err)
	}
	if _, err := os.Stat(started); err == nil {
		t.Fatal("sudo was started for a cancelled context")
	}
}

// Run can run sudo so it never prompts, for credentials already cached, or
// prompt with words of the caller's.
func TestSudoRunPromptAndNoPrompt(t *testing.T) {
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	fake := filepath.Join(dir, "sudo")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho \"$*\" > '"+args+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	useSudo(t, fake)
	ctx := context.Background()
	for _, c := range []struct {
		prompt      string
		interactive bool
		want        string
	}{
		{"", false, "-n /bin/nearhost hosts apply --file /etc/hosts\n"},
		{"Password for %p: ", true, "-p Password for %p:  /bin/nearhost hosts apply --file /etc/hosts\n"},
	} {
		err := System().Run(ctx, Command{
			Path:        "/bin/nearhost",
			Args:        []string{"hosts", "apply", "--file", "/etc/hosts"},
			Input:       "a.test\n",
			Prompt:      c.prompt,
			Interactive: c.interactive,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(args); string(got) != c.want {
			t.Errorf("sudo %q, want %q", got, c.want)
		}
	}
}

// Without a prompt Run works under a terminal in use: what sudo or the
// command says goes into the error, not onto the screen.
func TestSudoRunWithoutAPromptKeepsTheTerminalClear(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "sudo")
	script := "#!/bin/sh\necho 'some output'\necho 'sudo: a password is required' >&2\nexit 1\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	useSudo(t, fake)
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	stdout, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	err := System().Run(context.Background(), Command{Path: "/bin/nearhost", Args: []string{"hosts", "apply"}})
	os.Stdout, os.Stderr = stdout, stderr
	outW.Close()
	errW.Close()
	shown, _ := io.ReadAll(io.MultiReader(outR, errR))
	if len(shown) > 0 {
		t.Errorf("reached the terminal: %q", shown)
	}
	if err == nil || !strings.Contains(err.Error(), "a password is required") {
		t.Errorf("err = %v", err)
	}
}

// Needed is true when there is a sudo to run and Nearhost is not root.
func TestSudoNeededWhenSudoIsThere(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "sudo")
	if err := os.WriteFile(fake, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	useSudo(t, fake)
	if got, want := System().Needed(), os.Geteuid() != 0; got != want {
		t.Errorf("Needed() = %v, want %v", got, want)
	}
	sudo.Candidates = []string{filepath.Join(t.TempDir(), "no-sudo")}
	if System().Needed() {
		t.Error("needed without a sudo to run")
	}
}

// Elevated is whether the effective user is root; sudo collects a password.
func TestSudoElevatedAndTakesPassword(t *testing.T) {
	if got, want := Elevated(), os.Geteuid() == 0; got != want {
		t.Errorf("Elevated() = %v, want %v", got, want)
	}
	if !System().TakesPassword() {
		t.Error("sudo takes a password")
	}
}
