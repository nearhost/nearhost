package transport

import (
	"path/filepath"
	"testing"
)

// TestNewGivesASessionAsConfigured checks that New builds today's Session with
// the configuration applied.
func TestNewGivesASessionAsConfigured(t *testing.T) {
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
