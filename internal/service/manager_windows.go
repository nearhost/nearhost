//go:build windows

package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"strings"
	"syscall"
	"time"
)

type taskManager struct {
	names Names
	run   Runner
	sleep func(context.Context, time.Duration) // tests replace it
}

func newManager(names Names, _ string, run Runner) Manager {
	return &taskManager{names, run, sleepCtx}
}

// notFound reports whether schtasks said the task does not exist. schtasks
// prints the system's message for ERROR_FILE_NOT_FOUND in the user's
// language, so that is matched first, with the English text as a fallback.
func notFound(out []byte) bool {
	s := strings.ToLower(string(out))
	return strings.Contains(s, strings.ToLower(syscall.Errno(2).Error())) || strings.Contains(s, "cannot find")
}

// errNotInstalled is what a command on a task that does not exist returns.
var errNotInstalled = errors.New("the Nearhost task is not installed")

// schtasks runs schtasks.exe. A failure carries its output, which the
// caller can also test with notFound.
func (m *taskManager) schtasks(ctx context.Context, args ...string) ([]byte, error) {
	out, err := m.run(ctx, "schtasks.exe", args...)
	if err != nil {
		return out, fmt.Errorf("schtasks %s: %w: %s", args[0], err, output(out))
	}
	return out, nil
}

// Installed reads the task through PowerShell, in UTF-8: schtasks /Query /XML
// prints to a pipe in the OEM code page, which mangles a path that is not
// ASCII. The text still declares UTF-16, which ParseTask allows. A task that
// does not exist prints nothing; exit 0 keeps that from failing the command,
// as Export-ScheduledTask's ignored error would.
func (m *taskManager) Installed(ctx context.Context) (Definition, bool, error) {
	name := strings.ReplaceAll(m.names.Task, "'", "''")
	cmd := "$ProgressPreference='SilentlyContinue'; [Console]::OutputEncoding=[Text.Encoding]::UTF8; Export-ScheduledTask -TaskName '" + name + "' -ErrorAction SilentlyContinue; exit 0"
	out, err := m.run(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", cmd)
	if err != nil {
		return Definition{}, false, fmt.Errorf("powershell Export-ScheduledTask: %w: %s", err, output(out))
	}
	// A byte order mark or a CLIXML progress record can come before the XML.
	// The record's own header holds a <, so cut at the declaration or the
	// root element, and only otherwise at the first <.
	out = bytes.TrimPrefix(out, []byte("\xef\xbb\xbf"))
	cut := -1
	for _, marker := range []string{"<?xml", "<Task"} {
		if cut = bytes.Index(out, []byte(marker)); cut >= 0 {
			break
		}
	}
	if cut < 0 {
		cut = bytes.IndexByte(out, '<')
	}
	if cut < 0 {
		out = nil
	} else {
		out = out[cut:]
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return Definition{}, false, nil
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
	// The old core of a restart frees its lock just before its process
	// exits, and the task runs until it has: /Run then does nothing, as the
	// task ignores a new instance. Wait up to 2 s for it to stop. A task
	// still running after that, as one starting at login, is run anyway,
	// which leaves it be.
	running, _ := m.Active(ctx)
	for i := 0; running && i < 20; i++ {
		m.sleep(ctx, 100*time.Millisecond)
		running, _ = m.Active(ctx)
	}
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
