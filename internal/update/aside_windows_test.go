package update

// Windows cannot overwrite or delete a running .exe, but it can rename one.
// These tests run a copy of this test binary, from a folder with a space and
// non-ASCII characters in its name, as a child that waits, and move it while
// it runs.

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// asideChild, in the environment of the copy, makes TestAsideChildOnWindows
// wait for its stdin to close.
const asideChild = "NEARHOST_TEST_ASIDE_CHILD"

const asideReady = "nearhost-test: running"

// TestAsideChildOnWindows is the child's half of the tests below: it says it
// is running and waits until the parent closes its stdin. Run on its own it
// does nothing.
func TestAsideChildOnWindows(t *testing.T) {
	if os.Getenv(asideChild) == "" {
		return
	}
	os.Stdout.WriteString(asideReady + "\n")
	io.Copy(io.Discard, os.Stdin)
}

// runningCopy copies this test binary to <temp>\José Núñez\nearhost.exe and
// runs it as a child that waits. stop ends the child and waits for it to
// exit; it is also called when the test ends.
func runningCopy(t *testing.T) (exe string, stop func()) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "José Núñez")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe = filepath.Join(dir, "nearhost.exe")
	copyFile(t, self, exe)

	cmd := exec.Command(exe, "-test.run=^TestAsideChildOnWindows$", "-test.count=1")
	cmd.Env = append(os.Environ(), asideChild+"=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	ready := make(chan bool, 1)
	go func() {
		s := bufio.NewScanner(stdout)
		said := false
		for s.Scan() {
			if !said && strings.TrimSpace(s.Text()) == asideReady {
				said = true
				ready <- true
			}
		}
		if !said {
			ready <- false
		}
		cmd.Wait()
		close(exited)
	}()
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		stdin.Close()
		select {
		case <-exited:
		case <-time.After(time.Minute):
			cmd.Process.Kill()
			<-exited
			t.Error("the child did not exit when its stdin closed")
		}
	}
	t.Cleanup(stop)
	select {
	case ok := <-ready:
		if !ok {
			t.Fatalf("the child ended without running; stderr:\n%s", stderr.String())
		}
	case <-time.After(2 * time.Minute):
		t.Fatal("the child did not start")
	}
	// The running copy must be locked, or the tests prove nothing.
	if err := os.Remove(exe); err == nil {
		t.Fatal("deleted a running .exe; the child is not holding it")
	}
	return exe, stop
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o755); err != nil {
		t.Fatal(err)
	}
}

// localAppData points LOCALAPPDATA at a new folder, with a space and
// non-ASCII characters in its name, and returns the folder the running copy
// moves to.
func localAppData(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "AppData Ñ")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCALAPPDATA", dir)
	return filepath.Join(dir, "nearhost", "old")
}

// entries is the names in dir.
func entries(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}

func sameFile(t *testing.T, a, b string) bool {
	t.Helper()
	x, err := os.ReadFile(a)
	if err != nil {
		t.Fatal(err)
	}
	y, err := os.ReadFile(b)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Equal(x, y)
}

func TestStepAsideMovesARunningExeOnWindows(t *testing.T) {
	old := localAppData(t)
	exe, _ := runningCopy(t)
	self, _ := os.Executable()

	restore, err := StepAside(exe)
	if err != nil {
		t.Fatalf("StepAside: %v", err)
	}
	if _, err := os.Lstat(exe); !os.IsNotExist(err) {
		t.Fatalf("the original path is not free: %v", err)
	}
	moved := entries(t, old)
	if len(moved) != 1 {
		t.Fatalf("%s holds %v, want the moved copy", old, moved)
	}
	if !sameFile(t, self, filepath.Join(old, moved[0])) {
		t.Fatal("the file in the old folder is not the running copy")
	}
	if err := os.WriteFile(exe, []byte("new"), 0o755); err != nil {
		t.Fatalf("cannot write a new file at the original path: %v", err)
	}
	os.Remove(exe)

	// Nothing replaced it: the old one goes back, while it still runs.
	if err := restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !sameFile(t, self, exe) {
		t.Fatal("restore did not put the old copy back")
	}
	if left := entries(t, old); len(left) != 0 {
		t.Fatalf("restore left %v in %s", left, old)
	}
}

// A failed installer may leave a partial file at the original path, or
// remove the folder (as npm does with a package's); restore puts the old,
// working copy back either way.
func TestRestoreReplacesWhatAFailedInstallerLeftOnWindows(t *testing.T) {
	localAppData(t)
	exe, _ := runningCopy(t)
	self, _ := os.Executable()

	restore, err := StepAside(exe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("partial"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := restore(); err != nil {
		t.Fatalf("restore over a partial file: %v", err)
	}
	if !sameFile(t, self, exe) {
		t.Fatal("restore left the partial file in place")
	}

	restore, err = StepAside(exe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Dir(exe)); err != nil {
		t.Fatal(err)
	}
	if err := restore(); err != nil {
		t.Fatalf("restore into a removed folder: %v", err)
	}
	if !sameFile(t, self, exe) {
		t.Fatal("restore did not put the old copy back into a removed folder")
	}
}

func TestCleanOldWaitsForTheCopyToExitOnWindows(t *testing.T) {
	old := localAppData(t)
	exe, stop := runningCopy(t)
	if _, err := StepAside(exe); err != nil {
		t.Fatal(err)
	}
	moved := entries(t, old)
	if len(moved) != 1 {
		t.Fatalf("%s holds %v, want the moved copy", old, moved)
	}
	// A leftover from an earlier update, no longer running, and one the
	// PowerShell installer left beside the binary.
	if err := os.WriteFile(filepath.Join(old, "nearhost-earlier.exe"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe+".old-abc123", []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	CleanOld(exe)
	if left := entries(t, old); len(left) != 1 || left[0] != moved[0] {
		t.Fatalf("with the copy running, %s holds %v, want only the running copy", old, left)
	}
	if _, err := os.Lstat(exe + ".old-abc123"); !os.IsNotExist(err) {
		t.Fatalf("the installer's leftover is still there: %v", err)
	}

	stop()
	// Windows may take a moment to let go of an image after its process
	// ends.
	deadline := time.Now().Add(30 * time.Second)
	for {
		CleanOld(exe)
		left := entries(t, old)
		if len(left) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after the copy exited, %s still holds %v", old, left)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Two updates in a row, or a leftover from an earlier one, must not make the
// next move fail.
func TestStepAsideTwiceOnWindows(t *testing.T) {
	old := localAppData(t)
	exe, _ := runningCopy(t)
	self, _ := os.Executable()
	if _, err := StepAside(exe); err != nil {
		t.Fatal(err)
	}
	copyFile(t, self, exe)
	if _, err := StepAside(exe); err != nil {
		t.Fatalf("second StepAside: %v", err)
	}
	if moved := entries(t, old); len(moved) != 2 {
		t.Fatalf("%s holds %v, want both copies", old, moved)
	}
}

// When the old folder cannot take the rename, the running copy moves beside
// itself instead. Here a file sits where %LOCALAPPDATA%\nearhost must be a
// folder; an unset or relative LOCALAPPDATA falls back too.
func TestStepAsideFallsBackBesideTheExeOnWindows(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "AppData Ñ")
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "nearhost"), []byte("not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, value string }{
		{"a file in the way", blocked},
		{"unset", ""},
		{"relative", `AppData\Local`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LOCALAPPDATA", tc.value)
			exe, _ := runningCopy(t)
			self, _ := os.Executable()
			restore, err := StepAside(exe)
			if err != nil {
				t.Fatalf("StepAside: %v", err)
			}
			want := exe + ".old-" + strconv.Itoa(os.Getpid())
			if !sameFile(t, self, want) {
				t.Fatalf("the running copy is not at %s; the folder holds %v", want, entries(t, filepath.Dir(exe)))
			}
			if _, err := os.Lstat(exe); !os.IsNotExist(err) {
				t.Fatalf("the original path is not free: %v", err)
			}
			if err := restore(); err != nil {
				t.Fatalf("restore: %v", err)
			}
			if !sameFile(t, self, exe) {
				t.Fatal("restore did not put the old copy back")
			}
		})
	}
}
