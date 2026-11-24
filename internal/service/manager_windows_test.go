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
	"time"
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

const getState = "powershell.exe -NoProfile -NonInteractive -Command (Get-ScheduledTask -TaskName 'NearhostTest' -ErrorAction SilentlyContinue).State"

func TestStartCommands(t *testing.T) {
	f := &fakeRunner{}
	m := New(testNames, t.TempDir(), f.run)
	m.(*taskManager).sleep = func(context.Context, time.Duration) { t.Error("waited for a task that is not running") }
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{getState, "schtasks.exe /Run /TN NearhostTest"}; !reflect.DeepEqual(f.calls, want) {
		t.Errorf("commands = %q, want %q", f.calls, want)
	}
}

// The old core of a restart frees its lock just before its process exits,
// and the task runs until it has. /Run while it still runs does nothing, as
// the task ignores a new instance, so Start waits up to 2 s for it to stop.
func TestStartWaitsForTheOldTask(t *testing.T) {
	for _, tc := range []struct {
		name    string
		running int // how many times Active says Running
		waits   int
	}{
		{"stops within the window", 3, 3},
		{"still running at 2 s", 1000, 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asked := 0
			f := &fakeRunner{handle: func(call string) ([]byte, error) {
				if call == getState {
					asked++
					if asked <= tc.running {
						return []byte("Running\r\n"), nil
					}
					return []byte("Ready\r\n"), nil
				}
				return nil, nil
			}}
			m := New(testNames, t.TempDir(), f.run)
			var slept []time.Duration
			m.(*taskManager).sleep = func(_ context.Context, d time.Duration) { slept = append(slept, d) }
			if err := m.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(slept) != tc.waits || slept[0] != 100*time.Millisecond {
				t.Errorf("slept %v, want %d waits of 100ms", slept, tc.waits)
			}
			if last := f.calls[len(f.calls)-1]; last != "schtasks.exe /Run /TN NearhostTest" {
				t.Errorf("last command %q, want /Run; commands %q", last, f.calls)
			}
		})
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
	want := []string{`powershell.exe -NoProfile -NonInteractive -Command $ProgressPreference='SilentlyContinue'; [Console]::OutputEncoding=[Text.Encoding]::UTF8; Export-ScheduledTask -TaskName 'Ed''s Task' -ErrorAction SilentlyContinue; exit 0`}
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

// schtasks /Create can fail with the not-found text, as for an executable it
// cannot find; what it printed must reach the user.
func TestInstallFailureCarriesSchtasksOutput(t *testing.T) {
	f := &fakeRunner{handle: func(call string) ([]byte, error) {
		if strings.HasPrefix(call, "schtasks.exe /Create") {
			return []byte(missing), errors.New("exit status 1")
		}
		return nil, nil
	}}
	_, err := New(testNames, t.TempDir(), f.run).Install(context.Background(), plain())
	if err == nil || !strings.Contains(err.Error(), "cannot find the file specified") {
		t.Errorf("Install = %v, want schtasks' output in the error", err)
	}
}

// PowerShell can put a byte order mark or a CLIXML progress record in front
// of the task's XML; a bare mark is no task.
func TestInstalledToleratesNoise(t *testing.T) {
	d := plain()
	s := newTaskStore(t)
	if _, err := New(testNames, t.TempDir(), s.fake.run).Install(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	xml := decodeUTF16(t, s.xml)
	for name, out := range map[string]string{
		"bom":    "\ufeff" + xml,
		"clixml": "#< CLIXML\r\n<Objs Version=\"1.1.0.1\" xmlns=\"http://schemas.microsoft.com/powershell/2004/04\"><Obj S=\"progress\" RefId=\"0\"><TN RefId=\"0\"><T>System.Management.Automation.PSCustomObject</T></TN></Obj></Objs>\r\n" + xml,
	} {
		f := &fakeRunner{handle: func(string) ([]byte, error) { return []byte(out), nil }}
		got, ok, err := New(testNames, t.TempDir(), f.run).Installed(context.Background())
		if err != nil || !ok || !reflect.DeepEqual(got, d.For(Task)) {
			t.Errorf("%s: Installed = %+v, %v, %v; want %+v", name, got, ok, err, d.For(Task))
		}
	}
	f := &fakeRunner{handle: func(string) ([]byte, error) { return []byte("\ufeff"), nil }}
	if _, ok, err := New(testNames, t.TempDir(), f.run).Installed(context.Background()); ok || err != nil {
		t.Errorf("a bare BOM: Installed = %v, %v; want false, nil", ok, err)
	}
}
