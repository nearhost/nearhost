package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionCommandPrintsVersion(t *testing.T) {
	orig := Version
	t.Cleanup(func() { Version = orig })
	Version = "1.2.3"
	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"version"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out.String(), "nearhost 1.2.3") {
		t.Fatalf("expected version output, got %q", out.String())
	}
}

func TestMainReturnsNonZeroOnUnknownCommand(t *testing.T) {
	if code := Main([]string{"definitely-not-a-command"}); code == 0 {
		t.Fatal("expected non-zero exit code for unknown command")
	}
}

func TestVersionCommandRejectsArguments(t *testing.T) {
	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"version", "typo"})
	if err := root.Execute(); err == nil {
		t.Fatalf("expected an error for unexpected arguments, got output %q", out.String())
	}
}
