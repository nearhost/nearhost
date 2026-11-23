package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// restartWindow is how long a core killed hard may take to be back: launchd
// waits its ThrottleInterval of 10 s before it starts a job again.
const restartWindow = 30 * time.Second

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
	if runtime.GOOS == "windows" {
		t.Skip("a logon task runs with the user's own environment, so its core cannot be kept apart from the user's real config, CA and grants; check Windows by hand")
	}
	sb := newSandbox(t, fmt.Sprintf(localShop, t.TempDir(), testutil.Upstream(t, "shop")))
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	mgr := service.New(serviceNames, home, nil)
	// The service's binary stays put for the whole journey: TestMain's folder
	// goes when the package's tests end, which a failed Uninstall would
	// outlive. The copy is removed after Uninstall.
	scratch := filepath.Join(home, ".nht")
	// The journey removes its service when it ends, so it refuses to start
	// over one it did not install: only an earlier run's, which runs a copy
	// in scratch, is replaced.
	if d, ok, err := mgr.Installed(ctx); err != nil || ok && !strings.HasPrefix(d.Exe, filepath.Join(scratch, "svc-e2e-")) {
		t.Fatalf("a service named %v is installed that this journey did not install (%+v, %v); remove it first", serviceNames, d, err)
	} else if ok {
		// An interrupted run's core may still run: systemd would not start
		// the new one over it, so the journey would watch the old core.
		if active, err := mgr.Active(ctx); err != nil || active {
			t.Fatalf("an earlier run's service still runs (active %v, %v); stop it first with systemctl --user stop %s or launchctl bootout gui/%d/%s", active, err, serviceNames.Systemd, os.Getuid(), serviceNames.Launchd)
		}
	}
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	binDir, err := os.MkdirTemp(scratch, "svc-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(binDir, filepath.Base(nearhostBin))
	src, err := os.ReadFile(nearhostBin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, src, 0o755); err != nil {
		t.Fatal(err)
	}
	// The service gets the sandbox's folders through its own environment.
	def := service.Definition{
		Exe:  exe,
		Args: sb.serveArgs(),
		Env: []service.EnvVar{
			{Name: "HOME", Value: sb.dir},
			{Name: "XDG_CONFIG_HOME", Value: sb.dir},
			{Name: "XDG_DATA_HOME", Value: sb.dir + "/d"},
			{Name: "PATH", Value: os.Getenv("PATH")},
			{Name: "NEARHOST_NO_UPDATE_CHECK", Value: "1"},
		},
		LogPath: sb.dir + "/service.log",
	}

	t.Cleanup(func() {
		// Stop the core first, as the caller of Uninstall must.
		sb.nearhost("stop")
		if err := mgr.Uninstall(ctx); err != nil {
			t.Errorf("uninstall: %v", err)
		}
		sb.stopAnyCore()
		_ = os.RemoveAll(binDir)
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

	// 2. A clean stop is not restarted: the manager stops calling the
	// service active, and goes on not doing so.
	if out, code := sb.nearhost("stop"); code != 0 || out != "Stopped.\n" {
		t.Fatalf("nearhost stop: exit %d, printed %q", code, out)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		active, err := mgr.Active(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the service is still active 10 s after a clean stop; the core log:\n%s", sb.coreLog())
		}
		time.Sleep(200 * time.Millisecond)
	}
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(500 * time.Millisecond) {
		if active, err := mgr.Active(ctx); err != nil || active {
			t.Fatalf("active %v (%v) after a clean stop; the core log:\n%s", active, err, sb.coreLog())
		}
		if out, code := sb.nearhost("status"); code != 3 {
			t.Fatalf("nearhost status after a clean stop: exit %d, printed:\n%s\nthe core log:\n%s", code, out, sb.coreLog())
		}
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
	st, ok = sb.waitCore(restartWindow, func(st control.Status) bool { return st.Info.PID != old })
	if !ok {
		t.Fatalf("no new core answers %s after pid %d was killed; the core log:\n%s", restartWindow, old, sb.coreLog())
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
	if out, code := sb.nearhost("status"); code != 3 {
		t.Errorf("nearhost status after uninstall: exit %d, printed:\n%s", code, out)
	}
}
