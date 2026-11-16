package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/update"
)

type fakeUpdate struct {
	latest   string
	err      error
	exe      string
	writable bool
	tty      bool
	goos     string
	ran      []update.Method
	runErr   error
	// events is stepAside, run and restore in the order they were called.
	events     []string
	asideErr   error
	restoreErr error
	// missing is whether the installer left no file at exe.
	missing bool
}

func (f *fakeUpdate) backend() updateBackend {
	return updateBackend{
		latest: func(context.Context) (string, error) { return f.latest, f.err },
		exe:    func() string { return f.exe },
		// Go's default GOPATH is under USERPROFILE on Windows.
		getenv:      func(k string) string { return map[string]string{"HOME": "/Users/me", "USERPROFILE": "/Users/me"}[k] },
		writable:    func(string) bool { return f.writable },
		interactive: func() bool { return f.tty },
		goos:        f.goos,
		run: func(_ context.Context, m update.Method, _, _ io.Writer) error {
			f.ran = append(f.ran, m)
			f.events = append(f.events, "run")
			return f.runErr
		},
		stepAside: func(exe string) (func() error, error) {
			f.events = append(f.events, "stepAside "+exe)
			if f.asideErr != nil {
				return nil, f.asideErr
			}
			return func() error {
				f.events = append(f.events, "restore")
				return f.restoreErr
			}, nil
		},
		exists: func(string) bool { return !f.missing },
	}
}

func runUpdate(t *testing.T, f *fakeUpdate, version, stdin string, args ...string) (string, error) {
	t.Helper()
	orig := Version
	t.Cleanup(func() { Version = orig })
	Version = version
	cmd := newUpdateCmd(f.backend())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestUpdateUpToDate(t *testing.T) {
	f := &fakeUpdate{latest: "0.5.0", exe: "/usr/local/bin/nearhost"}
	out, err := runUpdate(t, f, "0.5.0", "")
	if err != nil || !strings.Contains(out, "nearhost 0.5.0 is the latest") || len(f.ran) != 0 {
		t.Fatalf("out %q, err %v, ran %v", out, err, f.ran)
	}
}

func TestUpdateDevBuild(t *testing.T) {
	f := &fakeUpdate{latest: "0.6.0", exe: "/usr/local/bin/nearhost"}
	out, err := runUpdate(t, f, "dev", "", "--yes")
	if err != nil || !strings.Contains(out, "development build") || len(f.ran) != 0 {
		t.Fatalf("out %q, err %v, ran %v", out, err, f.ran)
	}
}

func TestUpdateCheckOnly(t *testing.T) {
	f := &fakeUpdate{latest: "0.6.0", exe: "/opt/homebrew/Caskroom/nearhost/0.5.0/nearhost"}
	out, err := runUpdate(t, f, "0.5.0", "", "--check")
	if err == nil || !strings.Contains(err.Error(), "0.6.0") || len(f.ran) != 0 {
		t.Fatalf("out %q, err %v, ran %v", out, err, f.ran)
	}
}

func TestUpdateCheckFailure(t *testing.T) {
	f := &fakeUpdate{err: errors.New("offline")}
	if _, err := runUpdate(t, f, "0.5.0", ""); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("err %v", err)
	}
}

func TestUpdatePromptsAndRuns(t *testing.T) {
	f := &fakeUpdate{latest: "0.6.0", exe: "/opt/homebrew/Caskroom/nearhost/0.5.0/nearhost", tty: true, goos: "darwin"}
	out, err := runUpdate(t, f, "0.5.0", "\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"0.5.0 → 0.6.0", "Homebrew", "brew upgrade --cask nearhost/tap/nearhost", "[Y/n]", "Updated to 0.6.0"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if len(f.ran) != 1 || f.ran[0].Kind != update.Homebrew {
		t.Fatalf("ran %v", f.ran)
	}
}

func TestUpdateDeclined(t *testing.T) {
	f := &fakeUpdate{latest: "0.6.0", exe: "/usr/local/bin/nearhost", writable: true, tty: true}
	out, err := runUpdate(t, f, "0.5.0", "n\n")
	if err != nil || len(f.ran) != 0 || !strings.Contains(out, "Not updated") {
		t.Fatalf("out %q, err %v, ran %v", out, err, f.ran)
	}
}

func TestUpdateNotATerminalNeedsYes(t *testing.T) {
	f := &fakeUpdate{latest: "0.6.0", exe: "/usr/local/bin/nearhost", writable: true}
	out, err := runUpdate(t, f, "0.5.0", "")
	if err != nil || len(f.ran) != 0 || !strings.Contains(out, "--yes") {
		t.Fatalf("out %q, err %v, ran %v", out, err, f.ran)
	}
	out, err = runUpdate(t, f, "0.5.0", "", "--yes")
	if err != nil || len(f.ran) != 1 || f.ran[0].Kind != update.Script {
		t.Fatalf("out %q, err %v, ran %v", out, err, f.ran)
	}
}

func TestUpdateScriptIntoUnwritableDir(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		f := &fakeUpdate{latest: "0.6.0", exe: "/usr/local/bin/nearhost", tty: true, goos: goos}
		out, err := runUpdate(t, f, "0.5.0", "", "--yes")
		want := "nearhost 0.5.0 → 0.6.0 is available. This copy was installed with the install script.\n" +
			"You cannot write to /usr/local/bin, so run the installer as root:\n\n" +
			"  curl -fsSL https://nearhost.dev/install.sh | sudo NEARHOST_INSTALL_DIR=/usr/local/bin sh\n"
		if err != nil || len(f.ran) != 0 || len(f.events) != 0 || out != want {
			t.Fatalf("%s: out %q, err %v, events %v", goos, out, err, f.events)
		}
	}
}

func TestUpdateScriptIntoUnwritableDirOnWindowsNamesPowerShell(t *testing.T) {
	f := &fakeUpdate{latest: "0.6.0", exe: `C:\Program Files\José Núñez\nearhost.exe`, tty: true, goos: "windows"}
	out, err := runUpdate(t, f, "0.5.0", "", "--yes")
	want := "You cannot write to C:\\Program Files\\José Núñez. Open Windows Terminal as administrator and run:\n\n" +
		"  $env:NEARHOST_INSTALL_DIR='C:\\Program Files\\José Núñez'; irm https://nearhost.dev/install.ps1 | iex\n"
	if err != nil || len(f.ran) != 0 || len(f.events) != 0 || !strings.HasSuffix(out, want) {
		t.Fatalf("out %q, err %v, events %v", out, err, f.events)
	}
	if strings.Contains(out, "curl") || strings.Contains(out, "sudo") {
		t.Fatalf("a Windows user was told to use curl or sudo:\n%s", out)
	}
}

// On Windows the running nearhost.exe is moved out of the way before the
// installer runs, since Windows will not let anything overwrite it.
func TestUpdateOnWindowsStepsAsideFirst(t *testing.T) {
	exe := `C:\Users\José Núñez\AppData\Roaming\npm\node_modules\@nearhost\cli-win32-x64\bin\nearhost.exe`
	f := &fakeUpdate{latest: "0.6.0", exe: exe, goos: "windows"}
	out, err := runUpdate(t, f, "0.5.0", "", "--yes")
	if err != nil || !strings.Contains(out, "Updated to 0.6.0") {
		t.Fatalf("out %q, err %v", out, err)
	}
	if want := []string{"stepAside " + exe, "run"}; !slices.Equal(f.events, want) {
		t.Fatalf("events %v, want %v", f.events, want)
	}
}

func TestUpdateOnWindowsRestoresWhenTheInstallerFails(t *testing.T) {
	f := &fakeUpdate{latest: "0.6.0", exe: `C:\Tools\nearhost.exe`, goos: "windows", writable: true, runErr: errors.New("exit status 1")}
	out, err := runUpdate(t, f, "0.5.0", "", "--yes")
	if want := []string{`stepAside C:\Tools\nearhost.exe`, "run", "restore"}; !slices.Equal(f.events, want) {
		t.Fatalf("events %v, want %v", f.events, want)
	}
	if err == nil || !strings.Contains(err.Error(), "install.ps1") || !strings.Contains(err.Error(), "exit status 1") ||
		!strings.Contains(err.Error(), `the old nearhost is back at C:\Tools\nearhost.exe`) || strings.Contains(out, "Updated") {
		t.Fatalf("out %q, err %v", out, err)
	}
}

func TestUpdateOnWindowsRestoresWhenTheInstallerLeavesNoBinary(t *testing.T) {
	f := &fakeUpdate{latest: "0.6.0", exe: `C:\Tools\nearhost.exe`, goos: "windows", writable: true, missing: true}
	out, err := runUpdate(t, f, "0.5.0", "", "--yes")
	if want := []string{`stepAside C:\Tools\nearhost.exe`, "run", "restore"}; !slices.Equal(f.events, want) {
		t.Fatalf("events %v, want %v", f.events, want)
	}
	if err == nil || !strings.Contains(err.Error(), `left no nearhost at C:\Tools\nearhost.exe`) ||
		!strings.Contains(err.Error(), "the old nearhost is back") || strings.Contains(out, "Updated") {
		t.Fatalf("out %q, err %v", out, err)
	}
}

// If the old copy cannot be put back either, the error says where it is.
func TestUpdateOnWindowsSaysWhenRestoreFails(t *testing.T) {
	f := &fakeUpdate{latest: "0.6.0", exe: `C:\Tools\nearhost.exe`, goos: "windows", writable: true,
		runErr: errors.New("exit status 1"), restoreErr: errors.New(`move C:\old\nearhost-1.exe back to C:\Tools\nearhost.exe: access denied`)}
	_, err := runUpdate(t, f, "0.5.0", "", "--yes")
	if err == nil || !strings.Contains(err.Error(), "could not be put back") || !strings.Contains(err.Error(), `C:\old\nearhost-1.exe`) ||
		strings.Contains(err.Error(), "is back") {
		t.Fatalf("err %v", err)
	}
}

func TestUpdateOnWindowsRunsNothingWhenItCannotStepAside(t *testing.T) {
	f := &fakeUpdate{latest: "0.6.0", exe: `C:\Tools\nearhost.exe`, goos: "windows", writable: true, asideErr: errors.New("access denied")}
	out, err := runUpdate(t, f, "0.5.0", "", "--yes")
	if err == nil || !strings.Contains(err.Error(), "access denied") || len(f.ran) != 0 || strings.Contains(out, "Updated") {
		t.Fatalf("out %q, err %v, ran %v", out, err, f.ran)
	}
}

func TestUpdateOffWindowsNeverStepsAside(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		for _, runErr := range []error{nil, errors.New("exit status 1")} {
			f := &fakeUpdate{latest: "0.6.0", exe: "/Users/me/go/bin/nearhost", goos: goos, missing: true, runErr: runErr}
			_, err := runUpdate(t, f, "0.5.0", "", "--yes")
			if want := []string{"run"}; !slices.Equal(f.events, want) {
				t.Fatalf("%s: events %v, want %v", goos, f.events, want)
			}
			if (err != nil) != (runErr != nil) {
				t.Fatalf("%s: err %v", goos, err)
			}
		}
	}
}

func TestUpdateNpx(t *testing.T) {
	f := &fakeUpdate{latest: "0.6.0", exe: "/Users/me/.npm/_npx/abc/node_modules/@nearhost/cli-darwin-arm64/bin/nearhost"}
	out, err := runUpdate(t, f, "0.5.0", "", "--yes")
	if err != nil || len(f.ran) != 0 || !strings.Contains(out, "npx @nearhost/cli@latest") {
		t.Fatalf("out %q, err %v, ran %v", out, err, f.ran)
	}
}

func TestUpdateRunFailure(t *testing.T) {
	f := &fakeUpdate{latest: "0.6.0", exe: "/Users/me/go/bin/nearhost", runErr: errors.New("exit status 1")}
	if _, err := runUpdate(t, f, "0.5.0", "", "--yes"); err == nil || !strings.Contains(err.Error(), "go install") {
		t.Fatalf("err %v", err)
	}
}

func TestUpdateLinuxRemindsSetcap(t *testing.T) {
	f := &fakeUpdate{latest: "0.6.0", exe: "/Users/me/go/bin/nearhost", goos: "linux"}
	out, err := runUpdate(t, f, "0.5.0", "", "--yes")
	if err != nil || !strings.Contains(out, "sudo setcap cap_net_bind_service=+ep '/Users/me/go/bin/nearhost'") {
		t.Fatalf("out %q, err %v", out, err)
	}
	f = &fakeUpdate{latest: "0.6.0", exe: "/usr/lib/node_modules/@nearhost/cli-linux-x64/bin/nearhost", goos: "linux"}
	out, _ = runUpdate(t, f, "0.5.0", "", "--yes")
	if strings.Contains(out, "setcap") {
		t.Fatalf("npm update asked for setcap, which npm installs never need from here:\n%s", out)
	}
}

func TestShouldCheckForUpdate(t *testing.T) {
	ok := func(string) string { return "" }
	for _, tc := range []struct {
		name     string
		args     []string
		getenv   func(string) string
		version  string
		elevated bool
		tty      bool
		want     bool
	}{
		{"ui", nil, ok, "0.5.0", false, true, true},
		{"up", []string{"up"}, ok, "0.5.0", false, true, true},
		{"update itself", []string{"update"}, ok, "0.5.0", false, true, false},
		{"version", []string{"version"}, ok, "0.5.0", false, true, false},
		{"help", []string{"--help"}, ok, "0.5.0", false, true, false},
		{"completion", []string{"completion", "zsh"}, ok, "0.5.0", false, true, false},
		{"hosts helper under sudo", []string{"hosts", "apply"}, ok, "0.5.0", true, true, false},
		{"not a terminal", []string{"ls"}, ok, "0.5.0", false, false, false},
		{"dev build", []string{"ls"}, ok, "dev", false, true, false},
		{"opted out", []string{"ls"}, func(k string) string {
			if k == "NEARHOST_NO_UPDATE_CHECK" {
				return "1"
			}
			return ""
		}, "0.5.0", false, true, false},
	} {
		if got := shouldCheckForUpdate(tc.args, tc.getenv, tc.version, tc.elevated, tc.tty); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestBackgroundCheckNotice(t *testing.T) {
	c := startBackgroundCheck("0.5.0", func(context.Context) (string, error) { return "0.6.0", nil })
	<-c.done
	if got := c.notice(0); !strings.Contains(got, "0.5.0 → 0.6.0") || !strings.Contains(got, "nearhost update") {
		t.Fatalf("notice %q", got)
	}
	c = startBackgroundCheck("0.6.0", func(context.Context) (string, error) { return "0.6.0", nil })
	<-c.done
	if got := c.notice(0); got != "" {
		t.Fatalf("up to date, but notice %q", got)
	}
	block := make(chan struct{})
	defer close(block)
	c = startBackgroundCheck("0.5.0", func(context.Context) (string, error) { <-block; return "0.6.0", nil })
	if got := c.notice(0); got != "" {
		t.Fatalf("an unfinished check gave notice %q", got)
	}
	var nilCheck *backgroundCheck
	if nilCheck.notice(0) != "" {
		t.Fatal("nil check gave a notice")
	}
}

// The first check of the day, asking GitHub, has the grace to finish and
// fill the cache.
func TestColdCheckFinishesWithinTheGrace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(700 * time.Millisecond)
		io.WriteString(w, `{"tag_name":"v0.6.0"}`)
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "update.json")
	checker := update.Checker{URL: srv.URL, CachePath: path, HTTP: backgroundHTTP()}
	c := startBackgroundCheck("0.5.0", checker.Cached)
	if got := c.notice(updateGrace); got == "" {
		t.Fatal("a check finishing within the grace gave no notice")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the cache was not written: %v", err)
	}
}

// A GitHub that does not answer still leaves a cache entry before the
// command's grace ends, so later runs do not wait on it again.
func TestHungCheckSavesTheCacheWithinTheGrace(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	path := filepath.Join(t.TempDir(), "update.json")
	checker := update.Checker{URL: srv.URL, CachePath: path, HTTP: backgroundHTTP()}
	c := startBackgroundCheck("0.5.0", checker.Cached)
	select {
	case <-c.done:
	case <-time.After(updateGrace):
		t.Fatal("the check outlasted the grace")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the failed check was not cached: %v", err)
	}
}

func TestUpdatePrereleaseBuildInstallsThePinnedVersion(t *testing.T) {
	f := &fakeUpdate{latest: "0.13.0", exe: "/usr/local/lib/node_modules/@nearhost/cli-linux-x64/bin/nearhost"}
	out, err := runUpdate(t, f, "0.13.0-rc.2", "", "--yes")
	if err != nil || len(f.ran) != 1 || f.ran[0].String() != "npm install -g @nearhost/cli@0.13.0" {
		t.Fatalf("out %q, err %v, ran %v", out, err, f.ran)
	}
	f = &fakeUpdate{latest: "0.13.0", exe: "/usr/local/lib/node_modules/@nearhost/cli-linux-x64/bin/nearhost"}
	if _, err := runUpdate(t, f, "0.12.0", "", "--yes"); err != nil || len(f.ran) != 1 || f.ran[0].String() != "npm install -g @nearhost/cli@latest" {
		t.Fatalf("normal build ran %v, err %v", f.ran, err)
	}
}

func TestUpdatePrereleaseNpxNamesTheVersion(t *testing.T) {
	f := &fakeUpdate{latest: "0.13.0-rc.2", exe: "/Users/me/.npm/_npx/abc/node_modules/@nearhost/cli-darwin-arm64/bin/nearhost"}
	out, err := runUpdate(t, f, "0.13.0-rc.1", "", "--yes")
	if err != nil || !strings.Contains(out, "npx @nearhost/cli@0.13.0-rc.2") {
		t.Fatalf("out %q, err %v", out, err)
	}
}

func TestUpdateOnWindowsRunsPowerShell(t *testing.T) {
	f := &fakeUpdate{latest: "0.13.0-rc.2", exe: `C:\Tools\nearhost.exe`, goos: "windows", writable: true}
	out, err := runUpdate(t, f, "0.13.0-rc.1", "", "--yes")
	if err != nil || len(f.ran) != 1 || f.ran[0].Args[0] != "powershell.exe" ||
		f.ran[0].String() != `$env:NEARHOST_VERSION='v0.13.0-rc.2'; irm https://nearhost.dev/install.ps1 | iex` {
		t.Fatalf("out %q, err %v, ran %v", out, err, f.ran)
	}
}

func TestBackgroundNoticeAlongThePrereleaseLine(t *testing.T) {
	for _, tc := range []struct {
		current, latest string
		want            bool
	}{
		{"0.13.0-rc.1", "0.13.0-rc.2", true},
		{"0.13.0-rc.2", "0.13.0", true},
		{"0.13.0-rc.2", "0.12.5", false},
		{"0.12.0", "0.13.0-rc.1", false},
	} {
		tc := tc
		c := startBackgroundCheck(tc.current, func(context.Context) (string, error) { return tc.latest, nil })
		got := c.notice(time.Second) != ""
		if got != tc.want {
			t.Errorf("notice for %s -> %s = %v, want %v", tc.current, tc.latest, got, tc.want)
		}
	}
}
