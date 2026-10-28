package engine

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestPortHolderFindsThisProcessOnWindows(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	got := PortHolder(context.Background(), l.Addr().(*net.TCPAddr).Port)
	want := fmt.Sprintf("engine.test (pid %d)", os.Getpid())
	if !strings.Contains(got, want) {
		t.Fatalf("PortHolder = %q, want %q", got, want)
	}
}

func TestNetstatFromSystem32OnWindows(t *testing.T) {
	dir, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "NETSTAT.EXE"); !strings.EqualFold(netstatTool.name, want) {
		t.Fatalf("netstat is run as %q, want %q", netstatTool.name, want)
	}
}
