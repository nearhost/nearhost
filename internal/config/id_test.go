package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if _, err := installID(path); err == nil || !strings.Contains(err.Error(), "remove it") {
		t.Fatalf("got %v", err)
	}
}
