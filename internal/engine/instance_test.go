package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/testutil"
)

// instanceConfig is a config with one local project, for the instance tests.
func instanceConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{Projects: []config.Project{localProject("web", upstream(t, "ok"))}}
}

func TestASecondEngineWithTheSameRunDirectoryStops(t *testing.T) {
	first := newHarness(t, instanceConfig(t))
	// Its own ports, so only the run directory is shared.
	second := buildHarness(t, instanceConfig(t), func(o *Options) { o.RunDir = first.runDir })

	err := second.eng.Start(context.Background())
	want := fmt.Sprintf("Another Nearhost is already running (pid %d).", os.Getpid())
	if err == nil || err.Error() != want {
		t.Fatalf("second Start = %v, want %q", err, want)
	}
	if second.eng.proxy.Running() {
		t.Fatal("the second engine started its proxy")
	}
	if !first.eng.proxy.Running() {
		t.Fatal("the first engine's proxy stopped")
	}

	// Once the first is gone, the second can start.
	if err := first.eng.Shutdown(context.Background()); err != nil {
		t.Fatalf("first Shutdown: %v", err)
	}
	if err := second.eng.Start(context.Background()); err != nil {
		t.Fatalf("second Start after the first shut down: %v", err)
	}
}

func TestInstanceErrorWithNoPidLeavesOutTheParenthesis(t *testing.T) {
	want := "Another Nearhost is already running."
	if got := alreadyRunning(0).Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestALockFileWithNoHolderDoesNotBlockStart(t *testing.T) {
	// A process that has exited leaves its pid behind and holds nothing.
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	dead := cmd.Process.Pid
	h := buildHarness(t, instanceConfig(t), nil)
	path := filepath.Join(h.runDir, instanceLockName)
	if err := os.WriteFile(path, []byte(strconv.Itoa(dead)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := h.eng.Start(context.Background()); err != nil {
		t.Fatalf("Start with a stale lock file: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != strconv.Itoa(os.Getpid()) {
		t.Fatalf("lock file holds %q, want this process's pid %d", got, os.Getpid())
	}
}

func TestASecondEngineOnTheSamePortsGetsThePortErrorFirst(t *testing.T) {
	first := newHarness(t, instanceConfig(t))
	second := buildHarness(t, instanceConfig(t), func(o *Options) {
		o.RunDir = first.runDir
		o.HTTPSPort, o.HTTPPort = first.httpsPort, first.httpPort
	})
	second.eng.holderOf = func(context.Context, int) string { return fmt.Sprintf("nearhost (pid %d)", os.Getpid()) }

	err := second.eng.Start(context.Background())
	var pe *portError
	if !errors.As(err, &pe) {
		t.Fatalf("second Start = %v, want the port error", err)
	}
	if want := fmt.Sprintf("is in use by nearhost (pid %d)", os.Getpid()); !strings.Contains(err.Error(), want) {
		t.Fatalf("second Start = %q, want it to say %q", err, want)
	}
	// The second harness checks its ports are free when the test ends, and
	// they are the first's.
	if err := first.eng.Shutdown(context.Background()); err != nil {
		t.Fatalf("first Shutdown: %v", err)
	}
}

func TestARefusedEngineConnectsNoServer(t *testing.T) {
	first := newHarness(t, remoteConfig(testutil.FreePort(t)))
	second := buildHarness(t, remoteConfig(testutil.FreePort(t)), func(o *Options) { o.RunDir = first.runDir })
	if err := second.eng.Start(context.Background()); !IsAlreadyRunning(err) {
		t.Fatalf("second Start = %v, want the instance error", err)
	}

	// Starting a session would clear its socket in the shared run
	// directory, and end the first engine's master on it.
	if err := second.eng.ConnectServer("mini"); !IsAlreadyRunning(err) {
		t.Fatalf("ConnectServer = %v, want the instance error", err)
	}
	if err := second.eng.ReconnectServer("mini"); err == nil {
		t.Fatal("ReconnectServer succeeded on a refused engine")
	}
	if err := second.eng.StartProject(context.Background(), "myapp"); err == nil {
		t.Fatal("StartProject succeeded on a refused engine")
	}
	if log := second.log(); log != "" {
		t.Fatalf("the refused engine ran ssh:\n%s", log)
	}

	if err := first.eng.Shutdown(context.Background()); err != nil {
		t.Fatalf("first Shutdown: %v", err)
	}
}

func TestAFailedProxyStartReleasesTheLock(t *testing.T) {
	// Storage under a regular file: the proxy cannot create its CA there.
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	failing := buildHarness(t, instanceConfig(t), func(o *Options) { o.StorageRoot = filepath.Join(file, "caddy") })
	err := failing.eng.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "start proxy") {
		t.Fatalf("Start = %v, want the proxy to fail to start", err)
	}

	// The interface's retry key calls Start again on the same engine: it
	// meets the same failure, not its own lock.
	if err := failing.eng.Start(context.Background()); IsAlreadyRunning(err) || err == nil || !strings.Contains(err.Error(), "start proxy") {
		t.Fatalf("a second Start on the same engine = %v, want the proxy to fail again", err)
	}

	next := buildHarness(t, instanceConfig(t), func(o *Options) { o.RunDir = failing.runDir })
	if err := next.eng.Start(context.Background()); err != nil {
		t.Fatalf("Start after a failed proxy start: %v", err)
	}
}

// InstanceRunning says whether the run directory's lock is held, and leaves
// it free for the next engine when it is not.
func TestInstanceRunningProbesTheLock(t *testing.T) {
	dir := t.TempDir()
	if up, err := InstanceRunning(dir); up || err != nil {
		t.Fatalf("with no lock file: %v, %v", up, err)
	}
	release, err := lockInstance(dir)
	if err != nil {
		t.Fatal(err)
	}
	if up, err := InstanceRunning(dir); !up || err != nil {
		t.Fatalf("with the lock held: %v, %v", up, err)
	}
	release()
	if up, err := InstanceRunning(dir); up || err != nil {
		t.Fatalf("after the release: %v, %v", up, err)
	}
	again, err := lockInstance(dir)
	if err != nil {
		t.Fatalf("the probe left the lock held: %v", err)
	}
	again()
}
