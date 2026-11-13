//go:build unix

package cli

// The up tests that only make sense on the ControlMaster transport, which
// Windows does not have.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/testutil"
)

// The per-process transport (all Windows has) cancels a forward by ending
// that forward's own ssh, which neither fails nor waits on the server: these
// tests need the ControlMaster's `-O cancel`, which can do both.

func TestUpExitsNonZeroWhenShutdownFails(t *testing.T) {
	u := newUpRun(t, &config.Config{Servers: mini, Projects: []config.Project{remoteProject("api")}})
	u.noHosts = true
	flag := filepath.Join(t.TempDir(), "cancel-fails")
	if err := os.WriteFile(flag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	u.sshEnv = []string{"FAKE_SSH_CANCEL_FAIL=" + flag}
	u.start()
	u.waitFor("Serving until interrupted")
	err := u.stop()
	if err == nil || !strings.Contains(err.Error(), "shut down:") {
		t.Fatalf("expected the shutdown failure, got %v; output:\n%s", err, u.out.String())
	}
	if out := u.out.String(); strings.HasSuffix(out, "\nstopped\n") || strings.Contains(out, "fix:") {
		t.Errorf("claimed a clean stop, or advised on a failure of the shutdown:\n%s", out)
	}
}

// The ssh masters do not get the terminal's Ctrl-C, so a second one must not
// simply end the process: it cuts the shutdown short, and the masters are
// still ended before up returns.
func TestUpInterruptedAgainWhileShuttingDownStillEndsTheMasters(t *testing.T) {
	u := newUpRun(t, &config.Config{Servers: mini, Projects: []config.Project{remoteProject("api")}})
	u.noHosts = true
	wait := filepath.Join(t.TempDir(), "cancel-waits")
	if err := os.WriteFile(wait, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(wait) })
	u.sshEnv = []string{"FAKE_SSH_CANCEL_WAIT=" + wait}
	u.start()
	u.waitFor("Serving until interrupted")

	u.cancel() // the first signal: the shutdown now waits on the forward's cancel
	u.waitFor("shutting down")
	testutil.WaitUntil(t, 10*time.Second, func() bool {
		return strings.Contains(sshLog(t, filepath.Join(filepath.Dir(u.cfgPath), "ssh.log")), "-O cancel")
	}, "the shutdown to be cancelling the forward")
	u.interruptAgain()
	begin := time.Now()
	err := u.stop() // also checks that no ssh process, listener or goroutine is left
	if took := time.Since(begin); took > 15*time.Second {
		t.Errorf("up took %s to return after the second signal", took)
	}
	if err == nil || !strings.Contains(err.Error(), "shut down:") {
		t.Errorf("expected the cut-short shutdown to be reported, got %v", err)
	}
	out := u.out.String()
	if strings.Count(out, "\nstopping now\n") != 1 || strings.HasSuffix(out, "\nstopped\n") {
		t.Errorf("unexpected output:\n%s", out)
	}
	if log := sshLog(t, filepath.Join(filepath.Dir(u.cfgPath), "ssh.log")); !strings.Contains(log, "-O exit -- mini.lan") {
		t.Errorf("the master was not asked to exit:\n%s", log)
	}
}
