package winsys

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TestMain lets the test binary act as the program RunAs starts. An
// elevated child does not inherit the environment, so it is told what to do
// by the words after --elevate-helper.
func TestMain(m *testing.M) {
	if i := slices.Index(os.Args, "--elevate-helper"); i >= 0 {
		os.Exit(helper(os.Args[i+1:]))
	}
	os.Exit(m.Run())
}

// helper runs one helper command and returns its exit code: "exit <n>"
// exits with n, and "args <file> <arg>..." writes the arguments after the
// file to it, one per line.
func helper(words []string) int {
	switch words[0] {
	case "exit":
		n, _ := strconv.Atoi(words[1])
		return n
	case "args":
		if err := os.WriteFile(words[1], []byte(strings.Join(words[2:], "\n")), 0o600); err != nil {
			return 2
		}
		return 0
	}
	return 2
}

// self is the test binary's absolute path.
func self(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// exitCode waits for the process behind h, closes h and returns its exit
// code.
func exitCode(t *testing.T, h windows.Handle) uint32 {
	t.Helper()
	defer windows.CloseHandle(h)
	if ev, err := windows.WaitForSingleObject(h, 30_000); err != nil || ev != windows.WAIT_OBJECT_0 {
		t.Fatalf("wait: %d, %v", ev, err)
	}
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		t.Fatal(err)
	}
	return code
}

func TestShellExecuteInfoLayoutOnWindows(t *testing.T) {
	var info shellExecuteInfo
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("the layout checked is the 64-bit one")
	}
	if got := unsafe.Sizeof(info); got != 112 {
		t.Errorf("size = %d, want 112", got)
	}
	offsets := map[string][2]uintptr{
		"hwnd":      {unsafe.Offsetof(info.hwnd), 8},
		"nShow":     {unsafe.Offsetof(info.nShow), 48},
		"hInstApp":  {unsafe.Offsetof(info.hInstApp), 56},
		"dwHotKey":  {unsafe.Offsetof(info.dwHotKey), 88},
		"hIcon":     {unsafe.Offsetof(info.hIcon), 96},
		"hProcess":  {unsafe.Offsetof(info.hProcess), 104},
		"hkeyClass": {unsafe.Offsetof(info.hkeyClass), 80},
	}
	for name, o := range offsets {
		if o[0] != o[1] {
			t.Errorf("offset of %s = %d, want %d", name, o[0], o[1])
		}
	}
}

func TestRunAsWaitsForTheExitCodeOnWindows(t *testing.T) {
	h, err := RunAs(self(t), []string{"--elevate-helper", "exit", "3"})
	if err != nil {
		t.Fatal(err)
	}
	if code := exitCode(t, h); code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
}

func TestRunAsKeepsArgumentsIntactOnWindows(t *testing.T) {
	out := filepath.Join(t.TempDir(), "args")
	want := `Zoë Smith "quoted"`
	h, err := RunAs(self(t), []string{"--elevate-helper", "args", out, want})
	if err != nil {
		t.Fatal(err)
	}
	if code := exitCode(t, h); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("argument = %q, want %q", got, want)
	}
}
