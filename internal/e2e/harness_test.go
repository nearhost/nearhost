// Package e2e runs the built nearhost binary in a pseudo-terminal and reads
// what it draws through a terminal emulator, as someone at a terminal sees
// it. The interface's own tests (internal/tui) drive the model with a fake
// backend and carry the detail; the journeys here check that the pieces work
// together: the binary, a real terminal, the engine, the proxy and ssh.
//
// A journey:
//
//   - makes a sandbox, a home folder of its own with a config file, test
//     ports, no hosts file and the fake ssh (newSandbox);
//   - starts nearhost in a terminal (sandbox.start) and answers the first
//     run's dialog (skipWelcome);
//   - presses keys and waits for what the screen should then show.
//
// Rules that keep a journey from failing by chance:
//
//   - never sleep: wait for the screen (waitFor, waitGone, waitUntil), for
//     the program's exit (waitExit), or for a condition outside the terminal
//     (testutil.WaitUntil);
//   - wait for text that only the state wanted shows, such as "● shop" for a
//     running project, since the screen is read while it is being drawn;
//   - press a key only once the screen shows the thing the key acts on;
//   - share nothing between journeys: each has its own sandbox and ports.
//   - keep a journey on this machine: never click an address, which opens
//     a browser, never answer yes to the presets question, which fetches a
//     registry, and never ask to trust the CA or manage the hosts file,
//     which run the real sudo.
//
// A failed wait prints the screen as it was.
package e2e

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
	"github.com/charmbracelet/x/xpty"
)

// Screen size of every terminal the tests open.
const (
	cols = 110
	rows = 34
)

// nearhostBin is the nearhost binary, built once for the package.
var nearhostBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "nh-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	nearhostBin = filepath.Join(dir, "nearhost")
	if runtime.GOOS == "windows" {
		nearhostBin += ".exe"
	}
	build := exec.Command("go", "build", "-o", nearhostBin, "github.com/nearhost/nearhost/cmd/nearhost")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build nearhost: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// terminal is a program running in a pseudo-terminal, and the screen a
// terminal emulator makes of what it writes.
type terminal struct {
	t    *testing.T
	pty  xpty.Pty
	emu  *vt.SafeEmulator
	cmd  *exec.Cmd
	exit chan error // the program's exit, once

	// mu guards the emulator's screen, whose cells screen reads in place,
	// and raw.
	mu  sync.Mutex
	raw strings.Builder // every byte the program wrote, for a failure's report
}

// start runs the program at path with args and env in a new terminal, in
// the folder dir, or this one when dir is empty. The program is killed when
// the test ends, should it still run.
func start(t *testing.T, env []string, dir, path string, args ...string) *terminal {
	t.Helper()
	pty, err := xpty.NewPty(cols, rows)
	if err != nil {
		t.Fatalf("open a pseudo-terminal: %v", err)
	}
	cmd := exec.Command(path, args...)
	cmd.Env = env
	cmd.Dir = dir
	ownTerminal(cmd)
	if err := pty.Start(cmd); err != nil {
		pty.Close()
		t.Fatalf("start %s: %v", path, err)
	}
	term := &terminal{t: t, pty: pty, emu: vt.NewSafeEmulator(cols, rows), cmd: cmd, exit: make(chan error, 1)}
	// What the program writes goes to the emulator, which draws it.
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := pty.Read(buf)
			if n > 0 {
				term.mu.Lock()
				term.raw.Write(buf[:n])
				_, _ = term.emu.Write(buf[:n])
				term.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	// What the emulator sends goes to the program: the keys typed here, and
	// the emulator's answers to what the program asks its terminal.
	go func() { _, _ = io.Copy(pty, term.emu) }()
	go func() { term.exit <- xpty.WaitProcess(context.Background(), cmd) }()
	t.Cleanup(func() {
		if cmd.ProcessState == nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = pty.Close()
	})
	return term
}

// screen is the terminal's screen as text, one line a row, with trailing
// spaces cut.
func (term *terminal) screen() string {
	term.mu.Lock()
	defer term.mu.Unlock()
	var b strings.Builder
	for y := range term.emu.Height() {
		var line strings.Builder
		for x := 0; x < term.emu.Width(); {
			c := term.emu.CellAt(x, y)
			if c == nil || c.Content == "" {
				line.WriteByte(' ')
				x++
				continue
			}
			line.WriteString(c.Content)
			x += max(c.Width, 1)
		}
		b.WriteString(strings.TrimRight(line.String(), " "))
		b.WriteByte('\n')
	}
	return b.String()
}

// stepWait is how long a step may take to show on the screen.
const stepWait = 20 * time.Second

// waitFor waits until the screen holds every one of subs, and fails the test
// with the screen when it does not in time. It never sleeps a fixed time:
// a test moves on the moment the screen says it may.
func (term *terminal) waitFor(what string, subs ...string) {
	term.t.Helper()
	term.waitUntil(what, func(s string) bool {
		for _, sub := range subs {
			if !strings.Contains(s, sub) {
				return false
			}
		}
		return true
	})
}

// waitGone waits until the screen holds none of subs.
func (term *terminal) waitGone(what string, subs ...string) {
	term.t.Helper()
	term.waitUntil(what, func(s string) bool {
		for _, sub := range subs {
			if strings.Contains(s, sub) {
				return false
			}
		}
		return true
	})
}

func (term *terminal) waitUntil(what string, ok func(screen string) bool) {
	term.t.Helper()
	deadline := time.Now().Add(stepWait)
	for {
		s := term.screen()
		if ok(s) {
			return
		}
		select {
		case err := <-term.exit:
			term.exit <- err
			term.t.Fatalf("the program exited (%v) while waiting for %s; the screen:\n%s", err, what, s)
		default:
		}
		if time.Now().After(deadline) {
			term.t.Fatalf("no %s after %s; the screen:\n%s", what, stepWait, s)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// typeText types text, as keys.
func (term *terminal) typeText(text string) { term.emu.SendText(text) }

// Keys with no character of their own, for press.
const (
	keyEnter  = uv.KeyEnter
	keyEscape = uv.KeyEscape
	keyTab    = uv.KeyTab
	keyUp     = uv.KeyUp
	keyDown   = uv.KeyDown
)

// ctrl is the modifier of a key pressed with ctrl held, for press.
const ctrl = uv.ModCtrl

// press presses one key: a rune, or one of the uv.Key codes.
func (term *terminal) press(code rune, mod ...uv.KeyMod) {
	k := uv.KeyPressEvent{Code: code}
	if code >= ' ' && code < uv.KeyExtended && len(mod) == 0 {
		k.Text = string(code)
	}
	for _, m := range mod {
		k.Mod |= m
	}
	term.emu.SendKey(k)
}

// find is the column and row of the first place sub shows on the screen,
// and fails the test when it does not show.
func (term *terminal) find(sub string) (x, y int) {
	term.t.Helper()
	term.mu.Lock()
	for y := range term.emu.Height() {
		// The row's text, and the column each of its bytes is drawn in: a
		// wide character takes two columns and one place in the text.
		var line strings.Builder
		var cols []int
		for x := 0; x < term.emu.Width(); {
			content, width := " ", 1
			if c := term.emu.CellAt(x, y); c != nil && c.Content != "" {
				content, width = c.Content, max(c.Width, 1)
			}
			for range len(content) {
				cols = append(cols, x)
			}
			line.WriteString(content)
			x += width
		}
		if i := strings.Index(line.String(), sub); i >= 0 {
			term.mu.Unlock()
			return cols[i], y
		}
	}
	term.mu.Unlock()
	term.t.Fatalf("%q is not on the screen:\n%s", sub, term.screen())
	return 0, 0
}

// click presses and lets go of the left mouse button on the first place sub
// shows on the screen. Never click an address: the interface opens it in a
// browser.
func (term *terminal) click(sub string) {
	term.t.Helper()
	x, y := term.find(sub)
	at := uv.Mouse{X: x, Y: y, Button: uv.MouseLeft}
	term.emu.SendMouse(uv.MouseClickEvent(at))
	term.emu.SendMouse(uv.MouseReleaseEvent(at))
}

// wheel turns the mouse wheel by n notches over the first place sub shows on
// the screen: up for a negative n, down for a positive one.
func (term *terminal) wheel(sub string, n int) {
	term.t.Helper()
	x, y := term.find(sub)
	button := uv.MouseWheelDown
	if n < 0 {
		button, n = uv.MouseWheelUp, -n
	}
	for range n {
		term.emu.SendMouse(uv.MouseWheelEvent(uv.Mouse{X: x, Y: y, Button: button}))
	}
}

// resize gives the terminal a new size, which the program is told of.
func (term *terminal) resize(w, h int) {
	term.t.Helper()
	term.mu.Lock()
	term.emu.Resize(w, h)
	term.mu.Unlock()
	if err := term.pty.Resize(w, h); err != nil {
		term.t.Fatalf("resize: %v", err)
	}
}

// waitExit waits for the program to exit and returns its exit code.
func (term *terminal) waitExit() int {
	term.t.Helper()
	select {
	case err := <-term.exit:
		term.exit <- err
		var ee *exec.ExitError
		switch {
		case err == nil:
			return 0
		case errors.As(err, &ee):
			return ee.ExitCode()
		default:
			term.t.Fatalf("wait: %v", err)
		}
	case <-time.After(stepWait):
		term.t.Fatalf("the program did not exit in %s; the screen:\n%s", stepWait, term.screen())
	}
	return -1
}

// output is every byte the program wrote, escape sequences included.
func (term *terminal) output() string {
	term.mu.Lock()
	defer term.mu.Unlock()
	return term.raw.String()
}
