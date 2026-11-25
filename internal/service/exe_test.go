package service

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/report"
)

func TestServiceExe(t *testing.T) {
	root := t.TempDir()
	// A Homebrew install: the versioned binary and the bin symlink to it.
	cask := filepath.Join(root, "Caskroom", "nearhost", "1.0", "nearhost")
	if err := os.MkdirAll(filepath.Dir(cask), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cask, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "bin", "nearhost")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	// Windows needs a privilege to symlink, and skips the Homebrew cases anyway.
	if err := os.Symlink(cask, link); err != nil && runtime.GOOS != "windows" {
		t.Fatal(err)
	}
	orphan := filepath.Join(root, "other", "Caskroom", "nearhost", "1.0", "nearhost")

	found := func(string) (string, error) { return link, nil }
	missing := func(string) (string, error) { return "", errors.New("not found") }
	noEnv := func(string) string { return "" }

	tests := []struct {
		name, goos, exe string
		lookPath        func(string) (string, error)
		want, note, err string
	}{
		{"npx", "linux", "/home/u/.npm/_npx/ab/node_modules/@nearhost/cli-linux-x64/bin/nearhost", missing,
			"", "", "fetched by npx, bunx or pnpm dlx for one run"},
		{"bunx", "linux", "/tmp/bunx-1000-@nearhost/cli@latest/node_modules/@nearhost/cli-linux-x64/bin/nearhost", missing,
			"", "", "fetched by npx, bunx or pnpm dlx for one run"},
		{"go run", "linux", "/home/u/.cache/go-build/ab/exe/nearhost", missing,
			"", "", "built by go run"},
		{"go run temp dir", "linux", "/home/u/tmp/go-build123456/b001/exe/nearhost", missing,
			"", "", "built by go run"},
		{"go run on windows", "windows", `C:\Users\u\AppData\Local\Temp\go-build123456\b001\exe\nearhost.exe`, missing,
			"", "", "built by go run"},
		{"go-builder is not go run", "linux", "/opt/go-builder/bin/nearhost", missing,
			"/opt/go-builder/bin/nearhost", "", ""},
		{"homebrew symlink", "darwin", cask, found, link, "", ""},
		{"homebrew without symlink", "darwin", orphan, found,
			"", "", "Homebrew's versioned folder"},
		{"homebrew without bin", "darwin", cask, missing,
			"", "", "Homebrew's versioned folder"},
		{"nvm", "linux", "/home/u/.nvm/versions/node/v26.13.0/bin/nearhost", missing,
			"/home/u/.nvm/versions/node/v26.13.0/bin/nearhost", "Node v26.13.0's folder", ""},
		{"windows", "windows", `C:\Users\Ed Smith\AppData\Local\nearhost\nearhost.exe`, missing,
			`C:\Users\Ed Smith\AppData\Local\nearhost\nearhost.exe`, "", ""},
		{"plain", "linux", "/usr/local/bin/nearhost", missing, "/usr/local/bin/nearhost", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if runtime.GOOS == "windows" && strings.HasPrefix(tc.name, "homebrew") {
				t.Skip("Homebrew exists on macOS and Linux only, and the test needs POSIX paths and symlinks")
			}
			got, note, err := ServiceExe(tc.goos, tc.exe, noEnv, tc.lookPath)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want one containing %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("path = %q, want %q", got, tc.want)
			}
			if tc.note == "" && note != nil || tc.note != "" && (note == nil || !strings.Contains(note.Error(), tc.note)) {
				t.Errorf("note = %q, want one containing %q", note, tc.note)
			}
		})
	}
}

// nvm keeps Node's versions under $NVM_DIR, which need not be ~/.nvm.
func TestServiceExeNotesNodeUnderNVMDir(t *testing.T) {
	getenv := func(n string) string {
		return map[string]string{"NVM_DIR": "/home/u/.local/share/nvm"}[n]
	}
	exe := "/home/u/.local/share/nvm/versions/node/v26.13.0/bin/nearhost"
	got, note, err := ServiceExe("linux", exe, getenv, func(string) (string, error) { return "", errors.New("not found") })
	if err != nil || got != exe || note == nil || !strings.Contains(note.Error(), "Node v26.13.0's folder") {
		t.Errorf("ServiceExe = %q, %q, %v; want the path and a note on Node v26.13.0", got, note, err)
	}
}

// Windows paths ignore case, so NVM_DIR need not match the binary's in it.
func TestServiceExeNotesNodeUnderNVMDirInAnyCase(t *testing.T) {
	getenv := func(n string) string {
		return map[string]string{"NVM_DIR": `C:\Users\Ed\AppData\Roaming\NVM`}[n]
	}
	exe := `C:\Users\ed\AppData\Roaming\nvm\versions\node\v26.13.0\nearhost.exe`
	got, note, err := ServiceExe("windows", exe, getenv, func(string) (string, error) { return "", errors.New("not found") })
	if err != nil || got != exe || note == nil || !strings.Contains(note.Error(), "Node v26.13.0's folder") {
		t.Errorf("ServiceExe = %q, %q, %v; want the path and a note on Node v26.13.0", got, note, err)
	}
}

// A refusal is a service.copy message whose step installs a lasting copy.
func TestServiceExeRefusalIsAMessage(t *testing.T) {
	_, _, err := ServiceExe("linux", "/home/u/.cache/go-build/ab/exe/nearhost", func(string) string { return "" }, nil)
	m, ok := report.As(err)
	if !ok || m.Code != report.ServiceCopy {
		t.Fatalf("err %v (%T), want a service.copy message", err, err)
	}
	if m.Title != "A service cannot run this copy of nearhost." || m.Why != "This copy was built by go run, so it will not be there at the next login." {
		t.Errorf("title %q, why %q", m.Title, m.Why)
	}
	if len(m.Fix) != 1 || m.Fix[0].Command != "nearhost service install" {
		t.Errorf("steps %+v", m.Fix)
	}
}

// The note on Node's folder is a notice with a code of its own.
func TestServiceExeNoteIsANotice(t *testing.T) {
	_, note, err := ServiceExe("linux", "/home/u/.nvm/versions/node/v26.13.0/bin/nearhost", func(string) string { return "" }, func(string) (string, error) { return "", errors.New("none") })
	if err != nil || note == nil || note.Code != report.ServiceNodeVersion || note.Level != report.Info {
		t.Fatalf("note %+v, err %v", note, err)
	}
}
