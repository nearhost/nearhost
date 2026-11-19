package elevate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
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
	promptTurn = newTurn()
	t.Cleanup(func() { promptTurn = newTurn() })
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
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
	input := waitAsked(t, asked)
	if _, err := os.Stat(input); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("input still there for a late approval: %v", err)
	}
	leftOver(t, dir)

	once.Do(func() { close(release) }) // a late answer with no process to close
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("runAs did not return after release")
	}
	time.Sleep(100 * time.Millisecond) // let the goroutine finish; a panic would fail the run
}

// waitAsked returns the --input path of the next runAs call, failing t when
// none comes in 5 seconds.
func waitAsked(t *testing.T, asked chan string) string {
	t.Helper()
	select {
	case in := <-asked:
		return in
	case <-time.After(5 * time.Second):
		t.Fatal("runAs was not called")
		return ""
	}
}

// blockingRunAs replaces runAs with one that blocks until release is
// closed, sending each call's --input path on asked, and returns release. A
// call returns the handle 0 and a nil error, as a late approval with no
// process to close.
func blockingRunAs(t *testing.T) (asked chan string, release func(), calls func() int) {
	t.Helper()
	asked = make(chan string, 8)
	done := make(chan struct{})
	// Each test starts from a free turn, and a failing test cannot leave a
	// blocked runAs holding the turn for the next one.
	promptTurn = newTurn()
	var once sync.Once
	release = func() { once.Do(func() { close(done) }) }
	t.Cleanup(func() {
		release()
		promptTurn = newTurn()
	})
	var mu sync.Mutex
	n := 0
	orig := runAs
	runAs = func(_ string, args []string) (windows.Handle, error) {
		mu.Lock()
		n++
		mu.Unlock()
		asked <- args[slices.Index(args, "--input")+1]
		<-done
		return 0, nil
	}
	t.Cleanup(func() { runAs = orig })
	return asked, release, func() int { mu.Lock(); defer mu.Unlock(); return n }
}

// A prompt whose caller gave up keeps the turn until it closes: a second
// Run waits, gives up with its own error when its context ends, and never
// launches. Once the first runAs returns the turn is free and no folder is
// left.
func TestUACCancelledPromptKeepsItsTurnOnWindows(t *testing.T) {
	dir := tempDir(t)
	asked, release, calls := blockingRunAs(t)

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	time.AfterFunc(100*time.Millisecond, cancel1)
	start := time.Now()
	if err := System().Run(ctx1, helperCommand(t, "a.test\n", "exit", "0")); !errors.Is(err, context.Canceled) {
		t.Fatalf("first Run: %v, want context.Canceled", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("first Run took %v while runAs was blocked", took)
	}
	waitAsked(t, asked)
	leftOver(t, dir)

	ctx2, cancel2 := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel2()
	if err := System().Run(ctx2, helperCommand(t, "b.test\n", "exit", "0")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second Run: %v, want context.DeadlineExceeded", err)
	}
	if n := calls(); n != 1 {
		t.Fatalf("runAs called %d times, want 1: the second Run must not launch", n)
	}
	leftOver(t, dir)

	release()
	// The turn is free once the first runAs returns.
	ctx3, cancel3 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel3()
	if err := promptTurn.acquire(ctx3); err != nil {
		t.Fatalf("the turn did not come back: %v", err)
	}
	promptTurn.release()
	leftOver(t, dir)
}

// A second Run that waits for the turn starts only after the first runAs
// returns.
func TestUACSecondRunWaitsForTheFirstPromptOnWindows(t *testing.T) {
	dir := tempDir(t)
	asked, release, calls := blockingRunAs(t)

	first := make(chan error, 1)
	go func() { first <- System().Run(context.Background(), helperCommand(t, "", "exit", "0")) }()
	waitAsked(t, asked)
	second := make(chan error, 1)
	go func() { second <- System().Run(context.Background(), helperCommand(t, "", "exit", "0")) }()
	time.Sleep(200 * time.Millisecond)
	if n := calls(); n != 1 {
		t.Fatalf("runAs called %d times while the first prompt was up, want 1", n)
	}
	release()
	for _, c := range []chan error{first, second} {
		select {
		case err := <-c:
			// runAs gave handle 0, which cannot be waited on: any error is
			// fine, a hang is not.
			_ = err
		case <-time.After(10 * time.Second):
			t.Fatal("a Run did not return after the prompt closed")
		}
	}
	if n := calls(); n != 2 {
		t.Fatalf("runAs called %d times, want 2", n)
	}
	leftOver(t, dir)
}

// HelperDir makes a new folder in the temp directory, owned by this
// process's user, whose access list passes nothing down from the temp
// directory and lets in no one but that user, SYSTEM and Administrators.
func TestHelperDirIsPrivateOnWindows(t *testing.T) {
	temp := tempDir(t)
	dir, err := HelperDir()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if filepath.Dir(dir) != filepath.Clean(temp) || !strings.HasPrefix(filepath.Base(dir), HelperDirPrefix) {
		t.Fatalf("dir %s, want %s in %s", dir, HelperDirPrefix+"...", temp)
	}
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	me := user.User.Sid.String()
	// Both through SDDL, so a SID with a short name is spelt alike.
	want, err := windows.SecurityDescriptorFromString("O:" + me + "D:P(A;OICI;FA;;;" + me + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		t.Fatal(err)
	}
	// NTFS may mark the access list auto-inherited (PAI), which changes
	// nothing for this folder.
	if got := strings.Replace(sd.String(), "D:PAI(", "D:P(", 1); got != want.String() {
		t.Fatalf("security descriptor %s, want %s", got, want)
	}
	other, err := HelperDir()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(other)
	if other == dir {
		t.Fatalf("HelperDir gave %s twice", dir)
	}
}

// A helper that refuses its folder exits with HelperRefusedCode and leaves
// no report; Run names the folder and says what it must be.
func TestUACRunExplainsARefusedFolderOnWindows(t *testing.T) {
	dir := tempDir(t)
	err := System().Run(context.Background(), helperCommand(t, "", "exit", strconv.Itoa(HelperRefusedCode)))
	if err == nil || !strings.HasPrefix(err.Error(), "the administrator's helper refused the folder "+filepath.Join(dir, HelperDirPrefix)) ||
		!strings.HasSuffix(err.Error(), ": it must be on a local NTFS or ReFS drive (check TMP)") {
		t.Fatalf("Run: %v", err)
	}
	leftOver(t, dir)
}
