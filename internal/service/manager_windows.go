//go:build windows

package service

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"os/user"
	"strings"
)

type taskManager struct {
	names Names
	run   Runner
}

func newManager(names Names, _ string, run Runner) Manager { return &taskManager{names, run} }

// notFound reports whether schtasks said the task does not exist.
func notFound(out []byte) bool {
	return strings.Contains(strings.ToLower(string(out)), "cannot find")
}

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
		return Definition{}, false, fmt.Errorf("read task %s: %w", m.names.Task, err)
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
	_, err := m.schtasks(ctx, "/Run", "/TN", m.names.Task)
	return err
}

func (m *taskManager) Active(ctx context.Context) (bool, error) {
	out, err := m.schtasks(ctx, "/Query", "/TN", m.names.Task, "/FO", "CSV", "/NH")
	if err != nil {
		if notFound(out) {
			return false, nil
		}
		return false, err
	}
	rows, err := csv.NewReader(strings.NewReader(string(out))).ReadAll()
	if err != nil {
		return false, fmt.Errorf("read schtasks output: %w", err)
	}
	for _, r := range rows {
		if len(r) >= 3 && r[2] == "Running" {
			return true, nil
		}
	}
	return false, nil
}

func (m *taskManager) ManagerEnv(context.Context) map[string]string { return nil }
func (m *taskManager) Graphical(context.Context) bool               { return false }
