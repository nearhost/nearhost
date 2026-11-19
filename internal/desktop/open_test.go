//go:build !windows

package desktop

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeOpener points the opener at a shell script with the given body.
func fakeOpener(t *testing.T, body string) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "opener.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := opener
	opener = func(url string) []string { return []string{script, url} }
	t.Cleanup(func() { opener = old })
}

func TestOpenURLReturnsWhileOpenerRuns(t *testing.T) {
	fakeOpener(t, "sleep 10")
	start := time.Now()
	if err := OpenURL("https://a.test"); err != nil {
		t.Fatalf("OpenURL: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("OpenURL took %v, want under 1s", d)
	}
}

func TestOpenURLReportsEarlyFailure(t *testing.T) {
	fakeOpener(t, "exit 3")
	if err := OpenURL("https://a.test"); err == nil {
		t.Error("OpenURL succeeded for an opener that exited 3")
	}
}

func TestOpenURLReportsMissingOpener(t *testing.T) {
	old := opener
	opener = func(string) []string { return []string{"/nonexistent/opener"} }
	t.Cleanup(func() { opener = old })
	if err := OpenURL("https://a.test"); err == nil {
		t.Error("OpenURL succeeded for an opener that is not installed")
	}
}
