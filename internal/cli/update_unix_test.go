//go:build unix

package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// probeVersion runs the binary and reads its version; a binary that does
// not start is an error, not a version.
func TestProbeVersion(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "nearhost")
	if err := os.WriteFile(good, []byte("#!/bin/sh\n[ \"$1\" = version ] && echo 'nearhost 0.6.0'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if v, err := probeVersion(context.Background(), good); err != nil || v != "0.6.0" {
		t.Fatalf("probeVersion = %q, %v; want 0.6.0", v, err)
	}
	broken := filepath.Join(dir, "broken")
	if err := os.WriteFile(broken, []byte("not a program"), 0o644); err != nil {
		t.Fatal(err)
	}
	if v, err := probeVersion(context.Background(), broken); err == nil {
		t.Fatalf("a binary that cannot start gave version %q", v)
	}
}
