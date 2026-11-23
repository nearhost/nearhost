//go:build windows

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"strings"
	"syscall"
)

type taskManager struct {
	names Names
	run   Runner
}

func newManager(names Names, _ string, run Runner) Manager { return &taskManager{names, run} }

// notFound reports whether schtasks said the task does not exist. schtasks
// prints the system's message for ERROR_FILE_NOT_FOUND in the user's
// language, so that is matched first, with the English text as a fallback.
func notFound(out []byte) bool {
	s := strings.ToLower(string(out))
	return strings.Contains(s, strings.ToLower(syscall.Errno(2).Error())) || strings.Contains(s, "cannot find")
}

// errNotInstalled is what a command on a task that does not exist returns.
var errNotInstalled = errors.New("the Nearhost task is not installed")

func (m *taskManager) schtasks(ctx context.Context, args ...string) ([]byte, error) {
	out, err := m.run(ctx, "schtasks.exe", args...)
	if err != nil && !notFound(out) {
		return out, fmt.Errorf("schtasks %s: %w: %s", args[0], err, output(out))
	}
	return out, err
}

func (m *taskManager) Installed(ctx context.Context) (Definition, bool, error) {
	out, err := m.schtasks(ctx, "/Query", "/TN", m.names.Task, "/XML")
	if err != nil {
		if notFound(out) {
			return Definition{}, false, nil
		}
		return Definition{}, false, err
	}
	d, err := ParseTask(out)
	if err != nil {
		return Definition{}, false, fmt.Errorf("read task %s: %w", m.names.Task, Unreadable(err))
	}
	return d, true, nil
}

func (m *taskManager) Install(ctx context.Context, d Definition) (bool, error) {
	got, ok, _ := m.Installed(ctx)
	if unchanged(got, ok, d, Task) {
		return false, nil
	}
	u, err := user.Current()
	if err != nil {
		return false, err
	}
	f, err := os.CreateTemp("", "nearhost-task-*.xml")
	if err != nil {
		return false, err
	}
	defer os.Remove(f.Name())
	_, werr := f.Write(RenderTask(d, u.Username))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return false, werr
	}
	if _, err := m.schtasks(ctx, "/Create", "/TN", m.names.Task, "/XML", f.Name(), "/F"); err != nil {
		return false, err
	}
	return true, nil
}

func (m *taskManager) Uninstall(ctx context.Context) error {
	if out, err := m.schtasks(ctx, "/Delete", "/TN", m.names.Task, "/F"); err != nil && !notFound(out) {
		return err
	}
	return nil
}

func (m *taskManager) Start(ctx context.Context) error {
	out, err := m.schtasks(ctx, "/Run", "/TN", m.names.Task)
	if err != nil && notFound(out) {
		return fmt.Errorf("%w: %v", errNotInstalled, err)
	}
	return err
}

// Active asks PowerShell for the task's state, as schtasks prints it in the
// user's language. No output means there is no such task.
func (m *taskManager) Active(ctx context.Context) (bool, error) {
	name := strings.ReplaceAll(m.names.Task, "'", "''")
	cmd := "(Get-ScheduledTask -TaskName '" + name + "' -ErrorAction SilentlyContinue).State"
	out, err := m.run(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", cmd)
	if err != nil {
		return false, fmt.Errorf("powershell Get-ScheduledTask: %w: %s", err, output(out))
	}
	return strings.TrimSpace(string(out)) == "Running", nil
}

func (m *taskManager) ManagerEnv(context.Context) map[string]string { return nil }
func (m *taskManager) Graphical(context.Context) bool               { return false }
