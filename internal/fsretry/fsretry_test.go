package fsretry

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var errBusy = errors.New("busy")

func busy(err error) bool { return errors.Is(err, errBusy) }

// A rename busy twice is tried again, every apart, and succeeds on the
// third try.
func TestRetryWhileBusy(t *testing.T) {
	calls := 0
	var slept []time.Duration
	rename := Retry(func(string, string) error {
		calls++
		if calls <= 2 {
			return errBusy
		}
		return nil
	}, busy, func(d time.Duration) { slept = append(slept, d) }, 7*time.Millisecond, time.Second)
	if err := rename("a", "b"); err != nil || calls != 3 || len(slept) != 2 || slept[0] != 7*time.Millisecond {
		t.Fatalf("err %v after %d tries, slept %v", err, calls, slept)
	}
}

// A rename still busy at the limit returns a *StillBusyError holding the
// rename's error.
func TestRetryGivesUpAtTheLimit(t *testing.T) {
	calls := 0
	var waited time.Duration
	rename := Retry(func(string, string) error { calls++; return errBusy },
		busy, func(d time.Duration) { waited += d }, 10*time.Millisecond, 30*time.Millisecond)
	err := rename("a", "b")
	if _, ok := errors.AsType[*StillBusyError](err); !ok || !errors.Is(err, errBusy) || err.Error() != "busy" {
		t.Fatalf("err %v, want a StillBusyError for errBusy", err)
	}
	if waited != 30*time.Millisecond || calls != 4 {
		t.Fatalf("waited %v over %d tries, want 30ms over 4", waited, calls)
	}
}

// Any other error returns at once.
func TestRetryReturnsOtherErrors(t *testing.T) {
	other := errors.New("other")
	calls := 0
	rename := Retry(func(string, string) error { calls++; return other },
		busy, func(time.Duration) { t.Error("slept on an error that is not busy") }, time.Millisecond, time.Second)
	if err := rename("a", "b"); err != other || calls != 1 {
		t.Fatalf("err %v after %d tries", err, calls)
	}
}

func TestRenameMovesTheFile(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if err := os.WriteFile(from, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Rename(from, to, time.Millisecond, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(to); err != nil || string(got) != "x" {
		t.Fatalf("%s holds %q, err %v", to, got, err)
	}
}
