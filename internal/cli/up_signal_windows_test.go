package cli

// Ctrl-C in a Windows console reaches `nearhost up` as os.Interrupt through
// the same subscription as on Unix (notifyStop). These tests send a real
// console interrupt: they run a copy of this test binary in a process group
// of its own and send that group CTRL_BREAK_EVENT, which Go delivers as
// os.Interrupt, as Go's own os/signal tests do. (CTRL_C_EVENT cannot be sent
// to a group other than the whole console's.) The ssh processes are in
// groups of their own with no console, so the interrupt reaches only up, and
// only the shutdown can end them.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/testutil"
)

// interruptCopy, in the environment of the copy, says which test the copy
// serves: "first" or "again".
const interruptCopy = "NEARHOST_TEST_UP_INTERRUPT"

// The copy prints these on lines of their own when it is ready for an
// interrupt.
const (
	markServing  = "nearhost-test: serving"
	markShutdown = "nearhost-test: shutting down"
)

// The first interrupt shuts up down in order and ends every ssh.
func TestUpStopsOnAConsoleInterrupt(t *testing.T) {
	interruptCopyOfUp(t, "first", markServing)
}

// The ssh processes do not get the console's interrupt, so a second one must
// not simply end the process: it cuts the shutdown short, and every ssh is
// still ended before up returns.
func TestUpInterruptedAgainOnTheConsoleStillEndsEverySSH(t *testing.T) {
	interruptCopyOfUp(t, "again", markServing, markShutdown)
}

// interruptCopyOfUp runs TestUpUnderConsoleInterrupts in a copy of this test
// binary for mode, sends it a console interrupt at each of marks, and fails if
// the copy fails.
func interruptCopyOfUp(t *testing.T, mode string, marks ...string) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestUpUnderConsoleInterrupts$", "-test.v", "-test.count=1", "-test.timeout=5m")
	cmd.Env = append(os.Environ(), interruptCopy+"="+mode)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	out := &markWriter{marks: make(chan string, 8)}
	cmd.Stdout, cmd.Stderr = out, out
	// A leaked ssh holding the output would otherwise keep Wait waiting;
	// the copy's own checks report the leak.
	cmd.WaitDelay = 10 * time.Second
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			<-exited
		}
	})
	for _, mark := range marks {
		select {
		case got := <-out.marks:
			if got != mark {
				t.Fatalf("the copy printed %q, want %q; output:\n%s", got, mark, out)
			}
		case err := <-exited:
			exited <- err
			t.Fatalf("the copy ended (%v) before printing %q; output:\n%s", err, mark, out)
		case <-time.After(2 * time.Minute):
			t.Fatalf("the copy did not print %q; output:\n%s", mark, out)
		}
		if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(cmd.Process.Pid)); err != nil {
			t.Fatalf("GenerateConsoleCtrlEvent: %v", err)
		}
	}
	select {
	case err = <-exited:
	case <-time.After(2 * time.Minute):
		t.Fatalf("the copy did not end after the interrupt; output:\n%s", out)
	}
	if err != nil {
		t.Fatalf("the copy failed: %v; output:\n%s", err, out)
	}
	if !strings.Contains(out.String(), "--- PASS: TestUpUnderConsoleInterrupts") {
		t.Fatalf("the copy did not run the test; output:\n%s", out)
	}
}

// markWriter collects the copy's output and passes on the marks in it.
type markWriter struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	line  []byte
	marks chan string
}

func (w *markWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	w.line = append(w.line, p...)
	for {
		i := bytes.IndexByte(w.line, '\n')
		if i < 0 {
			break
		}
		if l := strings.TrimSpace(string(w.line[:i])); strings.HasPrefix(l, "nearhost-test: ") {
			select {
			case w.marks <- l:
			default:
			}
		}
		w.line = w.line[i+1:]
	}
	return len(p), nil
}

func (w *markWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// TestUpUnderConsoleInterrupts is the copy's half of the tests above: up
// with the production subscription to interrupts, serving a project over the
// fake ssh until the parent interrupts it. Run on its own it does nothing.
func TestUpUnderConsoleInterrupts(t *testing.T) {
	mode := os.Getenv(interruptCopy)
	if mode == "" {
		return
	}
	u := newUpRun(t, &config.Config{Servers: mini, Projects: []config.Project{remoteProject("api")}})
	u.noHosts = true
	var (
		mu       sync.Mutex
		heldAt   time.Time
		deadAtIt []int // ssh processes already gone when the shutdown began
	)
	u.adjust = func(b *upBackend) {
		b.notify = notifyStop
		// Note which ssh processes are left when the shutdown begins: the
		// interrupt must not have reached them. For the second interrupt,
		// hold the shutdown open until that interrupt ends its context (or
		// its timeout does), then shut down with that ended context, as up
		// does when cut short.
		b.shutdown = func(eng *engine.Engine, ctx context.Context) error {
			pids, _ := u.sshPids()
			mu.Lock()
			heldAt = time.Now()
			for _, pid := range pids {
				if !testutil.ProcessAlive(pid) {
					deadAtIt = append(deadAtIt, pid)
				}
			}
			mu.Unlock()
			if mode == "again" {
				fmt.Println(markShutdown)
				<-ctx.Done()
			}
			return eng.Shutdown(ctx)
		}
	}
	u.start()
	u.waitFor("Serving until interrupted")
	fmt.Println(markServing)

	var err error
	select {
	case err = <-u.done:
	case <-time.After(60 * time.Second):
		t.Fatalf("up did not return after the console interrupt; output:\n%s", u.out.String())
	}
	returned := time.Now()
	u.done <- err
	u.stop() // checks that no ssh process, listener or goroutine is left
	u.wantSSHEnded()
	out := u.out.String()
	if !strings.Contains(out, "\nshutting down\n") {
		t.Errorf("up did not shut down:\n%s", out)
	}
	mu.Lock()
	defer mu.Unlock()
	if heldAt.IsZero() {
		t.Fatalf("the shutdown never began; output:\n%s", out)
	}
	if pids, _ := u.sshPids(); len(deadAtIt) > 0 || len(pids) == 0 {
		t.Errorf("ssh processes %v of %v had ended before the shutdown began", deadAtIt, pids)
	} else if slices.ContainsFunc(pids, testutil.ProcessAlive) {
		t.Errorf("ssh processes are left: %v", pids)
	}
	switch mode {
	case "first":
		if err != nil || strings.Contains(out, "stopping now") || !strings.HasSuffix(out, "\nstopped\n") {
			t.Errorf("up returned %v; output:\n%s", err, out)
		}
	case "again":
		if took := returned.Sub(heldAt); took > 15*time.Second {
			t.Errorf("up took %s to return after the second interrupt, so it did not cut the shutdown short", took)
		}
		if strings.Count(out, "\nstopping now\n") != 1 {
			t.Errorf("the second interrupt did not hurry the shutdown; up returned %v; output:\n%s", err, out)
		}
	default:
		t.Fatalf("unknown %s=%q", interruptCopy, mode)
	}
}
