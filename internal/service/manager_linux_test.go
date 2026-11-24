//go:build linux

package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInstallThenInstalled(t *testing.T) {
	home := t.TempDir()
	f := &fakeRunner{}
	m := New(testNames, home, f.run)
	d := plain()
	changed, err := m.Install(context.Background(), d)
	if err != nil || !changed {
		t.Fatalf("Install = %v, %v; want true, nil", changed, err)
	}
	// The unit folder is the manager's, asked for once.
	want := []string{"systemctl --user show-environment", "systemctl --user daemon-reload", "systemctl --user enable nearhost-test.service"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("commands = %q, want %q", f.calls, want)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "systemd", "user", "nearhost-test.service")); err != nil {
		t.Error(err)
	}
	got, ok, err := m.Installed(context.Background())
	if err != nil || !ok || !reflect.DeepEqual(got, d.For(Unit)) {
		t.Errorf("Installed = %+v, %v, %v; want %+v", got, ok, err, d)
	}
}

func TestInstallUnchanged(t *testing.T) {
	f := &fakeRunner{}
	m := New(testNames, t.TempDir(), f.run)
	d := plain()
	if _, err := m.Install(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	f.reset()
	changed, err := m.Install(context.Background(), d)
	if err != nil || changed || len(f.calls) != 0 {
		t.Errorf("Install = %v, %v, commands %q; want false, nil, none", changed, err, f.calls)
	}
}

// A unit that says the same definition in other words, as one written by
// an earlier release without the StartLimit lines, is rewritten: only the
// file as this release renders it is up to date.
func TestInstallRewritesAUnitOfOtherText(t *testing.T) {
	home := t.TempDir()
	f := &fakeRunner{}
	m := New(testNames, home, f.run)
	d := plain()
	if _, err := m.Install(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".config", "systemd", "user", "nearhost-test.service")
	older := strings.Replace(string(RenderUnit(d)), "StartLimitIntervalSec=60\nStartLimitBurst=5\n", "", 1)
	if older == string(RenderUnit(d)) {
		t.Fatal("the unit has no StartLimit lines to take out")
	}
	if err := os.WriteFile(path, []byte(older), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := m.Installed(context.Background()); err != nil || !ok || !reflect.DeepEqual(got, d.For(Unit)) {
		t.Fatalf("the older unit reads as %+v, %v, %v; want the same definition", got, ok, err)
	}
	f.reset()
	changed, err := m.Install(context.Background(), d)
	if err != nil || !changed {
		t.Fatalf("Install = %v, %v; want true, nil", changed, err)
	}
	if b, _ := os.ReadFile(path); string(b) != string(RenderUnit(d)) {
		t.Errorf("unit %q, want %q", b, RenderUnit(d))
	}
	want := []string{"systemctl --user daemon-reload", "systemctl --user reenable nearhost-test.service"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("commands = %q, want %q", f.calls, want)
	}
}

func TestInstallChangedReenables(t *testing.T) {
	f := &fakeRunner{}
	m := New(testNames, t.TempDir(), f.run)
	d := plain()
	if _, err := m.Install(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	f.reset()
	d.Graphical = false
	changed, err := m.Install(context.Background(), d)
	if err != nil || !changed {
		t.Fatalf("Install = %v, %v; want true, nil", changed, err)
	}
	want := []string{"systemctl --user daemon-reload", "systemctl --user reenable nearhost-test.service"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("commands = %q, want %q", f.calls, want)
	}
}

// A changed unit that systemd fails to take is put back as it was, so the
// next install sees the change again and retries what failed.
func TestInstallChangedFailureRestoresTheUnit(t *testing.T) {
	for _, failing := range []string{"daemon-reload", "reenable nearhost-test.service"} {
		t.Run(failing, func(t *testing.T) {
			fail := false
			f := &fakeRunner{handle: func(call string) ([]byte, error) {
				if fail && strings.HasSuffix(call, " "+failing) {
					return []byte("Failed\n"), errors.New("exit status 1")
				}
				return nil, nil
			}}
			m := New(testNames, t.TempDir(), f.run)
			d := plain()
			if _, err := m.Install(context.Background(), d); err != nil {
				t.Fatal(err)
			}
			next := d
			next.Exe = "/opt/nearhost/bin/nearhost"
			fail = true
			if _, err := m.Install(context.Background(), next); err == nil {
				t.Fatalf("Install succeeded, want the %s failure", failing)
			}
			if got, ok, err := m.Installed(context.Background()); err != nil || !ok || !reflect.DeepEqual(got, d.For(Unit)) {
				t.Errorf("Installed = %+v, %v, %v; want the unit as it was, %+v", got, ok, err, d.For(Unit))
			}
			fail = false
			f.reset()
			changed, err := m.Install(context.Background(), next)
			if err != nil || !changed || len(f.calls) != 2 {
				t.Errorf("retry: Install = %v, %v, commands %q; want true, nil, the reload and reenable", changed, err, f.calls)
			}
		})
	}
}

// A write that fails once the new unit is in place, as when the folder
// cannot be synced, puts the unit back too.
func TestInstallWriteFailureRestoresTheUnit(t *testing.T) {
	m := New(testNames, t.TempDir(), (&fakeRunner{}).run)
	d := plain()
	if _, err := m.Install(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	before := writeFile
	t.Cleanup(func() { writeFile = before })
	writes := 0
	writeFile = func(path string, b []byte, perm os.FileMode) error {
		writes++
		if err := before(path, b, perm); err != nil {
			return err
		}
		if writes == 1 {
			return errors.New("sync the folder: input/output error")
		}
		return nil
	}
	next := d
	next.Exe = "/opt/nearhost/bin/nearhost"
	if _, err := m.Install(context.Background(), next); err == nil {
		t.Fatal("Install succeeded, want the write failure")
	}
	if got, ok, err := m.Installed(context.Background()); err != nil || !ok || !reflect.DeepEqual(got, d.For(Unit)) {
		t.Errorf("Installed = %+v, %v, %v; want the unit as it was, %+v", got, ok, err, d.For(Unit))
	}
}

// A unit that cannot be put back is no silent failure: the error says to
// remove it and install again.
func TestInstallSaysWhenTheUnitCannotBePutBack(t *testing.T) {
	f := &fakeRunner{}
	m := New(testNames, t.TempDir(), f.run)
	d := plain()
	if _, err := m.Install(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	before := writeFile
	t.Cleanup(func() { writeFile = before })
	writes := 0
	writeFile = func(path string, b []byte, perm os.FileMode) error {
		if writes++; writes > 1 {
			return errors.New("read-only file system")
		}
		return before(path, b, perm)
	}
	f.handle = func(call string) ([]byte, error) {
		if strings.HasSuffix(call, "daemon-reload") {
			return []byte("Failed\n"), errors.New("exit status 1")
		}
		return nil, nil
	}
	next := d
	next.Exe = "/opt/nearhost/bin/nearhost"
	_, err := m.Install(context.Background(), next)
	if err == nil || !strings.Contains(err.Error(), "run nearhost service install again") || !strings.Contains(err.Error(), "read-only file system") {
		t.Errorf("Install error = %v, want one that says the unit could not be put back", err)
	}
}

// A unit that failed too often in a row is not started again until its
// failure is reset: Start resets it first, and a reset that fails, as for
// a unit that never failed on an older systemd, does not stop the start.
func TestStartCommands(t *testing.T) {
	f := &fakeRunner{handle: func(call string) ([]byte, error) {
		if strings.Contains(call, "reset-failed") {
			return []byte("Unit nearhost-test.service not loaded."), errors.New("exit status 1")
		}
		return nil, nil
	}}
	if err := New(testNames, t.TempDir(), f.run).Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"systemctl --user daemon-reload", "systemctl --user reset-failed nearhost-test.service", "systemctl --user start nearhost-test.service"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("commands = %q, want %q", f.calls, want)
	}
}

func TestUninstall(t *testing.T) {
	home := t.TempDir()
	f := &fakeRunner{}
	m := New(testNames, home, f.run)
	if err := m.Uninstall(context.Background()); err != nil {
		t.Fatalf("none installed: %v", err)
	}
	if _, err := m.Install(context.Background(), plain()); err != nil {
		t.Fatal(err)
	}
	f.reset()
	if err := m.Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"systemctl --user disable nearhost-test.service", "systemctl --user daemon-reload"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("commands = %q, want %q", f.calls, want)
	}
	if _, ok, _ := m.Installed(context.Background()); ok {
		t.Error("still installed")
	}
}

func TestUninstallWhenNone(t *testing.T) {
	home := t.TempDir()
	f := &fakeRunner{}
	if err := New(testNames, home, f.run).Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "systemd", "user", "nearhost-test.service")); !os.IsNotExist(err) {
		t.Errorf("unit file left: %v", err)
	}
}

func TestActiveParses(t *testing.T) {
	for out, want := range map[string]bool{"active\n": true, "inactive\n": false, "failed\n": false} {
		f := &fakeRunner{handle: func(string) ([]byte, error) {
			if want {
				return []byte(out), nil
			}
			return []byte(out), errors.New("exit status 3")
		}}
		got, err := New(testNames, t.TempDir(), f.run).Active(context.Background())
		if err != nil || got != want {
			t.Errorf("Active(%q) = %v, %v; want %v", out, got, err, want)
		}
	}
}

// A failure that prints no unit state says nothing of whether the core runs.
func TestActiveFailure(t *testing.T) {
	for _, out := range []string{"", "Access denied\n"} {
		f := &fakeRunner{handle: func(string) ([]byte, error) { return []byte(out), errors.New("exit status 1") }}
		if _, err := New(testNames, t.TempDir(), f.run).Active(context.Background()); err == nil {
			t.Errorf("Active(%q) error = nil, want one", out)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fakeRunner{handle: func(string) ([]byte, error) { return nil, context.Canceled }}
	if _, err := New(testNames, t.TempDir(), f.run).Active(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Active error = %v, want context.Canceled", err)
	}
}

func TestNoUserManager(t *testing.T) {
	for _, msg := range []string{
		"Failed to connect to bus: No medium found\n",
		"Failed to connect to user scope bus via local transport: $DBUS_SESSION_BUS_ADDRESS and $XDG_RUNTIME_DIR not defined (consider using --machine=<user>@.host --user to connect to bus of other user)\n",
	} {
		testNoUserManager(t, msg)
	}
}

func testNoUserManager(t *testing.T, msg string) {
	home := t.TempDir()
	f := &fakeRunner{handle: func(string) ([]byte, error) {
		return []byte(msg), errors.New("exit status 1")
	}}
	m := New(testNames, home, f.run)
	if _, err := m.Install(context.Background(), plain()); !errors.Is(err, ErrNoUserManager) {
		t.Errorf("Install error = %v, want ErrNoUserManager", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "systemd", "user", "nearhost-test.service")); !os.IsNotExist(err) {
		t.Errorf("unit file left behind: %v", err)
	}
	if _, err := m.Active(context.Background()); !errors.Is(err, ErrNoUserManager) {
		t.Errorf("Active error = %v, want ErrNoUserManager", err)
	}
	if err := m.Start(context.Background()); !errors.Is(err, ErrNoUserManager) {
		t.Errorf("Start error = %v, want ErrNoUserManager", err)
	}
}

func TestManagerEnvAndGraphical(t *testing.T) {
	f := &fakeRunner{handle: func(call string) ([]byte, error) {
		if strings.HasSuffix(call, "show-environment") {
			return []byte("LANG=en_GB.UTF-8\nSSH_AUTH_SOCK=/run/user/1000/ssh-agent.socket\nODD=$'a b'\n"), nil
		}
		return []byte("active\n"), nil
	}}
	m := New(testNames, t.TempDir(), f.run)
	env := m.ManagerEnv(context.Background())
	if env["SSH_AUTH_SOCK"] != "/run/user/1000/ssh-agent.socket" || env["ODD"] != "a b" || env["LANG"] != "en_GB.UTF-8" {
		t.Errorf("env = %v", env)
	}
	if !m.Graphical(context.Background()) {
		t.Error("Graphical = false, want true")
	}
}

func TestUnitDirFollowsTheManagersXDGConfigHome(t *testing.T) {
	home, cfg, ours := t.TempDir(), t.TempDir(), t.TempDir()
	// This process's own value is not the manager's, and is not used.
	t.Setenv("XDG_CONFIG_HOME", ours)
	managerEnv := "XDG_CONFIG_HOME=" + cfg + "\n"
	f := &fakeRunner{handle: func(call string) ([]byte, error) {
		if strings.HasSuffix(call, "show-environment") {
			return []byte(managerEnv), nil
		}
		return nil, nil
	}}
	if _, err := New(testNames, home, f.run).Install(context.Background(), plain()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfg, "systemd", "user", "nearhost-test.service")); err != nil {
		t.Error(err)
	}
	// Unset in the manager, or relative, which systemd ignores, it is the
	// home folder's.
	for _, env := range []string{"", "XDG_CONFIG_HOME=relative\n"} {
		managerEnv = env
		home := t.TempDir()
		if _, err := New(testNames, home, f.run).Install(context.Background(), plain()); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(home, ".config", "systemd", "user", "nearhost-test.service")); err != nil {
			t.Errorf("manager env %q: %v", env, err)
		}
	}
	if _, err := os.Stat(filepath.Join(ours, "systemd")); !os.IsNotExist(err) {
		t.Errorf("a unit was written under this process's XDG_CONFIG_HOME: %v", err)
	}
}

// A manager that does not answer leaves the unit folder to be asked again:
// the home folder's stands in only until it does.
func TestUnitDirAsksAgainAfterNoAnswer(t *testing.T) {
	home, cfg := t.TempDir(), t.TempDir()
	asked := 0
	f := &fakeRunner{handle: func(call string) ([]byte, error) {
		if !strings.HasSuffix(call, "show-environment") {
			return nil, nil
		}
		asked++
		if asked == 1 {
			return []byte("Failed to connect to bus: No medium found\n"), errors.New("exit status 1")
		}
		return []byte("XDG_CONFIG_HOME=" + cfg + "\n"), nil
	}}
	m := New(testNames, home, f.run).(*systemdManager)
	if got, want := m.unitPath(context.Background()), filepath.Join(home, ".config", "systemd", "user", "nearhost-test.service"); got != want {
		t.Errorf("with no answer: %q, want %q", got, want)
	}
	want := filepath.Join(cfg, "systemd", "user", "nearhost-test.service")
	for range 2 {
		if got := m.unitPath(context.Background()); got != want {
			t.Errorf("once it answers: %q, want %q", got, want)
		}
	}
	if asked != 2 {
		t.Errorf("show-environment asked %d times, want 2: again after no answer, then not again", asked)
	}
}

// A unit that exists but cannot be parsed, as one edited by hand, is
// ErrUnreadable, so uninstall can still remove it.
func TestUnreadableUnit(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".config", "systemd", "user", "nearhost-test.service")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[Service]\nExecStart=/bin/true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeRunner{}
	m := New(testNames, home, f.run)
	if _, ok, err := m.Installed(context.Background()); ok || !errors.Is(err, ErrUnreadable) {
		t.Fatalf("Installed = %v, %v; want ErrUnreadable", ok, err)
	}
	if err := m.Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("unit file left: %v", err)
	}
}

// A unit that cannot be read for another reason is not ErrUnreadable.
func TestUnitReadErrorIsNotUnreadable(t *testing.T) {
	home := t.TempDir()
	// A folder where the file should be cannot be read as one.
	if err := os.MkdirAll(filepath.Join(home, ".config", "systemd", "user", "nearhost-test.service"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := New(testNames, home, (&fakeRunner{}).run).Installed(context.Background()); err == nil || errors.Is(err, ErrUnreadable) {
		t.Errorf("Installed error = %v, want one that is not ErrUnreadable", err)
	}
}
