package engine

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
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
