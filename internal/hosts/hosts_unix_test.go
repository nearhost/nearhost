//go:build unix

package hosts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyUnwritableDirectoryNamesDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts")
	if err := os.WriteFile(path, []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	err := Apply(path, []string{"a.test"})
	if err == nil || !strings.Contains(err.Error(), dir) {
		t.Fatalf("err = %v, want one naming %s", err, dir)
	}
	got, _ := os.ReadFile(path)
	if string(got) != base {
		t.Fatalf("file changed despite error: %q", got)
	}
}
