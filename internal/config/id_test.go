package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestInstallIDIsMadeOnceAndKept(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	first, err := InstallID()
	if err != nil || !validID(first) {
		t.Fatalf("first: %q %v", first, err)
	}
	again, err := InstallID()
	if err != nil || again != first {
		t.Fatalf("again: %q %v, want %q", again, err, first)
	}
	path, _ := sub("id")
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
}

func TestInstallIDRefusesAFileThatIsNotOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "id")
	os.WriteFile(path, []byte("../../etc\n"), 0o600)
	if _, err := installIDFor(path, fingerprint("")); err == nil || !strings.Contains(err.Error(), "remove it") {
		t.Fatalf("got %v", err)
	}
}

func fingerprint(fp string) func() string { return func() string { return fp } }

func readID(t *testing.T, path string) string {
	t.Helper()
	data, _ := os.ReadFile(path)
	return string(data)
}

func TestInstallIDIsMadeAgainOnAnotherMachine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "id")
	first, err := installIDFor(path, fingerprint("aaaa"))
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := installIDFor(path, fingerprint("aaaa")); again != first {
		t.Fatalf("same machine: %q, want %q", again, first)
	}
	// The config directory was copied to another laptop.
	other, err := installIDFor(path, fingerprint("bbbb"))
	if err != nil || other == first || !validID(other) {
		t.Fatalf("another machine: %q %v (first %q)", other, err, first)
	}
	if got := readID(t, path); got != other+" bbbb\n" {
		t.Fatalf("file %q", got)
	}
}

func TestInstallIDKeepsAnIDWrittenWithoutAFingerprint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "id")
	os.WriteFile(path, []byte("k3f7q2\n"), 0o600)
	if id, err := installIDFor(path, fingerprint("aaaa")); err != nil || id != "k3f7q2" {
		t.Fatalf("%q %v", id, err)
	}
	if got := readID(t, path); got != "k3f7q2 aaaa\n" {
		t.Fatalf("file %q", got)
	}
}

func TestInstallIDStaysWhereNoFingerprintCanBeRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "id")
	os.WriteFile(path, []byte("k3f7q2 aaaa\n"), 0o600)
	if id, err := installIDFor(path, fingerprint("")); err != nil || id != "k3f7q2" {
		t.Fatalf("%q %v", id, err)
	}
}

func TestInstallIDWaitsForAFileBeingWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "id")
	os.WriteFile(path, nil, 0o600) // another process has just created it
	go func() {
		time.Sleep(100 * time.Millisecond)
		os.WriteFile(path, []byte("k3f7q2 aaaa\n"), 0o600)
	}()
	if id, err := installIDFor(path, fingerprint("aaaa")); err != nil || id != "k3f7q2" {
		t.Fatalf("%q %v", id, err)
	}
}

func TestPlatformUUIDIsReadFromIoreg(t *testing.T) {
	out := `+-o J316sAP  <class IOPlatformExpertDevice, id 0x100000256>
    {
      "IOPlatformSerialNumber" = "XYZ"
      "IOPlatformUUID" = "8E1A0B5C-1234-5678-9ABC-DEF012345678"
    }`
	if got := platformUUID(out); got != "8E1A0B5C-1234-5678-9ABC-DEF012345678" {
		t.Fatalf("%q", got)
	}
	if got := platformUUID("nothing here"); got != "" {
		t.Fatalf("%q", got)
	}
}

func TestThisMachineHasAFingerprint(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("no fingerprint on " + runtime.GOOS)
	}
	if _, err := os.Stat("/etc/machine-id"); runtime.GOOS == "linux" && err != nil {
		t.Skip("no machine-id here")
	}
	fp := machineFingerprint()
	if len(fp) != 16 || fp != machineFingerprint() {
		t.Fatalf("fingerprint %q", fp)
	}
}
