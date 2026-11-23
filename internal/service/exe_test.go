package service

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
			if tc.note == "" && note != "" || !strings.Contains(note, tc.note) {
				t.Errorf("note = %q, want one containing %q", note, tc.note)
			}
		})
	}
}

func TestServiceExeRefusalsSayWhatToDo(t *testing.T) {
	_, _, err := ServiceExe("linux", "/home/u/.cache/go-build/ab/exe/nearhost", func(string) string { return "" }, nil)
	want := "This copy of nearhost was built by go run, so a service could not start it later. " +
		"Install nearhost (https://nearhost.dev/docs/install/), then run nearhost service install from that copy."
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
}
