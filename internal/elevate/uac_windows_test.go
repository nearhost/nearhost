package elevate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// TestMain lets the test binary act as the elevated helper. An elevated
// child does not inherit the environment, so it is told what to do by the
// words after --elevate-helper; Run adds --input and --report after them.
func TestMain(m *testing.M) {
	if i := slices.Index(os.Args, "--elevate-helper"); i >= 0 {
		os.Exit(helper(os.Args[i+1:]))
	}
	os.Exit(m.Run())
}

// helper runs one helper command and returns its exit code. "exit <n>"
// exits with n, "copy" copies the --input file to the --report file and
// exits 1, and "wait" waits up to 30 seconds, ending early once the --input
// file is gone.
func helper(words []string) int {
	flag := func(name string) string {
		if i := slices.Index(words, name); i >= 0 && i+1 < len(words) {
			return words[i+1]
		}
		return ""
	}
	switch words[0] {
	case "exit":
		n, _ := strconv.Atoi(words[1])
		return n
	case "copy":
		in, err := os.ReadFile(flag("--input"))
		if err != nil {
			return 2
		}
		if err := os.WriteFile(flag("--report"), in, 0o600); err != nil {
			return 2
		}
		return 1
	case "wait":
		for range 300 {
			if _, err := os.Stat(flag("--input")); err != nil {
				return 2
			}
			time.Sleep(100 * time.Millisecond)
		}
		return 0
	}
	return 2
}

// helperCommand is a Command that runs the test binary as the helper with
// words.
func helperCommand(t *testing.T, input string, words ...string) Command {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Command{Path: path, Args: append([]string{"--elevate-helper"}, words...), Input: input}
}

// tempDir points this process's temporary directory at a new empty one and
// returns it, so a test can see what Run leaves behind.
func tempDir(t *testing.T) string {
	dir := t.TempDir()
	t.Setenv("TMP", dir)
	t.Setenv("TEMP", dir)
	return dir
}

// leftOver fails t when dir holds anything Run made.
func leftOver(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "nearhost-elevate-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) > 0 {
		t.Errorf("left behind: %v", matches)
	}
}

func TestUACIsTheSystemElevatorOnWindows(t *testing.T) {
	e := System()
	if e.Needed() != !Elevated() || e.TakesPassword() || e.Cached(context.Background()) {
		t.Fatalf("Needed %v, TakesPassword %v, Cached %v", e.Needed(), e.TakesPassword(), e.Cached(context.Background()))
	}
	if err := e.Validate(context.Background(), "secret"); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("Validate: %v, want ErrUnsupported", err)
	}
}

func TestUACRunGivesInputAndReadsReportOnWindows(t *testing.T) {
	dir := tempDir(t)
	err := System().Run(context.Background(), helperCommand(t, "a.test\nb.test\n", "copy"))
	if err == nil || err.Error() != "b.test" {
		t.Fatalf("Run: %v, want b.test", err)
	}
	leftOver(t, dir)
}

func TestUACRunSucceedsOnWindows(t *testing.T) {
	dir := tempDir(t)
	if err := System().Run(context.Background(), helperCommand(t, "", "exit", "0")); err != nil {
		t.Fatal(err)
	}
	leftOver(t, dir)
}

func TestUACRunGivesTheExitCodeWithoutAReportOnWindows(t *testing.T) {
	dir := tempDir(t)
	err := System().Run(context.Background(), helperCommand(t, "", "exit", "4"))
	if err == nil || err.Error() != "exited with code 4" {
		t.Fatalf("Run: %v, want exited with code 4", err)
	}
	leftOver(t, dir)
}

func TestUACRunStopsWaitingWhenTheContextEndsOnWindows(t *testing.T) {
	dir := tempDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(200*time.Millisecond, cancel)
	start := time.Now()
	err := System().Run(ctx, helperCommand(t, "", "wait"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: %v, want context.Canceled", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("Run took %v", took)
	}
	leftOver(t, dir)
}

func TestUACRunStopsWaitingWhileThePromptIsUpOnWindows(t *testing.T) {
	dir := tempDir(t)
	asked, release, returned := make(chan string, 1), make(chan struct{}), make(chan struct{})
	orig := runAs
	runAs = func(_ string, args []string) (windows.Handle, error) {
		defer close(returned)
		asked <- args[slices.Index(args, "--input")+1]
		<-release // the prompt is up until the test answers it
		return 0, nil
	}
	t.Cleanup(func() { runAs = orig })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(200*time.Millisecond, cancel)
	start := time.Now()
	err := System().Run(ctx, helperCommand(t, "a.test\n", "exit", "0"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: %v, want context.Canceled", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("Run took %v", took)
	}
	input := <-asked
	if _, err := os.Stat(input); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("input still there for a late approval: %v", err)
	}
	leftOver(t, dir)

	close(release) // a late answer with no process to close
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("runAs did not return after release")
	}
	time.Sleep(100 * time.Millisecond) // let the goroutine finish; a panic would fail the run
}
