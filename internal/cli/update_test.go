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
	"runtime"
	"runtime/debug"
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
	// events is download, stepAside, rename, run, restore and remove in the
	// order they were called.
	events []string
	// downloaded is each download asked for, as "<version> <path>".
	downloaded  []string
	downloadErr error
	// onDownload, when set, is the download instead of returning
	// downloadErr; it gets the download's context and the signals the
	// update holds.
	onDownload func(ctx context.Context, signals chan<- os.Signal) error
	// onStepAside, when set, runs as the running binary is moved aside.
	onStepAside func(signals chan<- os.Signal)
	asideErr    error
	restoreErr  error
	renameErr   error
	// limit is the download's time limit; 0 means a minute.
	limit time.Duration
	// missing is whether the installer left no file at exe.
	missing bool
	// present, when set, lists the paths that exist instead.
	present       map[string]bool
	writableCalls int
	// installed is the version `<path> version` reports after the update;
	// "" means latest. probeErr is its failure to start.
	installed string
	probeErr  error
	// probed is each path whose version was asked for.
	probed []string
	// onPath is where `nearhost` is found on PATH, or "" when it is not.
	onPath string
	// env is added to the environment the backend reads.
	env map[string]string
	// links maps a path to what its symlinks resolve to; other paths
	// resolve to themselves.
	links map[string]string
	// files is the content of the files readHead reads.
	files map[string]string
	// capped lists the paths that have cap_net_bind_service; capUnknown is
	// that the probe cannot tell, and capProbed each path it was asked
	// about.
	capped     map[string]bool
	capUnknown bool
	capProbed  []string
	// regular, when set, lists the paths that are regular files instead
	// of every path.
	regular map[string]bool
	// lowStart is the first unprivileged port; 0 means 1024.
	// lowUnreadable is that it could not be read.
	lowStart      int
	lowUnreadable bool
}

func (f *fakeUpdate) backend() updateBackend {
	signals := make(chan os.Signal, 1)
	limit := f.limit
	if limit == 0 {
		limit = time.Minute
	}
	return updateBackend{
		latest: func(context.Context) (string, error) { return f.latest, f.err },
		exe:    func() string { return f.exe },
		// Go's default GOPATH is under USERPROFILE on Windows.
		getenv: func(k string) string {
			if v, ok := f.env[k]; ok {
				return v
			}
			return map[string]string{"HOME": "/Users/me", "USERPROFILE": "/Users/me"}[k]
		},
		writable:    func(string) bool { f.writableCalls++; return f.writable },
		interactive: func() bool { return f.tty },
		goos:        f.goos,
		run: func(_ context.Context, m update.Method, _, _ io.Writer) error {
			f.ran = append(f.ran, m)
			f.events = append(f.events, "run")
			return f.runErr
		},
		download: func(ctx context.Context, version, path string) error {
			f.downloaded = append(f.downloaded, version+" "+path)
			f.events = append(f.events, "download "+path)
			if f.onDownload != nil {
				return f.onDownload(ctx, signals)
			}
			return f.downloadErr
		},
		stepAside: func(exe string) (func() error, error) {
			f.events = append(f.events, "stepAside "+exe)
			if f.onStepAside != nil {
				f.onStepAside(signals)
			}
			if f.asideErr != nil {
				return nil, f.asideErr
			}
			return func() error {
				f.events = append(f.events, "restore")
				return f.restoreErr
			}, nil
		},
		exists: func(p string) bool {
			if f.present != nil {
				return f.present[p]
			}
			return !f.missing
		},
		rename: func(from, to string) error {
			f.events = append(f.events, "rename "+from+" "+to)
			return f.renameErr
		},
		remove: func(path string) error {
			f.events = append(f.events, "remove "+path)
			return nil
		},
		hold:          func() (<-chan os.Signal, func()) { return signals, func() {} },
		downloadLimit: limit,
		probe: func(_ context.Context, path string) (string, error) {
			f.probed = append(f.probed, path)
			if f.probeErr != nil {
				return "", f.probeErr
			}
			if f.installed != "" {
				return f.installed, nil
			}
			return f.latest, nil
		},
		lookPath: func(name string) (string, error) {
			if f.onPath == "" {
				return "", errors.New(name + " not found")
			}
			return f.onPath, nil
		},
		realPath: func(p string) (string, error) {
			if to, ok := f.links[p]; ok {
				return to, nil
			}
			return p, nil
		},
		readHead: func(p string) ([]byte, error) {
			if c, ok := f.files[p]; ok {
				return []byte(c), nil
			}
			return []byte("\x7fELF"), nil
		},
		isFile: func(p string) bool { return f.regular == nil || f.regular[p] },
		unprivilegedPortStart: func() (int, bool) {
			if f.lowStart == 0 {
				return 1024, !f.lowUnreadable
			}
			return f.lowStart, !f.lowUnreadable
		},
		netBindCap: func(p string) (bool, bool) {
			f.capProbed = append(f.capProbed, p)
			return f.capped[p], !f.capUnknown
		},
	}
}

func runUpdate(t *testing.T, f *fakeUpdate, version, stdin string, args ...string) (string, error) {
	t.Helper()
	orig := Version
	t.Cleanup(func() { Version = orig })
	Version = version
	cmd := newUpdateCmd(f.backend())
	// The root command, which these tests leave out, prints errors itself.
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
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
		"  $env:NEARHOST_INSTALL_DIR='C:\\Program Files\\José Núñez'; $env:NEARHOST_NO_PATH='1'; irm https://nearhost.dev/install.ps1 | iex\n"
	if err != nil || len(f.ran) != 0 || len(f.events) != 0 || !strings.HasSuffix(out, want) {
		t.Fatalf("out %q, err %v, events %v", out, err, f.events)
	}
	if strings.Contains(out, "curl") || strings.Contains(out, "sudo") {
		t.Fatalf("a Windows user was told to use curl or sudo:\n%s", out)
	}
}

// A folder whose name has an apostrophe, straight or curly, is quoted for
// PowerShell, which would otherwise end the string there.
func TestUpdateUnwritableAdviceQuotesTheFolder(t *testing.T) {
	for _, tc := range []struct{ dir, quoted string }{
		{`C:\Users\Seán O'Brien\bin`, `C:\Users\Seán O''Brien\bin`},
		{"C:\\Users\\Seán O\u2019Brien\\bin", "C:\\Users\\Seán O\u2019\u2019Brien\\bin"},
	} {
		f := &fakeUpdate{latest: "0.6.0", exe: tc.dir + `\nearhost.exe`, goos: "windows"}
		out, err := runUpdate(t, f, "0.5.0", "", "--yes")
		want := "  $env:NEARHOST_INSTALL_DIR='" + tc.quoted + "'; $env:NEARHOST_NO_PATH='1'; irm https://nearhost.dev/install.ps1 | iex\n"
		if err != nil || len(f.events) != 0 || !strings.HasSuffix(out, want) {
			t.Errorf("out %q, err %v; want it to end %q", out, err, want)
		}
	}
}

// A pre-release build installs a named version, so the run-as-root advice
// must name it too, or it would install the latest normal release.
func TestUpdateUnwritableAdviceNamesThePrereleaseVersion(t *testing.T) {
	for _, tc := range []struct{ goos, exe, want string }{
		{"linux", "/usr/local/bin/nearhost", "  curl -fsSL https://nearhost.dev/install.sh | sudo NEARHOST_INSTALL_DIR=/usr/local/bin NEARHOST_VERSION=v0.13.0-rc.2 sh\n"},
		{"windows", `C:\Program Files\nearhost\nearhost.exe`, "  $env:NEARHOST_INSTALL_DIR='C:\\Program Files\\nearhost'; $env:NEARHOST_NO_PATH='1'; $env:NEARHOST_VERSION='v0.13.0-rc.2'; irm https://nearhost.dev/install.ps1 | iex\n"},
	} {
		f := &fakeUpdate{latest: "0.13.0-rc.2", exe: tc.exe, goos: tc.goos}
		out, err := runUpdate(t, f, "0.13.0-rc.1", "", "--yes")
		if err != nil || len(f.events) != 0 || !strings.HasSuffix(out, tc.want) {
			t.Errorf("%s: out %q, err %v, events %v", tc.goos, out, err, f.events)
		}
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
	exe := `/Users/me/go/bin/nearhost.exe` // %USERPROFILE%\go\bin in the fake
	f := &fakeUpdate{latest: "0.6.0", exe: exe, goos: "windows", writable: true, runErr: errors.New("exit status 1")}
	out, err := runUpdate(t, f, "0.5.0", "", "--yes")
	if want := []string{"stepAside " + exe, "run", "restore"}; !slices.Equal(f.events, want) {
		t.Fatalf("events %v, want %v", f.events, want)
	}
	if err == nil || !strings.Contains(err.Error(), "go install") || !strings.Contains(err.Error(), "exit status 1") ||
		!strings.Contains(err.Error(), "the old nearhost is back at "+exe) || strings.Contains(out, "Updated") {
		t.Fatalf("out %q, err %v", out, err)
	}
}

func TestUpdateOnWindowsRestoresWhenTheInstallerLeavesNoBinary(t *testing.T) {
	exe := `/Users/me/go/bin/nearhost.exe`
	f := &fakeUpdate{latest: "0.6.0", exe: exe, goos: "windows", writable: true, missing: true}
	out, err := runUpdate(t, f, "0.5.0", "", "--yes")
	if want := []string{"stepAside " + exe, "run", "restore"}; !slices.Equal(f.events, want) {
		t.Fatalf("events %v, want %v", f.events, want)
	}
	if err == nil || !strings.Contains(err.Error(), "left no nearhost at "+exe) ||
		!strings.Contains(err.Error(), "the old nearhost is back") || strings.Contains(out, "Updated") {
		t.Fatalf("out %q, err %v", out, err)
	}
}

// A copy from the PowerShell installer or a zip is updated by nearhost
// itself: it downloads the release beside the running copy, and only then
// moves the running copy aside and the new one into its place. It never
// starts PowerShell, which Microsoft Defender blocks as malware when a
// program starts it with `irm … | iex`.
func TestUpdateOnWindowsDownloadsAScriptCopyItself(t *testing.T) {
	exe := `C:\Users\José Núñez\bin\nh.exe` // renamed copies are updated where they are
	f := &fakeUpdate{latest: "0.13.0-rc.2", exe: exe, goos: "windows", writable: true}
	out, err := runUpdate(t, f, "0.13.0-rc.1", "", "--yes")
	if err != nil || !strings.Contains(out, "Updated to 0.13.0-rc.2") {
		t.Fatalf("out %q, err %v", out, err)
	}
	if want := []string{"download " + exe + ".new", "stepAside " + exe, "rename " + exe + ".new " + exe}; !slices.Equal(f.events, want) || len(f.ran) != 0 {
		t.Fatalf("events %v, want %v; ran %v", f.events, want, f.ran)
	}
	if want := []string{"0.13.0-rc.2 " + exe + ".new"}; !slices.Equal(f.downloaded, want) {
		t.Fatalf("downloaded %q, want %q", f.downloaded, want)
	}
	if strings.Contains(out, "install.ps1") || strings.Contains(out, "powershell") {
		t.Fatalf("out names the PowerShell installer:\n%s", out)
	}
}

// A failed download, a checksum that does not match among them, moves
// nothing: the running copy stays where it is. The download removes what it
// wrote; the update removes nothing, as a file left at <exe>.new may belong
// to another update running at the same moment.
func TestUpdateOnWindowsFailedDownloadMovesNothing(t *testing.T) {
	exe := `C:\Tools\nearhost.exe`
	f := &fakeUpdate{latest: "0.6.0", exe: exe, goos: "windows", writable: true, downloadErr: errors.New("checksum mismatch")}
	out, err := runUpdate(t, f, "0.5.0", "", "--yes")
	if want := []string{"download " + exe + ".new"}; !slices.Equal(f.events, want) {
		t.Fatalf("events %v, want %v", f.events, want)
	}
	if err == nil || !strings.Contains(err.Error(), "nearhost 0.6.0 could not be downloaded: checksum mismatch") ||
		!strings.Contains(err.Error(), exe+" is unchanged") || strings.Contains(out, "Updated") {
		t.Fatalf("out %q, err %v", out, err)
	}
}

// Ctrl-C during the download ends it, and the running copy stays in place.
func TestUpdateOnWindowsCtrlCDuringTheDownloadLeavesTheExe(t *testing.T) {
	exe := `C:\Tools\nearhost.exe`
	f := &fakeUpdate{latest: "0.6.0", exe: exe, goos: "windows", writable: true,
		onDownload: func(ctx context.Context, signals chan<- os.Signal) error {
			signals <- os.Interrupt
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(10 * time.Second):
				return errors.New("Ctrl-C did not end the download")
			}
		}}
	out, err := runUpdate(t, f, "0.5.0", "", "--yes")
	if want := []string{"download " + exe + ".new"}; !slices.Equal(f.events, want) {
		t.Fatalf("events %v, want %v", f.events, want)
	}
	if err == nil || !strings.Contains(err.Error(), "cancelled") || !strings.Contains(err.Error(), exe+" is unchanged") ||
		strings.Contains(err.Error(), "5 minutes") || strings.Contains(out, "Updated") {
		t.Fatalf("out %q, err %v", out, err)
	}
}

// A download that outlasts its limit ends with a message that says so.
func TestUpdateOnWindowsDownloadTimeLimit(t *testing.T) {
	if got := systemUpdate().downloadLimit; got != 5*time.Minute {
		t.Fatalf("the download limit is %v; the message says 5 minutes", got)
	}
	exe := `C:\Tools\nearhost.exe`
	f := &fakeUpdate{latest: "0.6.0", exe: exe, goos: "windows", writable: true, limit: 20 * time.Millisecond,
		onDownload: func(ctx context.Context, _ chan<- os.Signal) error {
			<-ctx.Done()
			return ctx.Err()
		}}
	_, err := runUpdate(t, f, "0.5.0", "", "--yes")
	if want := []string{"download " + exe + ".new"}; !slices.Equal(f.events, want) {
		t.Fatalf("events %v, want %v", f.events, want)
	}
	if err == nil || !strings.Contains(err.Error(), "the download took longer than 5 minutes") || strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("err %v", err)
	}
}

// Ctrl-C once the old copy has moved does not stop the update halfway: the
// new copy still goes into place.
func TestUpdateOnWindowsCtrlCWhileSwappingFinishes(t *testing.T) {
	exe := `C:\Tools\nearhost.exe`
	f := &fakeUpdate{latest: "0.6.0", exe: exe, goos: "windows", writable: true,
		onStepAside: func(signals chan<- os.Signal) { signals <- os.Interrupt }}
	out, err := runUpdate(t, f, "0.5.0", "", "--yes")
	if want := []string{"download " + exe + ".new", "stepAside " + exe, "rename " + exe + ".new " + exe}; !slices.Equal(f.events, want) {
		t.Fatalf("events %v, want %v", f.events, want)
	}
	if err != nil || !strings.Contains(out, "Updated to 0.6.0") {
		t.Fatalf("out %q, err %v", out, err)
	}
}

// When the new copy cannot be renamed into place, the old one goes back and
// the new file goes.
func TestUpdateOnWindowsRestoresWhenTheRenameFails(t *testing.T) {
	exe := `C:\Tools\nearhost.exe`
	f := &fakeUpdate{latest: "0.6.0", exe: exe, goos: "windows", writable: true, renameErr: errors.New("sharing violation")}
	out, err := runUpdate(t, f, "0.5.0", "", "--yes")
	if want := []string{"download " + exe + ".new", "stepAside " + exe, "rename " + exe + ".new " + exe, "restore", "remove " + exe + ".new"}; !slices.Equal(f.events, want) {
		t.Fatalf("events %v, want %v", f.events, want)
	}
	if err == nil || !strings.Contains(err.Error(), "sharing violation") ||
		!strings.Contains(err.Error(), "the old nearhost is back at "+exe) || strings.Contains(out, "Updated") {
		t.Fatalf("out %q, err %v", out, err)
	}
}

// If the old copy cannot be put back either, the error says where it is,
// and the checked new copy stays at <exe>.new as a second way back.
func TestUpdateOnWindowsSaysWhenRestoreFails(t *testing.T) {
	exe := `C:\Tools\nearhost.exe`
	f := &fakeUpdate{latest: "0.6.0", exe: exe, goos: "windows", writable: true,
		renameErr: errors.New("sharing violation"), restoreErr: errors.New(`move C:\old\nearhost-1.exe back to C:\Tools\nearhost.exe: access denied`)}
	_, err := runUpdate(t, f, "0.5.0", "", "--yes")
	if err == nil || !strings.Contains(err.Error(), "could not be put back") || !strings.Contains(err.Error(), `C:\old\nearhost-1.exe`) ||
		!strings.Contains(err.Error(), "The new nearhost is at "+exe+".new; rename it to "+exe) || strings.Contains(err.Error(), "is back") {
		t.Fatalf("err %v", err)
	}
	if want := []string{"download " + exe + ".new", "stepAside " + exe, "rename " + exe + ".new " + exe, "restore"}; !slices.Equal(f.events, want) {
		t.Fatalf("events %v, want %v", f.events, want)
	}
}

// Through the real download, a checksum that does not match leaves only
// the running copy in its folder: the download's own cleanup removes
// <exe>.new.
func TestUpdateOnWindowsChecksumMismatchLeavesOnlyTheExe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/checksums.txt") {
			io.WriteString(w, strings.Repeat("0", 64)+"  nearhost_0.6.0_windows_amd64.zip\n")
			return
		}
		io.WriteString(w, "not the release")
	}))
	defer srv.Close()
	dir := t.TempDir()
	exe := filepath.Join(dir, "nearhost.exe")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &fakeUpdate{latest: "0.6.0", exe: exe, goos: "windows", writable: true}
	b := f.backend()
	b.download = update.Downloader{HTTP: srv.Client(), BaseURL: srv.URL, GOARCH: "amd64"}.Fetch
	orig := Version
	t.Cleanup(func() { Version = orig })
	Version = "0.5.0"
	cmd := newUpdateCmd(b)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--yes"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("out %q, err %v", out.String(), err)
	}
	if len(f.events) != 0 {
		t.Fatalf("events %v, want none", f.events)
	}
	es, _ := os.ReadDir(dir)
	if len(es) != 1 || es[0].Name() != "nearhost.exe" {
		t.Fatalf("%s holds %v, want only nearhost.exe", dir, es)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old" {
		t.Fatalf("exe holds %q", got)
	}
}

func TestUpdateOnWindowsRunsNothingWhenItCannotStepAside(t *testing.T) {
	exe := `C:\Tools\nearhost.exe`
	f := &fakeUpdate{latest: "0.6.0", exe: exe, goos: "windows", writable: true, asideErr: errors.New("access denied")}
	out, err := runUpdate(t, f, "0.5.0", "", "--yes")
	if err == nil || !strings.Contains(err.Error(), "access denied") || len(f.ran) != 0 || strings.Contains(out, "Updated") {
		t.Fatalf("out %q, err %v, ran %v", out, err, f.ran)
	}
	if want := []string{"download " + exe + ".new", "stepAside " + exe, "remove " + exe + ".new"}; !slices.Equal(f.events, want) {
		t.Fatalf("events %v, want %v", f.events, want)
	}
}

// The download's client has no overall timeout, which would cut off a slow
// download that is still going, but it gives up on a server that does not
// answer.
func TestUpdateDownloadClient(t *testing.T) {
	c := downloadHTTP()
	if c.Timeout != 0 {
		t.Fatalf("the client times out after %v", c.Timeout)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok || tr.ResponseHeaderTimeout == 0 || tr.TLSHandshakeTimeout == 0 {
		t.Fatalf("transport %#v gives up on nothing", c.Transport)
	}
}

// pnpm's global store has the version in the path, so the new binary is
// never at the old one's path; its exit status is what counts.
func TestUpdateOnWindowsTrustsPnpm(t *testing.T) {
	exe := `C:\Users\x\AppData\Local\pnpm\global\5\node_modules\.pnpm\@nearhost+cli-win32-x64@0.13.0\node_modules\@nearhost\cli-win32-x64\bin\nearhost.exe`
	f := &fakeUpdate{latest: "0.14.0", exe: exe, goos: "windows", missing: true}
	out, err := runUpdate(t, f, "0.13.0", "", "--yes")
	if err != nil || !strings.Contains(out, "Updated to 0.14.0") {
		t.Fatalf("out %q, err %v", out, err)
	}
	if want := []string{"stepAside " + exe, "run"}; !slices.Equal(f.events, want) {
		t.Fatalf("events %v, want %v", f.events, want)
	}
}

// A copy renamed from nearhost.exe: `go install` puts the new version at
// nearhost.exe, and the renamed copy goes back to being the old one. The
// copy the user runs was not updated, so the command fails and says where
// the new one is.
func TestUpdateOnWindowsRenamedCopy(t *testing.T) {
	for _, tc := range []struct {
		kind update.Kind
		exe  string
	}{
		{update.Go, `/Users/me/go/bin/nh.exe`}, // %USERPROFILE%\go\bin in the fake
	} {
		dir := tc.exe[:strings.LastIndexAny(tc.exe, `\/`)]
		installed := dir + `\nearhost.exe`
		f := &fakeUpdate{latest: "0.6.0", exe: tc.exe, goos: "windows", writable: true, present: map[string]bool{installed: true}}
		out, err := runUpdate(t, f, "0.5.0", "", "--yes")
		if want := []string{"stepAside " + tc.exe, "run", "restore"}; !slices.Equal(f.events, want) || f.ran[0].Kind != tc.kind {
			t.Fatalf("%s: events %v, want %v, ran %v", tc.kind, f.events, want, f.ran)
		}
		if err == nil || !strings.Contains(err.Error(), "installed the new nearhost at "+installed) ||
			!strings.Contains(err.Error(), tc.exe+" is still the old one") || strings.Contains(out, "Updated") {
			t.Fatalf("%s: out %q, err %v", tc.kind, out, err)
		}
	}
}

// The Windows advice must not add a second write test to the folder off
// Windows.
func TestUpdateTestsTheFolderOnce(t *testing.T) {
	f := &fakeUpdate{latest: "0.6.0", exe: "/usr/local/bin/nearhost", goos: "linux", writable: true}
	if _, err := runUpdate(t, f, "0.5.0", "", "--yes"); err != nil || f.writableCalls != 1 {
		t.Fatalf("err %v, writable called %d times", err, f.writableCalls)
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
	nodeArch := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	const pnpmStore = "/home/me/.local/share/pnpm/global/5/.pnpm/@nearhost+cli@0.6.0/node_modules/@nearhost"
	const setcap = "Let the new binary bind ports 80 and 443 again:\n\n  sudo setcap cap_net_bind_service=+ep "
	shellShim := "#!/bin/sh\nexec node \"$basedir/global/5/.pnpm/@nearhost+cli@0.6.0/node_modules/@nearhost/cli/bin/nearhost.js\" \"$@\"\n"
	for _, tc := range []struct {
		name string
		f    *fakeUpdate
		want string
	}{
		{"go", &fakeUpdate{exe: "/home/me/go/bin/nearhost", env: map[string]string{"HOME": "/home/me"}}, "'/home/me/go/bin/nearhost'"},
		{"script", &fakeUpdate{exe: "/usr/local/bin/nearhost", writable: true}, "'/usr/local/bin/nearhost'"},
		{"npm", &fakeUpdate{exe: "/usr/lib/node_modules/@nearhost/cli-linux-x64/bin/nearhost"}, "'/usr/lib/node_modules/@nearhost/cli-linux-x64/bin/nearhost'"},
		{"bun", &fakeUpdate{exe: "/home/me/.bun/install/global/node_modules/@nearhost/cli-linux-x64/bin/nearhost"}, "'/home/me/.bun/install/global/node_modules/@nearhost/cli-linux-x64/bin/nearhost'"},
		{"homebrew", &fakeUpdate{exe: "/home/linuxbrew/.linuxbrew/Caskroom/nearhost/0.5.0/nearhost",
			links: map[string]string{"/home/linuxbrew/.linuxbrew/bin/nearhost": "/home/linuxbrew/.linuxbrew/Caskroom/nearhost/0.6.0/nearhost"}},
			"'/home/linuxbrew/.linuxbrew/Caskroom/nearhost/0.6.0/nearhost'"},
		{"pnpm shell shim", &fakeUpdate{exe: "/home/me/.local/share/pnpm/global/5/.pnpm/@nearhost+cli-linux-x64@0.5.0/node_modules/@nearhost/cli-linux-x64/bin/nearhost",
			onPath: "/home/me/.local/share/pnpm/nearhost", files: map[string]string{"/home/me/.local/share/pnpm/nearhost": shellShim},
			regular: map[string]bool{pnpmStore + "/cli-linux-" + nodeArch + "/bin/nearhost": true}},
			"'" + pnpmStore + "/cli-linux-" + nodeArch + "/bin/nearhost'"},
		{"pnpm symlink to the launcher", &fakeUpdate{exe: "/home/me/.local/share/pnpm/global/5/.pnpm/@nearhost+cli-linux-x64@0.5.0/node_modules/@nearhost/cli-linux-x64/bin/nearhost",
			onPath: "/home/me/.local/share/pnpm/nearhost", links: map[string]string{"/home/me/.local/share/pnpm/nearhost": pnpmStore + "/cli/bin/nearhost.js"},
			regular: map[string]bool{pnpmStore + "/cli-linux-" + nodeArch + "/bin/nearhost": true}},
			"'" + pnpmStore + "/cli-linux-" + nodeArch + "/bin/nearhost'"},
		{"a directory with a space", &fakeUpdate{exe: "/home/me/my bin/it's/nearhost", env: map[string]string{"GOBIN": "/home/me/my bin/it's"}}, `'/home/me/my bin/it'\''s/nearhost'`},
	} {
		tc.f.latest, tc.f.goos = "0.6.0", "linux"
		tc.f.writable = true
		out, err := runUpdate(t, tc.f, "0.5.0", "", "--yes")
		if err != nil || strings.Count(out, "setcap") != 1 || !strings.HasSuffix(out, "Updated to 0.6.0.\n"+setcap+tc.want+"\n") {
			t.Errorf("%s: out %q, err %v; want it to end %q", tc.name, out, err, setcap+tc.want)
		}
	}
}

// A pnpm shim that cannot be followed to a binary is printed as the path
// the user runs, with a warning that it may be a launcher script.
func TestUpdateSetcapSaysWhenItCannotFindTheBinary(t *testing.T) {
	f := &fakeUpdate{latest: "0.6.0", goos: "linux", writable: true,
		exe:    "/home/me/.local/share/pnpm/global/5/.pnpm/@nearhost+cli-linux-x64@0.5.0/node_modules/@nearhost/cli-linux-x64/bin/nearhost",
		onPath: "/home/me/.local/share/pnpm/nearhost", files: map[string]string{"/home/me/.local/share/pnpm/nearhost": "#!/bin/sh\necho odd\n"}}
	out, err := runUpdate(t, f, "0.5.0", "", "--yes")
	if err != nil || !strings.Contains(out, "could not tell which file is the binary") ||
		!strings.Contains(out, "sudo setcap cap_net_bind_service=+ep '/home/me/.local/share/pnpm/nearhost'") {
		t.Fatalf("out %q, err %v", out, err)
	}
}

func TestUpdateSetcapAdviceDependsOnTheCapability(t *testing.T) {
	const exe = "/usr/lib/node_modules/@nearhost/cli-linux-x64/bin/nearhost"
	// The new binary already has it: nothing to say.
	f := &fakeUpdate{latest: "0.6.0", goos: "linux", exe: exe, writable: true, capped: map[string]bool{exe: true}}
	out, err := runUpdate(t, f, "0.5.0", "", "--yes")
	if err != nil || strings.Contains(out, "setcap") || !slices.Equal(f.capProbed, []string{exe}) {
		t.Fatalf("out %q, err %v, probed %v", out, err, f.capProbed)
	}
	// The probe cannot tell: the command is printed without a check.
	f = &fakeUpdate{latest: "0.6.0", goos: "linux", exe: exe, writable: true, capped: map[string]bool{exe: true}, capUnknown: true}
	out, _ = runUpdate(t, f, "0.5.0", "", "--yes")
	if !strings.Contains(out, "sudo setcap cap_net_bind_service=+ep '"+exe+"'") {
		t.Fatalf("out %q", out)
	}
	// Other systems have no file capabilities.
	for _, goos := range []string{"darwin", "windows"} {
		f = &fakeUpdate{latest: "0.6.0", goos: goos, exe: exe, writable: true}
		out, _ = runUpdate(t, f, "0.5.0", "", "--yes")
		if strings.Contains(out, "setcap") {
			t.Fatalf("%s: %q", goos, out)
		}
	}
	// An update that did not install says nothing about ports.
	f = &fakeUpdate{latest: "0.6.0", goos: "linux", exe: exe, writable: true, installed: "0.5.0"}
	out, err = runUpdate(t, f, "0.5.0", "", "--yes")
	if err == nil || strings.Contains(out, "setcap") || len(f.capProbed) != 0 {
		t.Fatalf("out %q, err %v", out, err)
	}
}

func TestUpdateSetcapAdviceSkipsASystemThatLetsAnyoneBindLowPorts(t *testing.T) {
	const exe = "/usr/lib/node_modules/@nearhost/cli-linux-x64/bin/nearhost"
	for _, tc := range []struct {
		name       string
		start      int
		unreadable bool
		advice     bool
	}{
		{"1024", 1024, false, true}, {"81", 81, false, true}, {"80", 80, false, false}, {"unreadable", 0, true, true},
	} {
		f := &fakeUpdate{latest: "0.6.0", goos: "linux", exe: exe, writable: true, lowStart: tc.start, lowUnreadable: tc.unreadable}
		out, err := runUpdate(t, f, "0.5.0", "", "--yes")
		if err != nil || strings.Contains(out, "setcap") != tc.advice {
			t.Errorf("%s: out %q, err %v", tc.name, out, err)
		}
	}
}

// A path that is not a regular file, such as a directory or a missing
// file, is never printed as the binary.
func TestUpdateSetcapOnlyNamesRegularFiles(t *testing.T) {
	const exe = "/usr/local/bin/nearhost"
	f := &fakeUpdate{latest: "0.6.0", goos: "linux", exe: exe, writable: true, regular: map[string]bool{}}
	out, err := runUpdate(t, f, "0.5.0", "", "--yes")
	if err != nil || !strings.Contains(out, "could not tell which file is the binary") || len(f.capProbed) != 0 {
		t.Fatalf("out %q, err %v", out, err)
	}
	f = &fakeUpdate{latest: "0.6.0", goos: "linux", exe: "/home/linuxbrew/.linuxbrew/Caskroom/nearhost/0.5.0/nearhost", writable: true, regular: map[string]bool{}}
	out, _ = runUpdate(t, f, "0.5.0", "", "--yes")
	if !strings.Contains(out, "could not tell which file is the binary") || !strings.Contains(out, "'/home/linuxbrew/.linuxbrew/bin/nearhost'") {
		t.Fatalf("out %q", out)
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/usr/local/bin": "/usr/local/bin", "/opt/my bin": "'/opt/my bin'",
		"/opt/it's": `'/opt/it'\''s'`, "": "''", "/a$b": "'/a$b'",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUpdateUnwritableScriptAdviceQuotesTheDirectory(t *testing.T) {
	f := &fakeUpdate{latest: "0.6.0", exe: "/opt/it's mine/nearhost", tty: true, goos: "linux"}
	out, err := runUpdate(t, f, "0.5.0", "", "--yes")
	if err != nil || !strings.Contains(out, `| sudo NEARHOST_INSTALL_DIR='/opt/it'\''s mine' sh`) {
		t.Fatalf("out %q, err %v", out, err)
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
		io.WriteString(w, `{"tag_name":"v0.6.0","assets":[{"name":"nearhost_0.6.0_`+runtime.GOOS+"_"+runtime.GOARCH+`.tar.gz"},{"name":"nearhost_0.6.0_`+runtime.GOOS+"_"+runtime.GOARCH+`.zip"}]}`)
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "update.json")
	checker := update.Checker{URL: srv.URL, CachePath: path, HTTP: backgroundHTTP(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
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
	checker := update.Checker{URL: srv.URL, CachePath: path, HTTP: backgroundHTTP(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
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

// A Windows pre-release build is never offered a normal release that has no
// Windows build, by `nearhost update` or by the background notice.
func TestWindowsPrereleaseBuildIsNotOfferedAReleaseWithoutAWindowsBuild(t *testing.T) {
	const list = `[
 {"tag_name":"v0.13.0","draft":false,"prerelease":false,"assets":[{"name":"nearhost_0.13.0_linux_amd64.tar.gz"}]},
 {"tag_name":"v0.13.0-rc.2","draft":false,"prerelease":true,"assets":[{"name":"nearhost_0.13.0-rc.2_windows_amd64.zip"}]}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, list) }))
	defer srv.Close()
	checker := update.Checker{Current: "0.13.0-rc.1", URL: srv.URL, GOOS: "windows", GOARCH: "amd64"}

	// latest is what the new copy reports once it is in place.
	f := &fakeUpdate{latest: "0.13.0-rc.2", exe: `C:\Tools\nearhost.exe`, goos: "windows", writable: true}
	b := f.backend()
	b.latest = checker.Latest
	orig := Version
	t.Cleanup(func() { Version = orig })
	Version = "0.13.0-rc.1"
	cmd := newUpdateCmd(b)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--yes"})
	if err := cmd.Execute(); err != nil || !slices.Equal(f.downloaded, []string{`0.13.0-rc.2 C:\Tools\nearhost.exe.new`}) {
		t.Fatalf("out %q, err %v, downloaded %v; want the update to 0.13.0-rc.2", out.String(), err, f.downloaded)
	}
	if strings.Contains(out.String(), "0.13.0 ") || strings.Contains(out.String(), "0.13.0.") {
		t.Fatalf("offered 0.13.0, which has no Windows build:\n%s", out.String())
	}

	c := startBackgroundCheck("0.13.0-rc.1", checker.Latest)
	if got, want := c.notice(time.Second), "A new nearhost is available: 0.13.0-rc.1 → 0.13.0-rc.2. Run `nearhost update` to update."; got != want {
		t.Fatalf("notice = %q, want %q", got, want)
	}
}

// Before it downloads, nearhost says what it will fetch and which file it
// will replace.
func TestUpdateOnWindowsSaysWhatItWillReplace(t *testing.T) {
	exe := `C:\Users\José Núñez\bin\nearhost.exe`
	f := &fakeUpdate{latest: "0.6.0", exe: exe, goos: "windows", writable: true}
	out, err := runUpdate(t, f, "0.5.0", "")
	want := "Run `nearhost update --yes` to have nearhost download 0.6.0 from GitHub and replace " + exe + ".\n"
	if err != nil || len(f.events) != 0 || !strings.HasSuffix(out, want) {
		t.Fatalf("out %q, err %v; want it to end %q", out, err, want)
	}
	f = &fakeUpdate{latest: "0.6.0", exe: exe, goos: "windows", writable: true, tty: true}
	out, _ = runUpdate(t, f, "0.5.0", "n\n")
	if !strings.Contains(out, "Download 0.6.0 from GitHub and replace "+exe+"? [Y/n] ") || len(f.events) != 0 {
		t.Fatalf("prompt %q, events %v", out, f.events)
	}
}

// Every update command runs with the environment as it is, plus what its
// method sets.
func TestUpdateEnv(t *testing.T) {
	environ := []string{"PATH=C:\\Windows", "PSModulePath=C:\\Program Files\\PowerShell\\7\\Modules", "HOME=x"}
	for _, args := range [][]string{{"npm", "install", "-g", "@nearhost/cli@latest"}, {"sh", "-c", update.InstallScript}} {
		got := updateEnv(environ, update.Method{Args: args, Env: []string{"NEARHOST_INSTALL_DIR=/usr/local/bin"}})
		if want := append(slices.Clone(environ), "NEARHOST_INSTALL_DIR=/usr/local/bin"); !slices.Equal(got, want) {
			t.Errorf("%s: env = %q, want %q", args[0], got, want)
		}
	}
}

// An installer that exits 0 without changing the binary is not an update:
// the command says the binary is still the old version, and fails.
func TestUpdateSaysWhenTheBinaryIsUnchanged(t *testing.T) {
	f := &fakeUpdate{latest: "0.6.0", exe: "/usr/local/bin/nearhost", goos: "linux", writable: true, installed: "0.5.0"}
	out, err := runUpdate(t, f, "0.5.0", "", "--yes")
	want := "The update ran, but " + filepath.FromSlash("/usr/local/bin/nearhost") + " is still 0.5.0. Run it again, or install by hand: curl -fsSL https://nearhost.dev/install.sh | sh.\n"
	if !strings.HasSuffix(out, want) || strings.Contains(out, "Updated") {
		t.Fatalf("out %q, want it to end %q", out, want)
	}
	if exitCode(err, io.Discard) != 1 {
		t.Fatalf("err %v, want exit status 1", err)
	}
	if !slices.Equal(f.probed, []string{"/usr/local/bin/nearhost"}) {
		t.Fatalf("probed %v", f.probed)
	}
}

// A matching version, whatever its spelling, is the update done.
func TestUpdateMatchingVersionSaysUpdated(t *testing.T) {
	// `go install …@latest` builds without the release's ldflags: its
	// version comes from the module version Go records, v0.6.0.
	goInstalled := buildVersion("dev", func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Path: "github.com/nearhost/nearhost", Version: "v0.6.0"}}, true
	})
	for _, installed := range []string{installedVersion("nearhost " + goInstalled + "\n"), "v0.6.0", "0.6.0+build.7"} {
		f := &fakeUpdate{latest: "0.6.0", exe: "/Users/me/go/bin/nearhost", goos: "darwin", installed: installed}
		out, err := runUpdate(t, f, "0.5.0", "", "--yes")
		if err != nil || !strings.HasSuffix(out, "Updated to 0.6.0.\n") {
			t.Fatalf("%s: out %q, err %v", installed, out, err)
		}
		if !slices.Equal(f.probed, []string{"/Users/me/go/bin/nearhost"}) {
			t.Fatalf("%s: probed %v", installed, f.probed)
		}
	}
}

// A new binary that does not start is its own problem, not an old version.
func TestUpdateSaysWhenTheNewBinaryDoesNotStart(t *testing.T) {
	f := &fakeUpdate{latest: "0.6.0", exe: "/usr/local/bin/nearhost", goos: "linux", writable: true, probeErr: errors.New("exec format error")}
	out, err := runUpdate(t, f, "0.5.0", "", "--yes")
	want := "The update ran, but " + filepath.FromSlash("/usr/local/bin/nearhost") + " did not start: exec format error. Run it again, or install by hand: curl -fsSL https://nearhost.dev/install.sh | sh.\n"
	if !strings.HasSuffix(out, want) || strings.Contains(out, "still") || strings.Contains(out, "Updated") || exitCode(err, io.Discard) != 1 {
		t.Fatalf("out %q, err %v; want it to end %q", out, err, want)
	}
}

// Homebrew leaves the old version's Caskroom folder behind and links the new
// one into its bin folder, which is the nearhost the next run starts.
func TestUpdateHomebrewChecksTheLinkedBinary(t *testing.T) {
	for _, tc := range []struct{ exe, linked string }{
		{"/opt/homebrew/Caskroom/nearhost/0.5.0/nearhost", "/opt/homebrew/bin/nearhost"},
		{"/home/linuxbrew/.linuxbrew/Caskroom/nearhost/0.5.0/nearhost", "/home/linuxbrew/.linuxbrew/bin/nearhost"},
	} {
		f := &fakeUpdate{latest: "0.6.0", exe: tc.exe, goos: "darwin", onPath: "/somewhere/else/nearhost"}
		out, err := runUpdate(t, f, "0.5.0", "", "--yes")
		if err != nil || !strings.Contains(out, "Updated to 0.6.0.") || !slices.Equal(f.probed, []string{tc.linked}) {
			t.Fatalf("out %q, err %v, probed %v; want %s", out, err, f.probed, tc.linked)
		}
	}
	f := &fakeUpdate{latest: "0.6.0", exe: "/opt/homebrew/Caskroom/nearhost/0.5.0/nearhost", goos: "darwin", installed: "0.5.0"}
	out, _ := runUpdate(t, f, "0.5.0", "", "--yes")
	if !strings.Contains(out, "The update ran, but "+filepath.FromSlash("/opt/homebrew/bin/nearhost")+" is still 0.5.0. Run it again, or install by hand: brew upgrade --cask nearhost/tap/nearhost.") {
		t.Fatalf("out %q", out)
	}
}

// pnpm's store has the version in the path, so the old binary's path stays
// old; the shim on PATH is what the next run starts. With no shim on PATH
// there is nothing sound to check, and the exit status counts, as before.
func TestUpdatePnpmChecksTheShimOnPath(t *testing.T) {
	exe := "/Users/me/Library/pnpm/global/5/.pnpm/@nearhost+cli-darwin-arm64@0.5.0/node_modules/@nearhost/cli-darwin-arm64/bin/nearhost"
	f := &fakeUpdate{latest: "0.6.0", exe: exe, goos: "darwin", onPath: "/Users/me/Library/pnpm/nearhost"}
	if out, err := runUpdate(t, f, "0.5.0", "", "--yes"); err != nil || !strings.Contains(out, "Updated to 0.6.0.") ||
		!slices.Equal(f.probed, []string{"/Users/me/Library/pnpm/nearhost"}) {
		t.Fatalf("out %q, err %v, probed %v", out, err, f.probed)
	}
	f = &fakeUpdate{latest: "0.6.0", exe: exe, goos: "darwin"}
	if out, err := runUpdate(t, f, "0.5.0", "", "--yes"); err != nil || !strings.Contains(out, "Updated to 0.6.0.") || len(f.probed) != 0 {
		t.Fatalf("out %q, err %v, probed %v", out, err, f.probed)
	}
	// Another nearhost earlier on PATH than pnpm's shim is named as such.
	f = &fakeUpdate{latest: "0.6.0", exe: exe, goos: "darwin", onPath: "/usr/local/bin/nearhost", installed: "0.4.0",
		env: map[string]string{"PNPM_HOME": "/Users/me/Library/pnpm"}}
	out, _ := runUpdate(t, f, "0.5.0", "", "--yes")
	if !strings.Contains(out, "The update ran, but "+filepath.FromSlash("/usr/local/bin/nearhost")+" is still 0.4.0. Run it again, or install by hand: pnpm add -g @nearhost/cli@latest. Another nearhost comes before pnpm's on PATH.") {
		t.Fatalf("out %q", out)
	}
	f = &fakeUpdate{latest: "0.6.0", exe: exe, goos: "darwin", onPath: "/Users/me/Library/pnpm/nearhost", installed: "0.5.0",
		env: map[string]string{"PNPM_HOME": "/Users/me/Library/pnpm"}}
	if out, _ := runUpdate(t, f, "0.5.0", "", "--yes"); strings.Contains(out, "Another nearhost") {
		t.Fatalf("pnpm's own shim was called another nearhost: %q", out)
	}
	// Newer pnpm keeps its shims in PNPM_HOME/bin.
	f = &fakeUpdate{latest: "0.6.0", exe: exe, goos: "darwin", onPath: "/Users/me/Library/pnpm/bin/nearhost", installed: "0.5.0",
		env: map[string]string{"PNPM_HOME": "/Users/me/Library/pnpm/"}}
	if out, _ := runUpdate(t, f, "0.5.0", "", "--yes"); strings.Contains(out, "Another nearhost") {
		t.Fatalf("pnpm's shim in PNPM_HOME/bin was called another nearhost: %q", out)
	}
}

// The version is the release's ldflags value; without one, a `go install`
// build's module version; anything else stays dev.
func TestBuildVersion(t *testing.T) {
	info := func(v string) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Main: debug.Module{Path: "github.com/nearhost/nearhost", Version: v}}, true
		}
	}
	for _, tc := range []struct {
		version string
		read    func() (*debug.BuildInfo, bool)
		want    string
	}{
		{"0.13.0", info("v0.6.0"), "0.13.0"},
		{"dev", info("v0.6.0"), "0.6.0"},
		{"dev", info("v0.13.0-rc.2"), "0.13.0-rc.2"},
		{"dev", info("(devel)"), "dev"},
		{"dev", info(""), "dev"},
		{"dev", info("v0.13.1-0.20261118120000-abcdef123456"), "dev"},
		{"dev", info("v0.13.1-rc.1.0.20261118120000-abcdef123456"), "dev"},
		{"dev", info("v0.0.0-20261118120000-abcdef123456"), "dev"},
		{"dev", info("v0.13.0+dirty"), "dev"},
		{"dev", func() (*debug.BuildInfo, bool) { return nil, false }, "dev"},
	} {
		if got := buildVersion(tc.version, tc.read); got != tc.want {
			v, _ := tc.read()
			t.Errorf("buildVersion(%q, %v) = %q, want %q", tc.version, v, got, tc.want)
		}
	}
}

// The script installs exactly the version nearhost resolved and compares
// with, whatever NEARHOST_VERSION the user's shell exports; the command
// shown stays the plain one-liner.
func TestUpdateScriptPinsTheResolvedVersion(t *testing.T) {
	f := &fakeUpdate{latest: "0.6.0", exe: "/usr/local/bin/nearhost", goos: "linux", writable: true, tty: true}
	out, err := runUpdate(t, f, "0.5.0", "\n")
	if err != nil || len(f.ran) != 1 || !slices.Contains(f.ran[0].Env, "NEARHOST_VERSION=v0.6.0") {
		t.Fatalf("out %q, err %v, ran %v", out, err, f.ran)
	}
	if !strings.Contains(out, "Run `curl -fsSL https://nearhost.dev/install.sh | sh`? [Y/n]") {
		t.Fatalf("out %q", out)
	}
}

// A stale installer variable in the user's shell cannot redirect, dry-run
// or pin the script update; other commands keep their environment.
func TestUpdateEnvDropsStaleInstallerVariables(t *testing.T) {
	environ := []string{"PATH=/bin", "NEARHOST_VERSION=v0.1.0", "NEARHOST_BASE_URL=http://example.invalid", "NEARHOST_INSTALL_DRY_RUN=1", "NEARHOST_INSTALL_DIR=/elsewhere", "NEARHOST_SKIP_SETCAP=1"}
	script := update.Method{Kind: update.Script, Args: []string{"sh", "-c", "x"}, Env: []string{"NEARHOST_INSTALL_DIR=/usr/local/bin", "NEARHOST_VERSION=v0.6.0"}}
	want := []string{"PATH=/bin", "NEARHOST_SKIP_SETCAP=1", "NEARHOST_INSTALL_DIR=/usr/local/bin", "NEARHOST_VERSION=v0.6.0"}
	if got := updateEnv(environ, script); !slices.Equal(got, want) {
		t.Fatalf("env = %q, want %q", got, want)
	}
	npm := update.Method{Kind: update.NPM, Args: []string{"npm", "install", "-g", "@nearhost/cli@latest"}}
	if got := updateEnv(environ, npm); !slices.Equal(got, environ) {
		t.Fatalf("npm env = %q, want %q", got, environ)
	}
}

// A Windows copy nearhost replaced itself is checked at its own path, and
// the by-hand advice is the PowerShell installer.
func TestUpdateOnWindowsChecksTheReplacedExe(t *testing.T) {
	exe := `C:\Tools\nearhost.exe`
	f := &fakeUpdate{latest: "0.6.0", exe: exe, goos: "windows", writable: true, installed: "0.5.0"}
	out, err := runUpdate(t, f, "0.5.0", "", "--yes")
	want := "The update ran, but " + exe + " is still 0.5.0. Run it again, or install by hand: irm https://nearhost.dev/install.ps1 | iex.\n"
	if !strings.HasSuffix(out, want) || exitCode(err, io.Discard) != 1 || !slices.Equal(f.probed, []string{exe}) {
		t.Fatalf("out %q, err %v, probed %v; want it to end %q", out, err, f.probed, want)
	}
}

// What the probe printed is shown without its control characters.
func TestUpdateShowsTheProbedVersionSafely(t *testing.T) {
	f := &fakeUpdate{latest: "0.6.0", exe: "/usr/local/bin/nearhost", goos: "linux", writable: true, installed: "0.5.0\x1b[2J"}
	out, _ := runUpdate(t, f, "0.5.0", "", "--yes")
	if strings.Contains(out, "\x1b") {
		t.Fatalf("out %q holds an escape", out)
	}
}

// The installer gets no terminal on stdin, as it had none when piped into
// sh: it prints the setcap advice instead of asking. Other commands keep
// the terminal.
func TestUpdateCommandStdin(t *testing.T) {
	script := update.DetectOn("linux", "/usr/local/bin/nearhost", func(string) string { return "" })
	c := updateCommand(context.Background(), script)
	if c.Stdin != nil {
		t.Fatalf("the installer's stdin is %v, want none", c.Stdin)
	}
	if !slices.Contains(c.Env, "NEARHOST_INSTALL_DIR=/usr/local/bin") || !slices.Equal(c.Args, script.Args) {
		t.Fatalf("args %q, env lacks the install directory", c.Args)
	}
	brew := update.DetectOn("darwin", "/opt/homebrew/Caskroom/nearhost/0.5.0/nearhost", func(string) string { return "" })
	if c := updateCommand(context.Background(), brew); c.Stdin != os.Stdin {
		t.Fatalf("brew's stdin is %v, want the terminal", c.Stdin)
	}
}

func TestInstalledVersion(t *testing.T) {
	for _, tc := range []struct{ out, want string }{
		{"nearhost 0.6.0\n", "0.6.0"},
		{"nearhost v0.6.0\r\n", "v0.6.0"},
		{"nearhost dev\n", "dev"},
		{"something else\n", "something else"},
	} {
		if got := installedVersion(tc.out); got != tc.want {
			t.Errorf("installedVersion(%q) = %q, want %q", tc.out, got, tc.want)
		}
	}
}
