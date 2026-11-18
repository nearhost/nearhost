//go:build unix

package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// An elevated run refuses every spelling of /etc/hosts but the one Nearhost
// passes: the kernel resolves ".." after the link before it, so
// /tmp/x/../../etc/hosts can open another file.
func TestElevatedFileCheckRefusesOtherSpellingsOnUnix(t *testing.T) {
	runElevated(t)
	for _, p := range []string{"/etc/hosts/", "/etc/./hosts", "/etc/../etc/hosts", "/tmp/x/../../etc/hosts", "/private/etc/hosts"} {
		if err := checkElevatedFile(p); err == nil {
			t.Errorf("checkElevatedFile(%q) accepted it", p)
		}
	}
	t.Chdir("/etc")
	if err := checkElevatedFile("hosts"); err == nil {
		t.Error(`checkElevatedFile("hosts") from /etc accepted it`)
	}
}

// Nearhost hands the sudo helper its aliases on stdin, so an elevated run
// takes no --input or --report: it reads no file and creates none.
func TestElevatedHostsApplyRefusesInputAndReportOnUnix(t *testing.T) {
	for name, flags := range map[string][]string{
		"input":  {"--input", "IN"},
		"report": {"--report", "REPORT"},
		"both":   {"--input", "IN", "--report", "REPORT"},
	} {
		t.Run(name, func(t *testing.T) {
			runElevated(t)
			dir := t.TempDir()
			in := filepath.Join(dir, "input")
			if err := os.WriteFile(in, []byte("a.test\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			report := filepath.Join(dir, "report")
			var args []string
			for _, f := range flags {
				switch f {
				case "IN":
					f = in
				case "REPORT":
					f = report
				}
				args = append(args, f)
			}
			_, err := runApply(t, args...)
			want := "an elevated run takes no --input or --report here"
			if err == nil || err.Error() != want {
				t.Fatalf("err = %v, want %q", err, want)
			}
			if _, err := os.Lstat(report); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("report created: %v", err)
			}
		})
	}
}
