//go:build windows

package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestElevationCopyOnWindows(t *testing.T) {
	root := NewRoot()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetArgs([]string{"hosts", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Windows asks you to approve the change.") || strings.Contains(out.String(), "sudo") {
		t.Fatalf("hosts --help:\n%s", out)
	}
	flag := NewRoot().PersistentFlags().Lookup("config")
	if flag == nil || !strings.Contains(flag.Usage, "%APPDATA%") {
		t.Fatalf("--config usage %v", flag)
	}
}
