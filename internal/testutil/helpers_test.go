package testutil

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// recorder notes a failure instead of ending the test.
type recorder struct {
	testing.TB
	failed bool
}

func (r *recorder) Errorf(string, ...any) { r.failed = true }
func (r *recorder) Fatalf(string, ...any) { r.failed = true }

func TestWantPermChecksTheModeOnUnix(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	os.WriteFile(p, nil, 0o600)
	rec := &recorder{TB: t}
	WantPerm(rec, p, 0o644)
	if (runtime.GOOS != "windows") != rec.failed {
		t.Fatalf("failed = %v on %s", rec.failed, runtime.GOOS)
	}
	rec = &recorder{TB: t}
	WantPerm(rec, filepath.Join(t.TempDir(), "missing"), 0o600)
	if !rec.failed {
		t.Fatal("a missing file passed")
	}
}

func TestSetHomeMovesUserHomeDir(t *testing.T) {
	dir := t.TempDir()
	SetHome(t, dir)
	if got, _ := os.UserHomeDir(); got != dir {
		t.Fatalf("UserHomeDir = %q, want %q", got, dir)
	}
}

func TestLongPathIsStableForALongPath(t *testing.T) {
	dir := LongPath(t, t.TempDir())
	if LongPath(t, dir) != dir || strings.Contains(dir, "~") {
		t.Fatalf("LongPath = %q", dir)
	}
}
