package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/testutil"
)

// permissionError is what Start returns for a port the user may not bind.
func permissionError() error {
	return &os.SyscallError{Syscall: "listen", Err: syscall.EACCES}
}

// startFailing makes up's engine start fail with err.
func startFailing(err error) func(*engine.Engine, context.Context) error {
	return func(*engine.Engine, context.Context) error { return err }
}

// setcapBackend is a Linux user without privileges whose binary is a regular
// file at bin.
func setcapBackend(u *upRun, startErr error, bin string) upBackend {
	b := u.backend()
	b.start = startFailing(startErr)
	b.bind = bindEnv{
		goos:                  "linux",
		notPermitted:          func(err error) bool { var w *wrapped; return errors.As(err, &w) && w.denied },
		elevated:              func() bool { return false },
		exe:                   func() string { return bin },
		isFile:                func(string) bool { return bin != "" },
		unprivilegedPortStart: func() (int, bool) { return 1024, true },
	}
	return b
}

func TestUpPrintsTheSetcapCommandForAPortItMayNotBind(t *testing.T) {
	const exe = "/home/me/.local/bin/nearhost"
	u := newUpRun(t, &config.Config{Projects: []config.Project{localProject("web", testutil.Upstream(t, "web"), true)}})
	perm := deniedPortErr("HTTPS port 443 needs permission to bind", permissionError())
	_, err := execute(t, newUpCmd(setcapBackend(u, perm, exe)), u.args()...)
	if err == nil {
		t.Fatal("up succeeded")
	}
	// The spec's example, for up: what happened, why in plain words, the
	// command on a line of its own, then what to do after it.
	const want = "Error: HTTPS port 443 needs permission to bind.\n" +
		"\n" +
		"On Linux, a program may use ports below 1024, such as 443 for HTTPS and\n" +
		"80 for HTTP, only when it has been given permission. The install script\n" +
		"gives it; a nearhost you build or copy yourself does not have it.\n" +
		"\n" +
		"To give it permission, run:\n" +
		"\n" +
		"  sudo setcap cap_net_bind_service=+ep '" + exe + "'\n" +
		"\n" +
		"Then start Nearhost again. Do not run nearhost with sudo.\n" +
		"\n" +
		"Run `nearhost doctor` to check this and the rest of what Nearhost needs.\n"
	if got := rendered(err); got != want {
		t.Errorf("rendered:\n%s\nwant:\n%s", got, want)
	}
	if m, ok := report.As(err); !ok || m.Code != report.PortNeedsPermission {
		t.Errorf("message %+v, want %s", m, report.PortNeedsPermission)
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Error("the advice hides the cause")
	}
	if code := exitCode(err, io.Discard); code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
}

// The engine's own port error keeps its port in the message's data, and
// its cause, which --verbose shows, out of the title.
func TestUpSetcapAdviceKeepsTheEnginesPort(t *testing.T) {
	m := bindEnv{
		goos: "linux", notPermitted: func(error) bool { return true }, elevated: func() bool { return false },
		exe: func() string { return "/usr/local/bin/nearhost" }, isFile: func(string) bool { return true },
	}.advise(fmt.Errorf("start: %w", report.Errorf(report.PortNeedsPermission, "HTTP port 80 needs permission to bind: listen tcp :80: bind: permission denied").
		With("port", 80).Wrap(errors.New("listen tcp :80: bind: permission denied"))))
	got, ok := report.As(m)
	if !ok || got.Title != "HTTP port 80 needs permission to bind." || got.Data["port"] != 80 || len(got.Fix) != 2 || !got.Doctor {
		t.Errorf("message %+v", got)
	}
}

func TestUpSaysWhenItCannotTellWhichFileIsTheBinary(t *testing.T) {
	u := newUpRun(t, &config.Config{Projects: []config.Project{localProject("web", testutil.Upstream(t, "web"), true)}})
	perm := deniedPortErr("HTTP port 80 needs permission to bind", permissionError())
	_, err := execute(t, newUpCmd(setcapBackend(u, perm, "")), u.args()...)
	if got := rendered(err); err == nil || !strings.Contains(got, "could not tell which file is the binary") ||
		!strings.Contains(got, "\n  sudo setcap cap_net_bind_service=+ep \"$(command -v nearhost)\"\n") {
		t.Errorf("error %v", err)
	}
}

func TestUpAddsNoSetcapAdviceWhenItWouldNotHelp(t *testing.T) {
	inUse := fmtPortErr("HTTPS port 443 is in use", errors.New("address already in use"))
	perm := deniedPortErr("HTTPS port 443 needs permission to bind", permissionError())
	dir := &fs.PathError{Op: "mkdir", Path: "/run/nearhost", Err: fs.ErrPermission}
	for _, tc := range []struct {
		name  string
		err   error
		tweak func(*upBackend)
	}{
		{"port in use", inUse, nil},
		{"another error", errors.New("boom"), nil},
		{"a file it could not create", fmtPortErr("create run directory", dir), nil},
		{"darwin", perm, func(b *upBackend) { b.bind.goos = "darwin" }},
		{"windows", perm, func(b *upBackend) { b.bind.goos = "windows" }},
		{"unprivileged ports start at 80", perm, func(b *upBackend) {
			b.bind.unprivilegedPortStart = func() (int, bool) { return 80, true }
		}},
		{"unprivileged ports start at 0", perm, func(b *upBackend) {
			b.bind.unprivilegedPortStart = func() (int, bool) { return 0, true }
		}},
		{"root", perm, func(b *upBackend) { b.bind.elevated = func() bool { return true } }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := newUpRun(t, &config.Config{Projects: []config.Project{localProject("web", testutil.Upstream(t, "web"), true)}})
			b := setcapBackend(u, tc.err, "/usr/local/bin/nearhost")
			if tc.tweak != nil {
				tc.tweak(&b)
			}
			_, err := execute(t, newUpCmd(b), u.args()...)
			if err == nil || err.Error() != tc.err.Error() {
				t.Errorf("error %v, want %q alone", err, tc.err)
			}
		})
	}
}

func TestUpAdvisesOnAnUnreadableUnprivilegedPortStart(t *testing.T) {
	u := newUpRun(t, &config.Config{Projects: []config.Project{localProject("web", testutil.Upstream(t, "web"), true)}})
	b := setcapBackend(u, deniedPortErr("HTTPS port 443 needs permission to bind", permissionError()), "/usr/local/bin/nearhost")
	b.bind.unprivilegedPortStart = func() (int, bool) { return 0, false }
	_, err := execute(t, newUpCmd(b), u.args()...)
	if err == nil || !strings.Contains(rendered(err), "sudo setcap") {
		t.Errorf("error %v", err)
	}
}

// fmtPortErr is an error that reads like the engine's portError: the message,
// then the error from binding.
func fmtPortErr(msg string, err error) error {
	return &wrapped{msg: msg + ": " + err.Error(), err: err}
}

// deniedPortErr is fmtPortErr for a port the user may not bind, which the
// test's notPermitted recognises where up uses engine.BindNotPermitted.
func deniedPortErr(msg string, err error) error {
	return &wrapped{msg: msg + ": " + err.Error(), err: err, denied: true}
}

type wrapped struct {
	msg    string
	denied bool
	err    error
}

func (w *wrapped) Error() string { return w.msg }
func (w *wrapped) Unwrap() error { return w.err }

// A message the engine wraps on its way out reaches `nearhost up`'s error
// with its code and fix steps, the wrapping said before its title.
func TestUpRendersAWrappedEngineMessage(t *testing.T) {
	u := newUpRun(t, &config.Config{Projects: []config.Project{localProject("web", testutil.Upstream(t, "web"), true)}})
	inUse := report.Errorf(report.PortInUse, "HTTPS port 443 is in use by node (pid 42).").
		Then("Quit the program using the port, or pass --https-port and --http-port.").
		With("port", 443).Wrap(errors.New("address already in use"))
	b := setcapBackend(u, fmt.Errorf("start proxy: %w", inUse), "/usr/local/bin/nearhost")
	_, err := execute(t, newUpCmd(b), u.args()...)
	if m, ok := report.As(err); !ok || m.Code != report.PortInUse || m.Data["port"] != 443 {
		t.Fatalf("err %v, message %+v", err, m)
	}
	var stderr strings.Builder
	if code := exitCodeVerbose(err, &stderr, true); code != 1 {
		t.Errorf("exit %d", code)
	}
	const want = "Error: start proxy: HTTPS port 443 is in use by node (pid 42).\n(port.in-use)\n\n" +
		"Quit the program using the port, or pass --https-port and --http-port.\n\n" +
		"Cause: address already in use\n"
	if got := stderr.String(); got != want {
		t.Errorf("rendered:\n%s\nwant:\n%s", got, want)
	}
}
