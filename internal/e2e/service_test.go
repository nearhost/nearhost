package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/service"
	"github.com/nearhost/nearhost/internal/testutil"
)

// This journey installs a real login service in the machine's own service
// manager, so it is opt-in: set NEARHOST_SERVICE_E2E=1. It uses names of its
// own and never touches the real service. It is run by hand on each of
// macOS, Linux and Windows, where CI has no user session to install into.

// serviceNames are the names the journey's service has, apart from the
// real one's.
var serviceNames = service.Names{Launchd: "dev.nearhost.e2e", Systemd: "nearhost-e2e.service", Task: "Nearhost e2e"}

// restartWindow is how long a core killed hard may take to be back. Task
// Scheduler restarts a failed task after a minute (RestartOnFailure PT1M).
func restartWindow() time.Duration {
	if runtime.GOOS == "windows" {
		return 90 * time.Second
	}
	return 20 * time.Second
}

// waitCore waits for a core to answer in the sandbox, for up to d, and
// returns what it says. It returns false when none does.
func (s *sandbox) waitCore(d time.Duration, ok func(control.Status) bool) (control.Status, bool) {
	s.t.Helper()
	deadline := time.Now().Add(d)
	for {
		out, code := s.nearhost("status", "--json")
		if code == 0 {
			var st control.Status
			if err := json.Unmarshal([]byte(out), &st); err == nil && ok(st) {
				return st, true
			}
		}
		if time.Now().After(deadline) {
			return control.Status{}, false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestServiceJourney(t *testing.T) {
	if os.Getenv("NEARHOST_SERVICE_E2E") != "1" {
		t.Skip("set NEARHOST_SERVICE_E2E=1 to install a real login service and remove it again")
	}
	sb := newSandbox(t, fmt.Sprintf(localShop, t.TempDir(), testutil.Upstream(t, "shop")))
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	// The Linux manager finds its unit folder through XDG_CONFIG_HOME in this
	// process, so it is left alone here: the service gets the sandbox's
	// folders through its own environment instead.
	def := service.Definition{
		Exe:  nearhostBin,
		Args: sb.serveArgs(),
		Env: []service.EnvVar{
			{Name: "HOME", Value: sb.dir},
			{Name: "XDG_CONFIG_HOME", Value: sb.dir},
			{Name: "XDG_DATA_HOME", Value: sb.dir + "/d"},
			{Name: "PATH", Value: os.Getenv("PATH")},
		},
		LogPath: sb.dir + "/service.log",
	}
	if runtime.GOOS == "windows" {
		// A scheduled task carries no environment: it runs with the user's own,
		// and so does nearhost here, which must find no other core running.
		sb.env = append(os.Environ(), "NEARHOST_NO_UPDATE_CHECK=1")
		if _, code := sb.nearhost("status"); code != 3 {
			t.Skip("a Nearhost core is running; stop it first, as this journey uses the user's own folders on Windows")
		}
	}

	ctx := context.Background()
	mgr := service.New(serviceNames, home, nil)
	t.Cleanup(func() {
		// Stop the core first, as the caller of Uninstall must.
		sb.nearhost("stop")
		if err := mgr.Uninstall(ctx); err != nil {
			t.Errorf("uninstall: %v", err)
		}
		sb.stopAnyCore()
	})

	if changed, err := mgr.Install(ctx, def); err != nil || !changed {
		t.Fatalf("install: changed %v, %v", changed, err)
	}
	if _, ok, err := mgr.Installed(ctx); err != nil || !ok {
		t.Fatalf("installed: ok %v, %v", ok, err)
	}

	// 1. Started, the service's core answers and says the service started it.
	started := time.Now()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	st, ok := sb.waitCore(15*time.Second, func(control.Status) bool { return true })
	if !ok {
		t.Fatalf("no core answers 15 s after start; the core log:\n%s", sb.coreLog())
	}
	t.Logf("the core answered %s after start (pid %d)", time.Since(started).Round(10*time.Millisecond), st.Info.PID)
	if st.Info.StartedBy != control.StartedService {
		t.Errorf("the core says it was started by %q, want service", st.Info.StartedBy)
	}
	if active, err := mgr.Active(ctx); err != nil || !active {
		t.Errorf("active after start: %v, %v", active, err)
	}

	// 2. A clean stop is not restarted.
	if out, code := sb.nearhost("stop"); code != 0 || out != "Stopped.\n" {
		t.Fatalf("nearhost stop: exit %d, printed %q", code, out)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if active, err := mgr.Active(ctx); err != nil || active {
			t.Fatalf("active %v (%v) after a clean stop", active, err)
		}
		if _, code := sb.nearhost("status"); code != 3 {
			t.Fatalf("a core answers after a clean stop")
		}
		time.Sleep(500 * time.Millisecond)
	}

	// 3. A core killed hard is restarted by the manager.
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("start again: %v", err)
	}
	st, ok = sb.waitCore(15*time.Second, func(control.Status) bool { return true })
	if !ok {
		t.Fatalf("no core answers 15 s after the second start; the core log:\n%s", sb.coreLog())
	}
	old := st.Info.PID
	p, err := os.FindProcess(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Kill(); err != nil {
		t.Fatalf("kill the core (pid %d): %v", old, err)
	}
	killed := time.Now()
	window := restartWindow()
	st, ok = sb.waitCore(window, func(st control.Status) bool { return st.Info.PID != old })
	if !ok {
		t.Fatalf("no new core answers %s after pid %d was killed; the core log:\n%s", window, old, sb.coreLog())
	}
	t.Logf("a new core (pid %d) answered %s after pid %d was killed", st.Info.PID, time.Since(killed).Round(10*time.Millisecond), old)
	if st.Info.StartedBy != control.StartedService {
		t.Errorf("the restarted core says it was started by %q, want service", st.Info.StartedBy)
	}

	// 4. Removed once its core is stopped.
	if out, code := sb.nearhost("stop"); code != 0 || out != "Stopped.\n" {
		t.Fatalf("nearhost stop: exit %d, printed %q", code, out)
	}
	if err := mgr.Uninstall(ctx); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, ok, err := mgr.Installed(ctx); err != nil || ok {
		t.Errorf("installed after uninstall: ok %v, %v", ok, err)
	}
	if active, _ := mgr.Active(ctx); active {
		t.Errorf("active after uninstall")
	}
	if _, code := sb.nearhost("status"); code != 3 {
		t.Errorf("a core answers after uninstall")
	}
}
