package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// A Nearhost answering on the agent socket is running; nothing listening
// there, in a folder where a running Nearhost could have made its socket,
// means none is. A dial that fails another way, or a folder where no socket
// can be made, cannot tell: a Nearhost whose socket failed runs on without
// one.
func TestRunningAtTellsOnlyWhatTheSocketCanShow(t *testing.T) {
	const path = "home/.nearhost/id.sock"
	for _, tc := range []struct {
		name     string
		dial     error
		writable error
		up       bool
		wantErr  string
	}{
		{name: "answers", up: true},
		{name: "no socket file", dial: &net.OpError{Op: "dial", Err: &os.SyscallError{Syscall: "connect", Err: fs.ErrNotExist}}},
		{name: "stale socket file", dial: &net.OpError{Op: "dial", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}},
		{name: "other dial error", dial: &net.OpError{Op: "dial", Err: errors.New("invalid argument")}, wantErr: "invalid argument"},
		{name: "folder not writable", dial: fs.ErrNotExist, writable: errors.New("permission denied"), wantErr: "permission denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var looked string
			up, err := runningAt(path,
				func(string) error { return tc.dial },
				func(dir string) error { looked = dir; return tc.writable })
			if up != tc.up {
				t.Errorf("running = %v, want %v", up, tc.up)
			}
			if tc.wantErr == "" && err != nil {
				t.Errorf("err = %v, want none", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Errorf("err = %v, want one with %q", err, tc.wantErr)
			}
			if tc.dial != nil && nothingListens(tc.dial) && looked != filepath.Dir(path) {
				t.Errorf("looked at %q for writing, want %q", looked, filepath.Dir(path))
			}
		})
	}
}

// dialSocket reaches a listening socket, and a path with no socket fails as
// nothing listening, on every system the agent socket is used on.
func TestDialSocketTellsAListenerFromNone(t *testing.T) {
	dir, err := os.MkdirTemp("", "nh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "s.sock")
	if err := dialSocket(path); err == nil || !nothingListens(err) {
		t.Fatalf("no socket: dial = %v, want nothing listening", err)
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	if err := dialSocket(path); err != nil {
		t.Fatalf("listening: dial = %v", err)
	}
}

// canCreateIn succeeds in a folder this user can write, looks at the parent
// of one not made yet, and leaves nothing behind.
func TestCanCreateIn(t *testing.T) {
	dir := t.TempDir()
	if err := canCreateIn(dir); err != nil {
		t.Fatalf("writable folder: %v", err)
	}
	if err := canCreateIn(filepath.Join(dir, "not-yet")); err != nil {
		t.Fatalf("missing folder in a writable one: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("left %d entries behind", len(entries))
	}
	if _, err := os.Stat(filepath.Join(dir, "not-yet")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the missing folder was made: %v", err)
	}
	if err := canCreateIn(filepath.Join(dir, "a", "b")); err == nil {
		t.Error("a folder whose parent is missing too counts as writable")
	}
}

// The refusal says how to finish by hand when the check cannot tell, as it
// may never be able to on this machine.
func TestRunningProblemSaysHowToFinishByHand(t *testing.T) {
	err := runningProblem(trustBackend{running: func() (bool, error) { return false, fmt.Errorf("the socket path is too long") }})
	for _, want := range []string{"could not tell whether a Nearhost is running (the socket path is too long)", "Stop every Nearhost", "nearhost untrust"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want %q in it", err, want)
		}
	}
}
