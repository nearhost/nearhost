//go:build darwin

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func plistPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", "dev.nearhost.test.plist")
}

func TestInstallThenInstalled(t *testing.T) {
	home := t.TempDir()
	f := &fakeRunner{}
	m := New(testNames, home, f.run)
	d := plain()
	changed, err := m.Install(context.Background(), d)
	if err != nil || !changed {
		t.Fatalf("Install = %v, %v; want true, nil", changed, err)
	}
	if len(f.calls) != 0 {
		t.Errorf("Install ran %q; it must not start the core", f.calls)
	}
	fi, err := os.Stat(plistPath(home))
	if err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("plist = %v, %v; want mode 0644", fi, err)
	}
	got, ok, err := m.Installed(context.Background())
	if err != nil || !ok || !reflect.DeepEqual(got, d.For(Plist)) {
		t.Errorf("Installed = %+v, %v, %v; want %+v", got, ok, err, d.For(Plist))
	}
}

func TestInstallUnchanged(t *testing.T) {
	f := &fakeRunner{}
	m := New(testNames, t.TempDir(), f.run)
	// Graphical is not in a plist, so it must not make a definition look changed.
	d := plain()
	if _, err := m.Install(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	changed, err := m.Install(context.Background(), d)
	if err != nil || changed || len(f.calls) != 0 {
		t.Errorf("Install = %v, %v, commands %q; want false, nil, none", changed, err, f.calls)
	}
}

// A definition that differs in a field the plist carries is written again.
func TestInstallChanged(t *testing.T) {
	f := &fakeRunner{}
	m := New(testNames, t.TempDir(), f.run)
	d := plain()
	if _, err := m.Install(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	d.Exe = "/opt/homebrew/bin/nearhost"
	d.Env = append(d.Env, EnvVar{"DOCKER_HOST", "unix:///var/run/docker.sock"})
	changed, err := m.Install(context.Background(), d)
	if err != nil || !changed || len(f.calls) != 0 {
		t.Fatalf("Install = %v, %v, commands %q; want true, nil, none", changed, err, f.calls)
	}
	got, ok, err := m.Installed(context.Background())
	if err != nil || !ok || !reflect.DeepEqual(got, d.For(Plist)) {
		t.Errorf("Installed = %+v, %v, %v; want %+v", got, ok, err, d.For(Plist))
	}
}

func TestStartCommands(t *testing.T) {
	home := t.TempDir()
	f := &fakeRunner{handle: func(call string) ([]byte, error) {
		if call == fmt.Sprintf("launchctl bootout gui/%d/dev.nearhost.test", os.Getuid()) {
			return []byte("Boot-out failed: 5: Input/output error\n"), errors.New("exit status 5")
		}
		return nil, nil
	}}
	if err := New(testNames, home, f.run).Start(context.Background()); err != nil {
		t.Fatalf("Start must ignore a service that was not loaded: %v", err)
	}
	uid := os.Getuid()
	want := []string{
		fmt.Sprintf("launchctl bootout gui/%d/dev.nearhost.test", uid),
		fmt.Sprintf("launchctl bootstrap gui/%d %s", uid, plistPath(home)),
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("commands = %q, want %q", f.calls, want)
	}
}

func TestUninstallWhenNone(t *testing.T) {
	home := t.TempDir()
	f := &fakeRunner{handle: func(string) ([]byte, error) {
		return []byte("Boot-out failed: 113: Could not find service\n"), errors.New("exit status 113")
	}}
	if err := New(testNames, home, f.run).Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plistPath(home)); !os.IsNotExist(err) {
		t.Errorf("plist left: %v", err)
	}
}

func TestUninstallRemovesFile(t *testing.T) {
	home := t.TempDir()
	f := &fakeRunner{}
	m := New(testNames, home, f.run)
	if _, err := m.Install(context.Background(), plain()); err != nil {
		t.Fatal(err)
	}
	if err := m.Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{fmt.Sprintf("launchctl bootout gui/%d/dev.nearhost.test", os.Getuid())}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("commands = %q, want %q", f.calls, want)
	}
	if _, err := os.Stat(plistPath(home)); !os.IsNotExist(err) {
		t.Errorf("plist left: %v", err)
	}
}

func TestActiveParses(t *testing.T) {
	running := "dev.nearhost.test = {\n\tactive count = 1\n\tstate = running\n\tpid = 412\n}\n"
	waiting := "dev.nearhost.test = {\n\tactive count = 0\n\tstate = not running\n}\n"
	for name, c := range map[string]struct {
		out  string
		err  error
		want bool
	}{
		"running":    {running, nil, true},
		"not":        {waiting, nil, false},
		"not loaded": {"Could not find service \"dev.nearhost.test\" in domain for user gui: 501\n", errors.New("exit status 113"), false},
	} {
		f := &fakeRunner{handle: func(string) ([]byte, error) { return []byte(c.out), c.err }}
		got, err := New(testNames, t.TempDir(), f.run).Active(context.Background())
		if err != nil || got != c.want {
			t.Errorf("%s: Active = %v, %v; want %v", name, got, err, c.want)
		}
	}
}

// Only a service launchd cannot find is inactive: any other failure of
// launchctl print says nothing of whether the core runs.
func TestActiveFailure(t *testing.T) {
	f := &fakeRunner{handle: func(string) ([]byte, error) {
		return []byte("Bad request.\n"), errors.New("exit status 5")
	}}
	if _, err := New(testNames, t.TempDir(), f.run).Active(context.Background()); err == nil || !strings.Contains(err.Error(), "Bad request.") {
		t.Errorf("Active error = %v, want launchctl's output", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f = &fakeRunner{handle: func(string) ([]byte, error) { return nil, context.Canceled }}
	if _, err := New(testNames, t.TempDir(), f.run).Active(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Active error = %v, want context.Canceled", err)
	}
}

func TestManagerEnv(t *testing.T) {
	f := &fakeRunner{handle: func(string) ([]byte, error) { return []byte("/private/tmp/com.apple.launchd.x/Listeners\n"), nil }}
	env := New(testNames, t.TempDir(), f.run).ManagerEnv(context.Background())
	if env["SSH_AUTH_SOCK"] != "/private/tmp/com.apple.launchd.x/Listeners" {
		t.Errorf("env = %v", env)
	}
}

func TestStartRetriesBootstrap(t *testing.T) {
	failures := 3
	f := &fakeRunner{handle: func(call string) ([]byte, error) {
		if strings.Contains(call, "bootstrap") && failures > 0 {
			failures--
			return []byte("Bootstrap failed: 5: Input/output error\n"), errors.New("exit status 5")
		}
		return nil, nil
	}}
	m := New(testNames, t.TempDir(), f.run)
	var slept []time.Duration
	m.(*launchdManager).sleep = func(_ context.Context, d time.Duration) { slept = append(slept, d) }
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(slept) != 3 {
		t.Errorf("slept %v, want 3 waits", slept)
	}
}

func TestStartGivesUpOnBootstrap(t *testing.T) {
	f := &fakeRunner{handle: func(call string) ([]byte, error) {
		if strings.Contains(call, "bootstrap") {
			return []byte("Bootstrap failed: 37: Operation already in progress\n"), errors.New("exit status 37")
		}
		return nil, nil
	}}
	m := New(testNames, t.TempDir(), f.run)
	m.(*launchdManager).sleep = func(context.Context, time.Duration) {}
	if err := m.Start(context.Background()); err == nil {
		t.Error("Start succeeded, want an error")
	}
	// A different failure is not retried.
	f2 := &fakeRunner{handle: func(call string) ([]byte, error) {
		if strings.Contains(call, "bootstrap") {
			return []byte("Bootstrap failed: 119: Service is disabled\n"), errors.New("exit status 119")
		}
		return nil, nil
	}}
	m = New(testNames, t.TempDir(), f2.run)
	m.(*launchdManager).sleep = func(context.Context, time.Duration) { t.Error("retried") }
	if err := m.Start(context.Background()); err == nil {
		t.Error("Start succeeded, want an error")
	}
}
