package cli

import (
	"bytes"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/elevate"
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
	noCleanOld(t)
	if code := Main([]string{"definitely-not-a-command"}); code == 0 {
		t.Fatal("expected non-zero exit code for unknown command")
	}
}

// noCleanOld stops Main from clearing the real %LOCALAPPDATA%\nearhost\old.
// Its cleanup waits for the runs Main started before it restores cleanOld.
func noCleanOld(t *testing.T) chan string {
	t.Helper()
	orig := cleanOld
	t.Cleanup(func() {
		clearingOld.Wait()
		cleanOld = orig
	})
	called := make(chan string, 1)
	// A second run drops its path rather than block the cleanup's wait.
	cleanOld = func(exe string) {
		select {
		case called <- exe:
		default:
		}
	}
	return called
}

// quiet sends this process's stdout and stderr to nowhere until the test ends.
func quiet(t *testing.T) {
	t.Helper()
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = null, null
	t.Cleanup(func() {
		os.Stdout, os.Stderr = stdout, stderr
		null.Close()
	})
}

// Every run clears the copies earlier updates moved aside, without waiting
// for it, unless it is elevated (the CI's Windows runner is).
func TestMainClearsOldCopies(t *testing.T) {
	called := noCleanOld(t)
	quiet(t)
	Main([]string{"version"})
	if elevate.Elevated() {
		select {
		case exe := <-called:
			t.Fatalf("an elevated run cleared old copies beside %s", exe)
		default:
		}
		return
	}
	select {
	case exe := <-called:
		if exe != realExecutable() {
			t.Fatalf("cleaned %q, want %q", exe, realExecutable())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Main did not clear old copies")
	}
}

func TestClearOldCopiesSkipsAnElevatedRun(t *testing.T) {
	called := noCleanOld(t)
	if clearOldCopies(true) {
		t.Fatal("an elevated run cleared old copies")
	}
	if !clearOldCopies(false) {
		t.Fatal("a normal run did not clear old copies")
	}
	select {
	case <-called:
	case <-time.After(10 * time.Second):
		t.Fatal("cleanOld was not called")
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

// With XDG_CONFIG_HOME unset or relative the config is under ~/.config, or
// %APPDATA% on Windows, as config.Dir has it; the help names each OS's
// locations in its own path form.
func TestConfigFlagHelpNamesBothDefaultLocations(t *testing.T) {
	usage := newRootCmd().PersistentFlags().Lookup("config").Usage
	locations := []string{"$XDG_CONFIG_HOME/nearhost/config.yaml", "~/.config/nearhost/config.yaml"}
	if runtime.GOOS == "windows" {
		locations = []string{`$XDG_CONFIG_HOME\nearhost\config.yaml`, `%APPDATA%\nearhost\config.yaml`}
	}
	for _, want := range locations {
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
  nearhost -d        Serve them in the background
  nearhost status    Show what the background Nearhost is serving
  nearhost stop      Stop it
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
