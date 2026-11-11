package transport

import (
	"path/filepath"
	"runtime"
	"testing"
)

// TestNewGivesASessionAsConfigured checks that New builds today's Session with
// the configuration applied.
func TestNewGivesASessionAsConfigured(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("New is per-process on Windows")
	}
	t.Setenv("NEARHOST_TRANSPORT", "")
	dir := t.TempDir()
	tr := New(Config{Name: "mini", Host: "mini.lan", RunDir: dir, SSHPath: "/x/ssh", Env: []string{"A=1"}, Foreground: true})
	s, ok := tr.(*Session)
	if !ok {
		t.Fatalf("New returned %T, want *Session", tr)
	}
	if s.Host() != "mini.lan" || tr.Host() != "mini.lan" {
		t.Errorf("Host() = %q", s.Host())
	}
	if s.SSHPath != "/x/ssh" {
		t.Errorf("SSHPath = %q", s.SSHPath)
	}
	if len(s.Env) != 1 || s.Env[0] != "A=1" {
		t.Errorf("Env = %v", s.Env)
	}
	if !s.Foreground {
		t.Error("Foreground = false")
	}
	if filepath.Dir(s.SocketPath()) != dir {
		t.Errorf("SocketPath = %q, want it inside %q", s.SocketPath(), dir)
	}

	d := New(Config{Name: "mini", Host: "mini.lan", RunDir: dir}).(*Session)
	if d.SSHPath != "ssh" {
		t.Errorf("default SSHPath = %q, want ssh", d.SSHPath)
	}
	d.Logf("ignored %d", 1) // a nil Config.Logf must not panic
}

// TestNewChoosesTheStrategy checks that NEARHOST_TRANSPORT=per-process selects
// the per-process transport off Windows, and any other value is ignored.
func TestNewChoosesTheStrategy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("New is per-process on Windows")
	}
	c := Config{Name: "mini", Host: "mini.lan", RunDir: t.TempDir()}
	for _, tc := range []struct {
		name, value, want string
	}{{"unset", "", "session"}, {"bogus", "bogus", "session"}, {"per-process", "per-process", "per-process"}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NEARHOST_TRANSPORT", tc.value)
			switch tr := New(c).(type) {
			case *Session:
				if tc.want != "session" {
					t.Errorf("New returned *Session, want %s", tc.want)
				}
			case *perProcess:
				if tc.want != "per-process" {
					t.Errorf("New returned *perProcess, want %s", tc.want)
				}
			default:
				t.Errorf("New returned %T", tr)
			}
		})
	}
}

// TestNewIsPerProcessOnWindows checks that Windows gets the per-process
// transport whatever the environment says.
func TestNewIsPerProcessOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows only")
	}
	t.Setenv("NEARHOST_TRANSPORT", "")
	tr := New(Config{Name: "mini", Host: "mini.lan", RunDir: t.TempDir()})
	if _, ok := tr.(*perProcess); !ok {
		t.Fatalf("New returned %T, want *perProcess", tr)
	}
}
