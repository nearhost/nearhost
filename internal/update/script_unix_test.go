//go:build unix

package update

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// runScriptMethod runs m's command the way `nearhost update` does, with a
// fake curl first on PATH. The fake writes installer to the file named by
// -o, records that file's path in <work>/fetched, and exits with curlExit.
// The command runs in work, so a command substitution that ran would create
// its file there.
func runScriptMethod(t *testing.T, m Method, installer string, curlExit int) (work string, err error) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	bin := t.TempDir()
	work = t.TempDir()
	curl := "#!/bin/sh\nout=\nwhile [ $# -gt 0 ]; do\n  if [ \"$1\" = -o ]; then out=$2; shift; fi\n  shift\ndone\n" +
		"printf '%s' \"$out\" > \"$WORK/fetched\"\ncat > \"$out\" <<'EOF'\n" + installer + "\nEOF\nexit " + strconv.Itoa(curlExit) + "\n"
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(curl), 0o755); err != nil {
		t.Fatal(err)
	}
	c := exec.Command(m.Args[0], m.Args[1:]...)
	c.Dir = work
	c.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "WORK="+work)
	c.Env = append(c.Env, m.Env...)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Logf("output: %s", out)
	}
	return work, err
}

// A directory name with quotes, spaces, $ and a command substitution reaches
// the installer as it is, and the shell runs none of it.
func TestScriptCommandPassesAHostileDirectoryAsData(t *testing.T) {
	dir := filepath.Join(t.TempDir(), `a b'$(touch pwned)"`+"`touch pwned2`")
	m := DetectOn("linux", filepath.Join(dir, "nearhost"), func(string) string { return "" }).Pinned("0.14.0")
	work, err := runScriptMethod(t, m, `printf '%s|%s' "$NEARHOST_INSTALL_DIR" "$NEARHOST_VERSION" > "$WORK/got"`, 0)
	if err != nil {
		t.Fatalf("the command failed: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(work, "got"))
	if err != nil {
		t.Fatalf("the installer did not run: %v", err)
	}
	if want := dir + "|v0.14.0"; string(got) != want {
		t.Fatalf("the installer saw %q, want %q", got, want)
	}
	for _, name := range []string{"pwned", "pwned2"} {
		if _, err := os.Stat(filepath.Join(work, name)); err == nil {
			t.Fatalf("the directory's name ran a command: %s exists", name)
		}
	}
	fetched, _ := os.ReadFile(filepath.Join(work, "fetched"))
	if _, err := os.Stat(string(fetched)); err == nil {
		t.Fatalf("the downloaded installer %s was left behind", fetched)
	}
}

// A download that fails part-way runs nothing, and the command fails.
func TestScriptCommandRunsNothingWhenTheDownloadFails(t *testing.T) {
	m := DetectOn("linux", "/opt/tools/bin/nearhost", func(string) string { return "" })
	work, err := runScriptMethod(t, m, `touch "$WORK/ran"`, 7)
	if err == nil {
		t.Fatal("a failed download was reported as success")
	}
	if _, err := os.Stat(filepath.Join(work, "ran")); err == nil {
		t.Fatal("the installer ran after its download failed")
	}
	fetched, _ := os.ReadFile(filepath.Join(work, "fetched"))
	if strings.TrimSpace(string(fetched)) == "" {
		t.Fatal("the fake curl was not called")
	}
	if _, err := os.Stat(string(fetched)); err == nil {
		t.Fatalf("the partial download %s was left behind", fetched)
	}
}

// The installer's own exit status is the command's.
func TestScriptCommandFailsWhenTheInstallerFails(t *testing.T) {
	m := DetectOn("linux", "/opt/tools/bin/nearhost", func(string) string { return "" })
	if _, err := runScriptMethod(t, m, "exit 3", 0); err == nil {
		t.Fatal("a failed installer was reported as success")
	}
}
