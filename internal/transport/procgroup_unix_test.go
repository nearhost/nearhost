//go:build unix

package transport

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// groups runs a session through a connect, a forward, an SFTP attempt and a
// stop, and returns the process group each kind of ssh invocation ran in.
func groups(t *testing.T, foreground bool) (master, sftp, control []int) {
	t.Helper()
	pgidLog := filepath.Join(t.TempDir(), "pgid.log")
	s, _ := newFakeSession(t, "FAKE_SSH_PGID_LOG="+pgidLog)
	s.Foreground = foreground
	if err := s.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	connected(t, s)
	if err := s.AddForward(context.Background(), 5000, 80); err != nil {
		t.Fatal(err)
	}
	if f, err := s.OpenSFTP(t.Context()); err == nil {
		f.Close()
	}
	s.Stop()
	data, err := os.ReadFile(pgidLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		first, argv, _ := strings.Cut(line, " ")
		pgid, err := strconv.Atoi(first)
		if err != nil {
			t.Fatalf("no process group in %q", line)
		}
		switch {
		case strings.HasPrefix(argv, "-N -M "):
			master = append(master, pgid)
		case strings.HasSuffix(argv, " sftp"):
			sftp = append(sftp, pgid)
		default:
			control = append(control, pgid)
		}
	}
	if len(master) == 0 || len(sftp) == 0 || len(control) == 0 {
		t.Fatalf("missing an invocation kind: master %v, sftp %v, others %v", master, sftp, control)
	}
	return master, sftp, control
}

// A terminal sends Ctrl-C to its whole foreground process group. The master
// and the SFTP process must not be in it: nearhost alone ends them.
func TestMasterAndSFTPRunInTheirOwnProcessGroups(t *testing.T) {
	own := syscall.Getpgrp()
	master, sftp, others := groups(t, false)
	for _, g := range master {
		if g == own {
			t.Errorf("the master runs in this process's group %d", own)
		}
	}
	for _, g := range sftp {
		if g == own || g == master[0] {
			t.Errorf("the SFTP process runs in group %d (ours is %d, the master's %d)", g, own, master[0])
		}
	}
	// The short commands stay with nearhost and end with it.
	for _, g := range others {
		if g != own {
			t.Errorf("a check or control command ran in group %d, want ours (%d)", g, own)
		}
	}
}

func TestForegroundSessionKeepsSSHInThisProcessGroup(t *testing.T) {
	own := syscall.Getpgrp()
	master, sftp, others := groups(t, true)
	for _, g := range append(append(master, sftp...), others...) {
		if g != own {
			t.Errorf("an ssh process ran in group %d, want ours (%d)", g, own)
		}
	}
}
