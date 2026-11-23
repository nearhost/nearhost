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
	want := []string{"systemctl --user daemon-reload", "systemctl --user enable nearhost-test.service"}
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

func TestStartCommands(t *testing.T) {
	f := &fakeRunner{}
	if err := New(testNames, t.TempDir(), f.run).Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"systemctl --user daemon-reload", "systemctl --user start nearhost-test.service"}
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

func TestNoUserManager(t *testing.T) {
	home := t.TempDir()
	f := &fakeRunner{handle: func(string) ([]byte, error) {
		return []byte("Failed to connect to bus: No medium found\n"), errors.New("exit status 1")
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
