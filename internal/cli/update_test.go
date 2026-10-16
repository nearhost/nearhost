package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

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
}

func (f *fakeUpdate) backend() updateBackend {
	return updateBackend{
		latest:      func(context.Context) (string, error) { return f.latest, f.err },
		exe:         func() string { return f.exe },
		getenv:      func(k string) string { return map[string]string{"HOME": "/Users/me"}[k] },
		writable:    func(string) bool { return f.writable },
		interactive: func() bool { return f.tty },
		goos:        f.goos,
		run: func(_ context.Context, m update.Method, _, _ io.Writer) error {
			f.ran = append(f.ran, m)
			return f.runErr
		},
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
	f := &fakeUpdate{latest: "0.6.0", exe: "/usr/local/bin/nearhost", tty: true}
	out, err := runUpdate(t, f, "0.5.0", "", "--yes")
	if err != nil || len(f.ran) != 0 || !strings.Contains(out, "sudo NEARHOST_INSTALL_DIR=/usr/local/bin sh") {
		t.Fatalf("out %q, err %v, ran %v", out, err, f.ran)
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
		name    string
		args    []string
		getenv  func(string) string
		version string
		euid    int
		tty     bool
		want    bool
	}{
		{"ui", nil, ok, "0.5.0", 501, true, true},
		{"up", []string{"up"}, ok, "0.5.0", 501, true, true},
		{"update itself", []string{"update"}, ok, "0.5.0", 501, true, false},
		{"version", []string{"version"}, ok, "0.5.0", 501, true, false},
		{"help", []string{"--help"}, ok, "0.5.0", 501, true, false},
		{"completion", []string{"completion", "zsh"}, ok, "0.5.0", 501, true, false},
		{"hosts helper under sudo", []string{"hosts", "apply"}, ok, "0.5.0", 0, true, false},
		{"not a terminal", []string{"ls"}, ok, "0.5.0", 501, false, false},
		{"dev build", []string{"ls"}, ok, "dev", 501, true, false},
		{"opted out", []string{"ls"}, func(k string) string {
			if k == "NEARHOST_NO_UPDATE_CHECK" {
				return "1"
			}
			return ""
		}, "0.5.0", 501, true, false},
	} {
		if got := shouldCheckForUpdate(tc.args, tc.getenv, tc.version, tc.euid, tc.tty); got != tc.want {
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
