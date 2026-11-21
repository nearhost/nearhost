package config

import (
	"io/fs"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// On Windows, opening the id file while another process renames a new one
// over it fails with a sharing violation for a moment. That read is tried
// again, not reported.
func TestInstallIDReadsAgainPastASharingViolation(t *testing.T) {
	path := leftover(t, "abcdef aaaa\n")
	saved := idReadFile
	t.Cleanup(func() { idReadFile = saved })
	fails := 2
	idReadFile = func(name string) ([]byte, error) {
		if fails > 0 {
			fails--
			return nil, &fs.PathError{Op: "open", Path: name, Err: windows.ERROR_SHARING_VIOLATION}
		}
		return os.ReadFile(name)
	}
	id, err := installIDFor(path, fingerprint("aaaa"))
	if err != nil || id != "abcdef" {
		t.Fatalf("got %q %v, want the id in the file", id, err)
	}
	if fails != 0 {
		t.Fatalf("%d sharing violations left unread", fails)
	}
}
