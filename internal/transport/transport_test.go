package transport

import (
	"net"
	"runtime"
	"strconv"
	"testing"
)

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

func TestIsFatalSSHError(t *testing.T) {
	fatal := []string{
		"user@host: Permission denied (publickey).",
		"Host key verification failed.",
		"Too many authentication failures",
	}
	for _, m := range fatal {
		if !isFatalSSHError(m) {
			t.Errorf("%q should be fatal", m)
		}
	}
	retry := []string{
		"Warning: Identity file /x/id_missing not accessible: No such file or directory.",
		"client_loop: send disconnect: Broken pipe",
		"ssh exited",
	}
	for _, m := range retry {
		if isFatalSSHError(m) {
			t.Errorf("%q should be retried", m)
		}
	}
}

func TestFreePortIsBindable(t *testing.T) {
	p, err := FreePort()
	if err != nil || p < 1024 || p > 65535 {
		t.Fatalf("FreePort = %d, %v", p, err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
	if err != nil {
		t.Fatalf("port %d not bindable: %v", p, err)
	}
	l.Close()
}
