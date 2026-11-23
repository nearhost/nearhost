//go:build windows

package service

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

const missing = "ERROR: The system cannot find the file specified.\r\n"

// taskStore plays Task Scheduler: /Create keeps the XML it is given and
// Export-ScheduledTask returns it, as UTF-8 text that still declares UTF-16,
// as PowerShell prints it.
type taskStore struct {
	xml     []byte
	running bool
	fake    *fakeRunner
}

func newTaskStore(t *testing.T) *taskStore {
	s := &taskStore{}
	s.fake = &fakeRunner{handle: func(call string) ([]byte, error) {
		switch {
		case strings.HasPrefix(call, "schtasks.exe /Create"):
			b, err := os.ReadFile(strings.TrimSuffix(strings.SplitN(call, " /XML ", 2)[1], " /F"))
			if err != nil {
				t.Error(err)
			}
			s.xml = b
			return []byte("SUCCESS: The scheduled task has successfully been created.\r\n"), nil
		case strings.HasPrefix(call, "schtasks.exe /Delete"):
			if s.xml == nil {
				return []byte(missing), errors.New("exit status 1")
			}
			s.xml = nil
			return nil, nil
		case strings.Contains(call, "Export-ScheduledTask"):
			if s.xml == nil {
				return nil, nil
			}
			return []byte(decodeUTF16(t, s.xml)), nil
		}
		return nil, nil
	}}
	return s
}

func TestInstallThenInstalled(t *testing.T) {
	s := newTaskStore(t)
	m := New(testNames, t.TempDir(), s.fake.run)
	d := plain()
	changed, err := m.Install(context.Background(), d)
	if err != nil || !changed {
		t.Fatalf("Install = %v, %v; want true, nil", changed, err)
	}
	got, ok, err := m.Installed(context.Background())
	if err != nil || !ok || !reflect.DeepEqual(got, d.For(Task)) {
		t.Errorf("Installed = %+v, %v, %v; want %+v", got, ok, err, d.For(Task))
	}
}

func TestInstallUnchanged(t *testing.T) {
	s := newTaskStore(t)
	m := New(testNames, t.TempDir(), s.fake.run)
	// A task carries no Env or LogPath, so they must not make it look changed.
	d := plain()
	if _, err := m.Install(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	s.fake.reset()
	changed, err := m.Install(context.Background(), d)
	if err != nil || changed {
		t.Fatalf("Install = %v, %v; want false, nil", changed, err)
	}
	for _, c := range s.fake.calls {
		if strings.Contains(c, "/Create") {
			t.Errorf("ran %q for an unchanged task", c)
		}
	}
}

func TestStartCommands(t *testing.T) {
	f := &fakeRunner{}
	if err := New(testNames, t.TempDir(), f.run).Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"schtasks.exe /Run /TN NearhostTest"}; !reflect.DeepEqual(f.calls, want) {
		t.Errorf("commands = %q, want %q", f.calls, want)
	}
}

func TestUninstallWhenNone(t *testing.T) {
	s := newTaskStore(t)
	m := New(testNames, t.TempDir(), s.fake.run)
	if err := m.Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"schtasks.exe /Delete /TN NearhostTest /F"}; !reflect.DeepEqual(s.fake.calls, want) {
		t.Errorf("commands = %q, want %q", s.fake.calls, want)
	}
}

func TestUninstallRemoves(t *testing.T) {
	s := newTaskStore(t)
	m := New(testNames, t.TempDir(), s.fake.run)
	if _, err := m.Install(context.Background(), plain()); err != nil {
		t.Fatal(err)
	}
	if err := m.Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := m.Installed(context.Background()); ok {
		t.Error("still installed")
	}
}

func TestActiveParses(t *testing.T) {
	for name, c := range map[string]struct {
		out  string
		err  error
		want bool
	}{
		"running":       {"Running\r\n", nil, true},
		"ready":         {"Ready\r\n", nil, false},
		"not installed": {"", nil, false},
	} {
		f := &fakeRunner{handle: func(string) ([]byte, error) { return []byte(c.out), c.err }}
		got, err := New(testNames, t.TempDir(), f.run).Active(context.Background())
		if err != nil || got != c.want {
			t.Errorf("%s: Active = %v, %v; want %v", name, got, err, c.want)
		}
	}
}

func TestActiveCommand(t *testing.T) {
	f := &fakeRunner{}
	names := Names{Task: "Ed's Task"}
	if _, err := New(names, t.TempDir(), f.run).Active(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{`powershell.exe -NoProfile -NonInteractive -Command (Get-ScheduledTask -TaskName 'Ed''s Task' -ErrorAction SilentlyContinue).State`}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("commands = %q, want %q", f.calls, want)
	}
}

func TestNotFoundInAnyLanguage(t *testing.T) {
	// The system's own text for ERROR_FILE_NOT_FOUND is whatever language Windows runs in.
	msg := "ERROR: " + syscall.Errno(2).Error() + "\r\n"
	f := &fakeRunner{handle: func(string) ([]byte, error) { return []byte(msg), errors.New("exit status 1") }}
	m := New(testNames, t.TempDir(), f.run)
	if err := m.Uninstall(context.Background()); err != nil {
		t.Errorf("Uninstall = %v", err)
	}
	if err := m.Start(context.Background()); !errors.Is(err, errNotInstalled) {
		t.Errorf("Start = %v, want errNotInstalled", err)
	}
}

// The task is read through PowerShell, in UTF-8: schtasks /Query /XML piped
// prints the OEM code page, which mangles a path that is not ASCII.
func TestInstalledCommand(t *testing.T) {
	f := &fakeRunner{}
	names := Names{Task: "Ed's Task"}
	if _, ok, err := New(names, t.TempDir(), f.run).Installed(context.Background()); ok || err != nil {
		t.Fatalf("Installed = %v, %v; want false, nil", ok, err)
	}
	want := []string{`powershell.exe -NoProfile -NonInteractive -Command [Console]::OutputEncoding=[Text.Encoding]::UTF8; Export-ScheduledTask -TaskName 'Ed''s Task' -ErrorAction SilentlyContinue; exit 0`}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("commands = %q, want %q", f.calls, want)
	}
}

func TestInstalledReadsAPathThatIsNotASCII(t *testing.T) {
	s := newTaskStore(t)
	m := New(testNames, t.TempDir(), s.fake.run)
	d := Definition{Exe: `C:\Users\Jürgen Øster\AppData\Local\nearhost\nearhost.exe`}
	if _, err := m.Install(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	got, ok, err := m.Installed(context.Background())
	if err != nil || !ok || got.Exe != d.Exe {
		t.Errorf("Installed = %+v, %v, %v; want %q", got, ok, err, d.Exe)
	}
}

func TestInstalledErrors(t *testing.T) {
	for name, c := range map[string]struct {
		out        string
		err        error
		unreadable bool
	}{
		"powershell fails": {"powershell.exe: not found", errors.New("exit status 1"), false},
		"not a task":       {"<?xml version=\"1.0\"?><Other/>", nil, true},
		"another program":  {"<Task><Actions><Exec><Command>x.exe</Command></Exec></Actions></Task>", nil, true},
	} {
		f := &fakeRunner{handle: func(string) ([]byte, error) { return []byte(c.out), c.err }}
		_, ok, err := New(testNames, t.TempDir(), f.run).Installed(context.Background())
		if ok || err == nil || errors.Is(err, ErrUnreadable) != c.unreadable {
			t.Errorf("%s: Installed = %v, %v; want an error, unreadable %v", name, ok, err, c.unreadable)
		}
	}
}
