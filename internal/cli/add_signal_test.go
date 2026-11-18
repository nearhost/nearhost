package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/testutil"
)

// signalAddClient is a fake ssh client for a remote add whose private run
// directories go in a directory of the test's, which it returns, and whose
// SFTP subsystem holds the first read of a file when hold is not "". It also
// returns the fake's log.
func signalAddClient(t *testing.T, hold string) (sshClient, string, string) {
	t.Helper()
	parent := testutil.TempDir(t, "p")
	log := filepath.Join(t.TempDir(), "ssh.log")
	env := []string{"FAKE_SSH_LOG=" + log, "FAKE_SSH_SFTP_SERVER=" + testutil.SFTPServer(t)}
	if hold != "" {
		env = append(env, "FAKE_SSH_SFTP_HOLD="+hold)
	}
	return sshClient{path: testutil.FakeSSH(t), env: env, runDirParent: parent}, parent, log
}

// signalProject is a project folder with a .env for add to read.
func signalProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("APP_PORT=8080\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// emptyDir fails the test unless dir holds nothing.
func emptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("%s was left behind in %s", e.Name(), dir)
	}
}

// A Ctrl-C while add waits on the server, here in the middle of reading a
// file over SFTP, ends the connection, removes the run directory, leaves no
// ssh running and saves nothing. The test cancels add's context, as the
// signal does, since a test cannot send itself one on every system.
func TestAddInterruptedDuringAnSFTPReadLeavesNothingBehind(t *testing.T) {
	hold := filepath.Join(t.TempDir(), "blocked")
	c, parent, log := signalAddClient(t, hold)
	cfgPath, before := existing(t)
	dir := serverPath(signalProject(t))

	t.Cleanup(func() {
		// Should add leave the held ssh running, it must not outlive the test.
		if pid, err := strconv.Atoi(readTrimmed(hold)); err == nil && testutil.ProcessAlive(pid) {
			if p, err := os.FindProcess(pid); err == nil {
				p.Kill()
			}
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out syncBuffer
	done := make(chan error, 1)
	go func() {
		done <- executeInto(ctx, &out, newAddCmd(addBackend{openRemote: c.openRemote}), "--config", cfgPath, "add", dir, "--server", "mini", "--name", "blog")
	}()

	// The fake writes its pid to hold once it holds a read.
	var pid int
	testutil.WaitUntil(t, 20*time.Second, func() bool {
		var err error
		pid, err = strconv.Atoi(readTrimmed(hold))
		return err == nil
	}, "the SFTP read to block")
	cancel()

	var err error
	select {
	case err = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("add did not return within 15 seconds of the interrupt")
	}
	var ee exitError
	if !errors.As(err, &ee) || ee.code != interruptedCode {
		t.Fatalf("add returned %v, want an exit with status %d\n%s", err, interruptedCode, out.String())
	}
	if got := out.String(); !strings.Contains(got, "Interrupted, so the project was not added.") || strings.Contains(got, "signal") {
		t.Errorf("add printed:\n%s", got)
	}
	emptyDir(t, parent)
	testutil.WaitUntil(t, 5*time.Second, func() bool { return !testutil.ProcessAlive(pid) }, "the held ssh to exit")
	noAddLeftovers(t, log)
	after, rerr := os.ReadFile(cfgPath)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !bytes.Equal(after, before) {
		t.Errorf("the config changed:\n%s", after)
	}
}

// A remote add that is not interrupted removes its run directory too.
func TestAddRemovesItsRunDirectory(t *testing.T) {
	c, parent, log := signalAddClient(t, "")
	cfgPath, _ := existing(t)
	dir := serverPath(signalProject(t))
	out, err := execute(t, newAddCmd(addBackend{openRemote: c.openRemote}), "--config", cfgPath, "add", dir, "--server", "mini", "--name", "blog")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if _, ok := loadSaved(t, cfgPath).Project("blog"); !ok {
		t.Fatalf("the project was not saved\n%s", out)
	}
	emptyDir(t, parent)
	noAddLeftovers(t, log)
}

// readTrimmed is the content of the file at path without surrounding space,
// or "" when it cannot be read.
func readTrimmed(path string) string {
	b, _ := os.ReadFile(path)
	return strings.TrimSpace(string(b))
}

// The end function a context watches runs once, whether the context ends
// first or the caller calls it, and a caller that calls it while the
// context's run is under way waits for that run.
func TestEndWithContextRunsOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runs := 0
	started, release := make(chan struct{}), make(chan struct{})
	end := endWithContext(ctx, func() error {
		runs++
		close(started)
		<-release
		return nil
	})
	cancel()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the end of the context did not run end")
	}
	returned := make(chan struct{})
	go func() {
		end()
		close(returned)
	}()
	select {
	case <-returned:
		t.Fatal("end returned before the run the context started had finished")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-returned
	end()
	if runs != 1 {
		t.Fatalf("end ran %d times", runs)
	}
}

// A Ctrl-C while add waits for an answer ends add, as it did when the
// signal killed it, and saves nothing.
func TestAddInterruptedAtAQuestion(t *testing.T) {
	cfgPath, before := existing(t)
	stdin, typing := io.Pipe() // nothing is ever typed
	defer typing.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out syncBuffer
	root := newRootCmd()
	root.AddCommand(newAddCmd(addBackend{interactive: func() bool { return true }}))
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(stdin)
	root.SetArgs([]string{"--config", cfgPath, "add", signalProject(t), "--server", "elsewhere"})
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()

	testutil.WaitUntil(t, 10*time.Second, func() bool {
		return strings.Contains(out.String(), "Add it? [Y/n]")
	}, "the question")
	cancel()
	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("add did not return within 10 seconds of the interrupt")
	}
	var ee exitError
	if !errors.As(err, &ee) || ee.code != interruptedCode {
		t.Fatalf("add returned %v, want an exit with status %d\n%s", err, interruptedCode, out.String())
	}
	after, _ := os.ReadFile(cfgPath)
	if !bytes.Equal(after, before) {
		t.Errorf("the config changed:\n%s", after)
	}
}
