package transport

import (
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

func TestOwnGroupOnWindows(t *testing.T) {
	cmd := exec.Command("ssh")
	ownGroup(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("no SysProcAttr")
	}
	want := uint32(windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW)
	if got := cmd.SysProcAttr.CreationFlags; got&want != want {
		t.Fatalf("CreationFlags = %#x, want %#x set", got, want)
	}
}
