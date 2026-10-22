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

// With XDG_CONFIG_HOME unset or relative the config is under ~/.config.
func TestConfigFlagHelpNamesBothDefaultLocations(t *testing.T) {
	usage := newRootCmd().PersistentFlags().Lookup("config").Usage
	for _, want := range []string{"$XDG_CONFIG_HOME/nearhost/config.yaml", "~/.config/nearhost/config.yaml"} {
		if !strings.Contains(usage, want) {
			t.Errorf("--config help %q does not name %s", usage, want)
		}
	}
}

func TestVersionFlag(t *testing.T) {
	orig := Version
	t.Cleanup(func() { Version = orig })
	Version = "1.2.3"
	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--version"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if out.String() != "nearhost 1.2.3\n" {
		t.Fatalf("output = %q", out.String())
	}
}

func TestRootHelpSaysWhatBareNearhostDoes(t *testing.T) {
	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	const block = `Getting started:
  nearhost add .     Add the project in this directory
  nearhost           Open the interface, which serves every enabled project
  nearhost up        Serve them without the interface
  nearhost doctor    Check the machine and each project`
	if !strings.Contains(out.String(), block) {
		t.Fatalf("help lacks the getting started block:\n%s", out.String())
	}
}

func TestUnknownCommandSuggests(t *testing.T) {
	for _, args := range [][]string{{"lss"}, {"lss", "--foo"}} {
		root := NewRoot()
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs(args)
		err := root.Execute()
		if err == nil || !strings.Contains(err.Error(), "Did you mean this?") || !strings.Contains(err.Error(), "\tls") {
			t.Errorf("%v: err = %v", args, err)
		}
	}
}

// A bad flag with no command, or with a word nothing is like, is the plain
// flag error, with no suggestion.
func TestUnknownFlagWithoutACommandIsAFlagError(t *testing.T) {
	for _, args := range [][]string{{"--foo"}, {"zzzzzz", "--foo"}} {
		root := NewRoot()
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs(args)
		err := root.Execute()
		if err == nil || !strings.Contains(err.Error(), "unknown flag: --foo") || strings.Contains(err.Error(), "Did you mean") {
			t.Errorf("%v: err = %v", args, err)
		}
	}
}

func TestHostsApplyHidden(t *testing.T) {
	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"hosts", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if strings.Contains(out.String(), "apply") || !strings.Contains(out.String(), "clean") {
		t.Fatalf("help:\n%s", out.String())
	}
}
