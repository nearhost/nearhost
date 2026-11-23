//go:build linux

package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// noBus matches what systemctl --user says when no user manager answers: the
// older "Failed to connect to bus" and, from systemd 248, "Failed to connect
// to user scope bus via local transport".
var noBus = regexp.MustCompile(`Failed to connect to (user scope )?bus`)

type systemdManager struct {
	names Names
	path  string // the unit file
	run   Runner
}

func newManager(names Names, home string, run Runner) Manager {
	// systemd reads user units from $XDG_CONFIG_HOME when that is absolute.
	cfg := filepath.Join(home, ".config")
	if x := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(x) {
		cfg = x
	}
	return &systemdManager{names, filepath.Join(cfg, "systemd", "user", names.Systemd), run}
}

// systemctl runs systemctl --user. A failure to reach the user manager is
// ErrNoUserManager; any other failure carries the command's output.
func (m *systemdManager) systemctl(ctx context.Context, args ...string) ([]byte, error) {
	out, err := m.run(ctx, "systemctl", append([]string{"--user"}, args...)...)
	if err != nil {
		if noBus.Match(out) {
			return out, ErrNoUserManager
		}
		return out, fmt.Errorf("systemctl --user %s: %w: %s", strings.Join(args, " "), err, output(out))
	}
	return out, nil
}

func (m *systemdManager) Installed(context.Context) (Definition, bool, error) {
	b, err := os.ReadFile(m.path)
	if errors.Is(err, fs.ErrNotExist) {
		return Definition{}, false, nil
	}
	if err != nil {
		return Definition{}, false, err
	}
	d, err := ParseUnit(b)
	if err != nil {
		return Definition{}, false, fmt.Errorf("read %s: %w", m.path, err)
	}
	return d, true, nil
}

func (m *systemdManager) Install(ctx context.Context, d Definition) (bool, error) {
	got, ok, err := m.Installed(ctx)
	if unchanged(got, ok, d, Unit) {
		return false, nil
	}
	// A unit we cannot read is replaced, as a changed one is.
	fresh := !ok && err == nil
	if err := writeFileAtomic(m.path, RenderUnit(d), 0o644); err != nil {
		return false, err
	}
	enable := "reenable"
	if fresh {
		enable = "enable"
	}
	for _, args := range [][]string{{"daemon-reload"}, {enable, m.names.Systemd}} {
		if _, err := m.systemctl(ctx, args...); err != nil {
			if fresh {
				_ = os.Remove(m.path)
			}
			return false, err
		}
	}
	return true, nil
}

func (m *systemdManager) Uninstall(ctx context.Context) error {
	if _, err := os.Stat(m.path); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	// Removing the file is the point, so a manager that will not answer
	// does not stop it.
	_, _ = m.systemctl(ctx, "disable", m.names.Systemd)
	if err := os.Remove(m.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	_, _ = m.systemctl(ctx, "daemon-reload")
	return nil
}

func (m *systemdManager) Start(ctx context.Context) error {
	for _, args := range [][]string{{"daemon-reload"}, {"start", m.names.Systemd}} {
		if _, err := m.systemctl(ctx, args...); err != nil {
			return err
		}
	}
	return nil
}

func (m *systemdManager) Active(ctx context.Context) (bool, error) {
	out, err := m.run(ctx, "systemctl", "--user", "is-active", m.names.Systemd)
	if noBus.Match(out) {
		return false, ErrNoUserManager
	}
	// is-active exits non-zero for every state but active.
	_ = err
	return strings.TrimSpace(string(out)) == "active", nil
}

func (m *systemdManager) ManagerEnv(ctx context.Context) map[string]string {
	out, err := m.systemctl(ctx, "show-environment")
	if err != nil {
		return nil
	}
	env := map[string]string{}
	for line := range strings.SplitSeq(string(out), "\n") {
		name, val, ok := strings.Cut(strings.TrimRight(line, "\r"), "=")
		if !ok {
			continue
		}
		// systemd quotes values it cannot print plainly as $'...'.
		if v, ok := strings.CutPrefix(val, "$'"); ok {
			val = strings.TrimSuffix(v, "'")
		}
		env[name] = val
	}
	return env
}

func (m *systemdManager) Graphical(ctx context.Context) bool {
	out, _ := m.run(ctx, "systemctl", "--user", "is-active", "graphical-session.target")
	return strings.TrimSpace(string(out)) == "active"
}
