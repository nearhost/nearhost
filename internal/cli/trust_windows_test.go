//go:build windows

package cli

import (
	"errors"
	"fmt"
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

	_, err = run(t, newUntrustCmd(f.backend()), "untrust")
	const wantUntrust = "Nearhost Local CA was not removed: the Windows security warning was declined. Run `nearhost untrust` again and choose Yes."
	if err == nil || err.Error() != wantUntrust {
		t.Fatalf("untrust err = %v, want %q", err, wantUntrust)
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

	_, err = run(t, newUntrustCmd(f.backend()), "untrust")
	const wantUntrust = "Nearhost Local CA was not removed: the Windows security warning was declined. Run `nearhost untrust` again and choose Yes."
	if err == nil || err.Error() != wantUntrust {
		t.Fatalf("untrust err = %v, want %q", err, wantUntrust)
	}
}
