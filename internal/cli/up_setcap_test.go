package cli

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
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
	perm := fmtPortErr("HTTPS port 443 needs permission to bind", permissionError())
	_, err := execute(t, newUpCmd(setcapBackend(u, perm, exe)), u.args()...)
	if err == nil {
		t.Fatal("up succeeded")
	}
	want := perm.Error() + "\n\nsudo setcap cap_net_bind_service=+ep '" + exe + "'\nDo not run nearhost with sudo."
	if err.Error() != want {
		t.Errorf("error %q, want %q", err, want)
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Error("the advice hides the cause")
	}
	var ee exitError
	if errors.As(err, &ee) {
		t.Error("the error is an exitError, so Main would print nothing")
	}
}

func TestUpSaysWhenItCannotTellWhichFileIsTheBinary(t *testing.T) {
	u := newUpRun(t, &config.Config{Projects: []config.Project{localProject("web", testutil.Upstream(t, "web"), true)}})
	perm := fmtPortErr("HTTP port 80 needs permission to bind", permissionError())
	_, err := execute(t, newUpCmd(setcapBackend(u, perm, "")), u.args()...)
	if err == nil || !strings.Contains(err.Error(), "could not tell which file is the binary") ||
		!strings.Contains(err.Error(), `sudo setcap cap_net_bind_service=+ep "$(command -v nearhost)"`) {
		t.Errorf("error %v", err)
	}
}

func TestUpAddsNoSetcapAdviceWhenItWouldNotHelp(t *testing.T) {
	inUse := fmtPortErr("HTTPS port 443 is in use", errors.New("address already in use"))
	perm := fmtPortErr("HTTPS port 443 needs permission to bind", permissionError())
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
	b := setcapBackend(u, fmtPortErr("HTTPS port 443 needs permission to bind", permissionError()), "/usr/local/bin/nearhost")
	b.bind.unprivilegedPortStart = func() (int, bool) { return 0, false }
	_, err := execute(t, newUpCmd(b), u.args()...)
	if err == nil || !strings.Contains(err.Error(), "sudo setcap") {
		t.Errorf("error %v", err)
	}
}

// fmtPortErr is an error that reads like the engine's portError: the message,
// then the error from binding.
func fmtPortErr(msg string, err error) error {
	return &wrapped{msg: msg + ": " + err.Error(), err: err}
}

type wrapped struct {
	msg string
	err error
}

func (w *wrapped) Error() string { return w.msg }
func (w *wrapped) Unwrap() error { return w.err }
