//go:build windows

package cli

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestTrustSaysPlainlyWhenTheWarningWasDeclinedOnWindows(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	f.instErr = errors.New("add cert failed: Failed adding cert: The operation was canceled by the user.")
	f.uninstErr = f.instErr

	_, err := run(t, newTrustCmd(f.backend()), "trust")
	const wantTrust = "Nearhost Local CA was not added: the Windows security warning was declined. Run `nearhost trust` again and choose Yes."
	if err == nil || err.Error() != wantTrust {
		t.Fatalf("trust err = %v, want %q", err, wantTrust)
	}

	// untrust only reaches the uninstall for a root the system holds.
	f.trusted = true
	_, err = run(t, newUntrustCmd(f.backend()), "untrust")
	const wantUntrust = "Error: Nearhost Local CA was not removed: the Windows security warning was declined. " +
		"Be ready to choose Yes in the security warning. Then run `nearhost untrust` again."
	if said := flat(rendered(err)); err == nil || said != wantUntrust {
		t.Fatalf("untrust said %q, want %q", said, wantUntrust)
	}
}

// A declined warning comes back as the errno, not only as English text.
func TestTrustSaysPlainlyWhenTheErrnoSaysCancelledOnWindows(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFake()
	f.instErr = fmt.Errorf("add cert failed: %w", windows.ERROR_CANCELLED)
	f.uninstErr = f.instErr

	_, err := run(t, newTrustCmd(f.backend()), "trust")
	const wantTrust = "Nearhost Local CA was not added: the Windows security warning was declined. Run `nearhost trust` again and choose Yes."
	if err == nil || err.Error() != wantTrust {
		t.Fatalf("trust err = %v, want %q", err, wantTrust)
	}

	// untrust only reaches the uninstall for a root the system holds.
	f.trusted = true
	_, err = run(t, newUntrustCmd(f.backend()), "untrust")
	const wantUntrust = "Error: Nearhost Local CA was not removed: the Windows security warning was declined. " +
		"Be ready to choose Yes in the security warning. Then run `nearhost untrust` again."
	if said := flat(rendered(err)); err == nil || said != wantUntrust {
		t.Fatalf("untrust said %q, want %q", said, wantUntrust)
	}
}

// A declined warning is a failure a second run gets past, so --delete-key
// deletes nothing.
func TestUntrustDeleteKeyDeletesNothingWhenTheWarningWasDeclined(t *testing.T) {
	ca, _ := localCA(t)
	f := deleteFake(t)
	f.trusted = true
	f.uninstErr = fmt.Errorf("delete cert failed: %w", windows.ERROR_CANCELLED)
	_, err := runIn(t, newUntrustCmd(f.backend()), "", "untrust", "--delete-key", "--yes")
	if said := flat(rendered(err)); err == nil || !strings.HasPrefix(said, "Error: Nothing was deleted. ") || !strings.Contains(said, "security warning") ||
		!strings.Contains(said, "Then run `nearhost untrust --delete-key` again.") {
		t.Fatalf("err = %v", err)
	}
	if !present(filepath.Join(ca, "root.key")) {
		t.Fatal("the CA was deleted")
	}
}
