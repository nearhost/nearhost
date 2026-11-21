//go:build !windows

package cli

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// The real spawn starts the child in a session of its own, with no terminal,
// and its wait reports how the child ended.
func TestSpawnDetachedRunsInItsOwnSession(t *testing.T) {
	out := t.TempDir() + "/sid"
	logPath := t.TempDir() + "/core.log"
	wait, err := spawnDetached("/bin/sh", []string{"-c", `echo $$ $(ps -o pgid= -p $$) > "$1"; echo panic: oops >&2; exit 3`, "sh", out}, logPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := wait(); err == nil || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("wait = %v, want exit status 3", err)
	}
	// A second call gives the same answer rather than blocking.
	if err := wait(); err == nil {
		t.Fatal("second wait = nil")
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(string(b))
	if len(f) != 2 {
		t.Fatalf("child wrote %q", b)
	}
	pid, _ := strconv.Atoi(f[0])
	pgid, _ := strconv.Atoi(f[1])
	own, err := syscall.Getpgid(0)
	if err != nil {
		t.Fatal(err)
	}
	// setsid makes the child the leader of a new group as well as a new
	// session.
	if pgid != pid || pgid == own {
		t.Errorf("child %d is in group %d, want its own (this process is in %d)", pid, pgid, own)
	}
	// What the child says on stderr, as a Go panic, is in the core log.
	if b, err := os.ReadFile(logPath); err != nil || string(b) != "panic: oops\n" {
		t.Errorf("core log %q, %v", b, err)
	}
}
