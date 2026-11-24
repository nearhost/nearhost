//go:build darwin

package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type launchdManager struct {
	names Names
	path  string // the plist
	run   Runner
	sleep func(context.Context, time.Duration) // tests replace it
}

func newManager(names Names, home string, run Runner) Manager {
	return &launchdManager{names, filepath.Join(home, "Library", "LaunchAgents", names.Launchd+".plist"), run, sleepCtx}
}

// domain is the user's GUI domain, where login-time agents run.
func domain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

func (m *launchdManager) target() string { return domain() + "/" + m.names.Launchd }

func (m *launchdManager) Installed(context.Context) (Definition, bool, error) {
	b, err := os.ReadFile(m.path)
	if errors.Is(err, fs.ErrNotExist) {
		return Definition{}, false, nil
	}
	if err != nil {
		return Definition{}, false, err
	}
	d, err := ParsePlist(b)
	if err != nil {
		return Definition{}, false, fmt.Errorf("read %s: %w", m.path, Unreadable(err))
	}
	return d, true, nil
}

// Install writes the plist. launchd loads it at the next login (RunAtLoad),
// and Start loads it now.
func (m *launchdManager) Install(ctx context.Context, d Definition) (bool, error) {
	got, ok, _ := m.Installed(ctx)
	if unchanged(got, ok, d, Plist) {
		return false, nil
	}
	if err := writeFileAtomic(m.path, RenderPlist(m.names.Launchd, d), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func (m *launchdManager) Uninstall(ctx context.Context) error {
	// Not loaded is the usual answer, and removing the file is the point.
	_, _ = m.run(ctx, "launchctl", "bootout", m.target())
	if err := os.Remove(m.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (m *launchdManager) Start(ctx context.Context) error {
	// A job launchd reports as running is either the old core of a restart,
	// which frees its lock just before its process exits, or a core that is
	// starting at login. Wait to see which: one that stops is loaded again,
	// one that stays is left alone.
	running, _ := m.Active(ctx)
	for i := 0; running && i < 20; i++ {
		m.sleep(ctx, 100*time.Millisecond)
		running, _ = m.Active(ctx)
	}
	if running {
		return nil
	}
	_, _ = m.run(ctx, "launchctl", "bootout", m.target())
	// Right after a bootout launchd may still be tearing the service down
	// and answer with I/O error (5) or operation in progress (37), so try
	// again for a couple of seconds.
	var out []byte
	var err error
	for range 11 {
		out, err = m.run(ctx, "launchctl", "bootstrap", domain(), m.path)
		if err == nil {
			return nil
		}
		if !strings.Contains(string(out), "Input/output error") && !strings.Contains(string(out), "Operation already in progress") {
			break
		}
		m.sleep(ctx, 200*time.Millisecond)
	}
	return fmt.Errorf("launchctl bootstrap: %w: %s", err, output(out))
}

func (m *launchdManager) Active(ctx context.Context) (bool, error) {
	// print fails for a service that is not loaded, which is not running.
	// Any other failure says nothing either way.
	out, err := m.run(ctx, "launchctl", "print", m.target())
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if strings.Contains(string(out), "Could not find service") {
			return false, nil
		}
		return false, fmt.Errorf("launchctl print: %w: %s", err, output(out))
	}
	return strings.Contains(string(out), "state = running"), nil
}

func (m *launchdManager) ManagerEnv(ctx context.Context) map[string]string {
	out, err := m.run(ctx, "launchctl", "getenv", "SSH_AUTH_SOCK")
	if v := strings.TrimSpace(string(out)); err == nil && v != "" {
		return map[string]string{"SSH_AUTH_SOCK": v}
	}
	return nil
}

func (m *launchdManager) Graphical(context.Context) bool { return false }
