package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/hosts"
	"github.com/nearhost/nearhost/internal/testutil"
)

// logPath is the core log of the daemon the test runs.
func (u *upRun) logPath() string { return filepath.Join(filepath.Dir(u.cfgPath), "core.log") }

// coreLog is what the daemon has logged so far.
func (u *upRun) coreLog() string {
	b, _ := os.ReadFile(u.logPath())
	return string(b)
}

// startDaemon runs `nearhost daemon` with the test's ports until the test
// ends or stop is called.
func (u *upRun) startDaemon(extra ...string) {
	u.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	u.cancel = cancel
	// The goroutine sends to its own channel: a test that starts a second
	// daemon replaces u.done while the first may still be returning.
	done := make(chan error, 1)
	u.done = done
	b := u.backend()
	if u.adjust != nil {
		u.adjust(&b)
	}
	args := u.args(extra...)
	args[0] = "daemon"
	go func() { done <- executeInto(ctx, &u.out, newDaemonCmd(b), args...) }()
	u.t.Cleanup(func() {
		if u.done != nil {
			u.stop()
		}
	})
}

// waitForLog waits until the core log holds sub.
func (u *upRun) waitForLog(sub string) {
	u.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(u.coreLog(), sub) {
			return
		}
		select {
		case err := <-u.done:
			u.done <- err
			if !strings.Contains(u.coreLog(), sub) {
				u.t.Fatalf("daemon returned (%v) before logging %q; log:\n%s", err, sub, u.coreLog())
			}
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
	u.t.Fatalf("timed out waiting for %q in the log:\n%s", sub, u.coreLog())
}

// stamped is a log line's timestamp and the space after it.
var stamped = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(Z|[+-]\d\d:\d\d) `)

func TestDaemonLogsToTheCoreLog(t *testing.T) {
	u := newUpRun(t, &config.Config{Projects: []config.Project{localProject("a", testutil.Upstream(t, "a"), true)}})
	u.noHosts = true
	u.startDaemon()
	u.waitForLog("a: running at https://a.test\n")
	if err := u.stop(); err != nil {
		t.Fatalf("daemon returned %v; log:\n%s", err, u.coreLog())
	}
	if out := u.out.String(); out != "" {
		t.Errorf("daemon wrote to its output:\n%s", out)
	}
	log := u.coreLog()
	for _, line := range strings.Split(strings.TrimSuffix(log, "\n"), "\n") {
		// The blank lines that set the summary apart stay blank.
		if line != "" && !stamped.MatchString(line) {
			t.Errorf("line without a timestamp: %q", line)
		}
		// The timestamp is the only time on a line.
		if regexp.MustCompile(`^\S+ \d\d:\d\d:\d\d `).MatchString(line) {
			t.Errorf("line with a second time: %q", line)
		}
	}
	wantContains(t, log, "[a] state: running\n", "Serving until stopped with `nearhost stop`.\n", " stopped\n")
	if strings.Contains(log, "Ctrl-C") {
		t.Errorf("the log says to press Ctrl-C:\n%s", log)
	}
}

func TestDaemonNeverElevates(t *testing.T) {
	stale := filepath.Join(testutil.TempDir(t, "hosts"), "hosts")
	if err := os.WriteFile(stale, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := hosts.DefaultPath
	hosts.DefaultPath = stale
	t.Cleanup(func() { hosts.DefaultPath = before })

	u := newUpRun(t, &config.Config{Projects: []config.Project{localProject("one", testutil.Upstream(t, "one"), true)}})
	// The backend's hosts function is the one that elevates: sudo, or UAC
	// on Windows. The daemon must never reach it.
	u.adjust = func(b *upBackend) {
		b.ensureHosts = func(context.Context, []string) error {
			t.Error("the daemon called the hosts function that elevates")
			return errors.New("elevated")
		}
	}
	u.startDaemon()
	u.waitForLog("Nothing is being served.\n")
	st, err := u.control().Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Projects) != 1 || st.Projects[0].State == string(engine.StateRunning) || !strings.Contains(st.Projects[0].Message, errHostsNeedTerminal.Error()) {
		t.Errorf("Projects = %+v, want one not running, with %q", st.Projects, errHostsNeedTerminal)
	}
	if b, _ := os.ReadFile(stale); string(b) != "127.0.0.1 localhost\n" {
		t.Errorf("the hosts file changed:\n%s", b)
	}
	if err := u.stop(); err != nil {
		t.Fatalf("daemon returned %v; log:\n%s", err, u.coreLog())
	}
	// The reason says what to do; a fix that says to run up would not.
	if log := u.coreLog(); strings.Contains(log, "fix:") {
		t.Errorf("the log has a fix for the hosts file:\n%s", log)
	}
}

// The message the daemon leaves says to run `nearhost hosts update`: after it
// does, the daemon starts the project it had left stopped.
func TestDaemonStartsAProjectAfterHostsUpdate(t *testing.T) {
	stale := filepath.Join(testutil.TempDir(t, "hosts"), "hosts")
	if err := os.WriteFile(stale, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, beforeApplier, beforeTerm := hosts.DefaultPath, newHostsUpdateApplier, stdinIsTerminal
	t.Cleanup(func() { hosts.DefaultPath, newHostsUpdateApplier, stdinIsTerminal = before, beforeApplier, beforeTerm })
	hosts.DefaultPath = stale
	stdinIsTerminal = func() bool { return true }
	newHostsUpdateApplier = func(context.Context, bool) hosts.Applier {
		return func(aliases []string) error { return hosts.Apply(stale, aliases) }
	}

	u := newUpRun(t, &config.Config{Projects: []config.Project{localProject("one", testutil.Upstream(t, "one"), true)}})
	u.adjust = func(b *upBackend) {
		b.ensureHosts = func(context.Context, []string) error {
			t.Error("the daemon called the hosts function that elevates")
			return errors.New("elevated")
		}
	}
	u.startDaemon()
	u.waitForLog("Nothing is being served.\n")
	if st, err := u.control().Status(context.Background()); err != nil || len(st.Projects) != 1 || st.Projects[0].State == string(engine.StateRunning) {
		t.Fatalf("before the update: %+v, %v; want one project not running", st, err)
	}

	var out syncBuffer
	if err := executeInto(context.Background(), &out, newHostsCmd(nil), "hosts", "update", "--config", u.cfgPath); err != nil {
		t.Fatalf("hosts update: %v\n%s", err, out.String())
	}
	u.waitForLog("[one] state: running\n")
	if got := u.get("one.test"); !strings.HasPrefix(got, "one:one.test") {
		t.Errorf("served %q through the proxy", got)
	}
	if err := u.stop(); err != nil {
		t.Fatalf("daemon returned %v; log:\n%s", err, u.coreLog())
	}
}

func TestDaemonKeepsRunningWithNothingToServe(t *testing.T) {
	fastWatch(t)
	u := newUpRun(t, &config.Config{})
	u.noHosts = true
	u.startDaemon()
	u.waitForLog("Nothing is being served.\n")
	time.Sleep(200 * time.Millisecond)
	u.stillRunning()
	saveConfig(t, u.cfgPath, &config.Config{Projects: []config.Project{localProject("a", testutil.Upstream(t, "a"), true)}})
	u.waitForLog("[a] state: running\n")
	if got := u.get("a.test"); !strings.HasPrefix(got, "a:a.test") {
		t.Fatalf("served %q through the proxy", got)
	}
	if err := u.stop(); err != nil {
		t.Fatalf("daemon returned %v; log:\n%s", err, u.coreLog())
	}
}

func TestDaemonStartedBy(t *testing.T) {
	u := newUpRun(t, &config.Config{})
	u.noHosts = true
	u.startDaemon("--started-by", "detached")
	u.waitForLog("Nothing is being served.\n")
	info, err := u.control().Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.StartedBy != control.StartedDetached || !info.Reloads {
		t.Errorf("Info = %+v, want started by detached, and reloads", info)
	}
	if err := u.stop(); err != nil {
		t.Fatalf("daemon returned %v; log:\n%s", err, u.coreLog())
	}

	for _, by := range []string{"launchd", ""} {
		var out syncBuffer
		err := executeInto(context.Background(), &out, newDaemonCmd(u.backend()), "daemon", "--started-by", by)
		if err == nil || err.Error() != "--started-by must be foreground, detached, service or tui" {
			t.Errorf("--started-by %q: %v", by, err)
		}
	}
}

func TestDaemonRefusesASecondCore(t *testing.T) {
	u := newUpRun(t, &config.Config{})
	u.noHosts = true
	u.startDaemon()
	u.waitForLog("Nothing is being served.\n")

	// Ports of its own, so it is the instance lock that refuses it.
	https, http := testutil.ProxyPorts(t)
	var out syncBuffer
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := executeInto(ctx, &out, newDaemonCmd(u.backend()), "daemon", "--no-hosts",
		"--https-port", strconv.Itoa(https), "--http-port", strconv.Itoa(http), "--web-port", "0")
	if !errors.Is(err, engine.ErrAlreadyRunning) || !strings.Contains(err.Error(), "Another Nearhost is already running") {
		t.Fatalf("the second daemon returned %v", err)
	}
	// It says how to stop the other, which runs in the background.
	if msg := err.Error(); !strings.HasSuffix(msg, ". Stop it with `nearhost stop`, or see it with `nearhost status`.") || strings.Contains(msg, "Stop it first") {
		t.Errorf("the refusal reads %q", msg)
	}
	if code := exitCode(err, io.Discard); code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
	// Whoever started it reads why in the log.
	if log := u.coreLog(); !strings.Contains(log, "Another Nearhost is already running") {
		t.Errorf("the refusal is not in the log:\n%s", log)
	}
	u.stillRunning()
	if err := u.stop(); err != nil {
		t.Fatalf("daemon returned %v; log:\n%s", err, u.coreLog())
	}
}

// A service starts its core at login whether or not one runs: beside another
// core, the service's start does nothing and succeeds, so the service
// manager does not count it a failure. Other starts still refuse.
func TestServiceDaemonBesideACore(t *testing.T) {
	u := newUpRun(t, &config.Config{})
	u.noHosts = true
	u.startDaemon()
	u.waitForLog("Nothing is being served.\n")

	// Ports of its own, so it is the instance lock that turns it away.
	https, http := testutil.ProxyPorts(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	daemon := func(by string) error {
		var out syncBuffer
		return executeInto(ctx, &out, newDaemonCmd(u.backend()), "daemon", "--started-by", by, "--no-hosts",
			"--https-port", strconv.Itoa(https), "--http-port", strconv.Itoa(http), "--web-port", "0")
	}
	if err := daemon("service"); err != nil {
		t.Errorf("the service's daemon returned %v", err)
	}
	if log := u.coreLog(); !strings.Contains(log, "Another Nearhost is already running, so this service start does nothing.\n") {
		t.Errorf("the log does not say the service start did nothing:\n%s", log)
	}
	err := daemon("detached")
	if !errors.Is(err, engine.ErrAlreadyRunning) {
		t.Fatalf("the detached daemon returned %v", err)
	}
	if code := exitCode(err, io.Discard); code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
	u.stillRunning()
	if err := u.stop(); err != nil {
		t.Fatalf("daemon returned %v; log:\n%s", err, u.coreLog())
	}
}

func TestCoreLogRotates(t *testing.T) {
	dir := testutil.TempDir(t, "log")
	path := filepath.Join(dir, "core.log")
	big := bytes.Repeat([]byte("x"), 6<<20)
	if err := os.WriteFile(path, big, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".1", []byte("older\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := openCoreLog(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, "new\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "new\n" {
		t.Errorf("core.log = %q, want a new file", b)
	}
	if b, _ := os.ReadFile(path + ".1"); !bytes.Equal(b, big) {
		t.Errorf("core.log.1 holds %d bytes, want the old log's %d", len(b), len(big))
	}

	// A log under the limit is appended to.
	w, err = openCoreLog(path, true)
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(w, "more\n")
	w.Close()
	if b, _ := os.ReadFile(path); string(b) != "new\nmore\n" {
		t.Errorf("core.log = %q, want it appended to", b)
	}

	// Without rotate, as while another core runs, a log over the limit is
	// appended to and left where it is.
	if err := os.WriteFile(path, big, 0o600); err != nil {
		t.Fatal(err)
	}
	w, err = openCoreLog(path, false)
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(w, "turned away\n")
	w.Close()
	if b, _ := os.ReadFile(path); len(b) != len(big)+len("turned away\n") {
		t.Errorf("core.log holds %d bytes, want the old log and the new line", len(b))
	}
	if b, _ := os.ReadFile(path + ".1"); !bytes.Equal(b, big) {
		t.Errorf("core.log.1 was replaced")
	}
}

func TestTSWriterStampsEachLine(t *testing.T) {
	var buf bytes.Buffer
	w := tsWriter(&buf)
	for _, s := range []string{"one\ntw", "o\n", "\n", "three\n"} {
		if n, err := io.WriteString(w, s); n != len(s) || err != nil {
			t.Fatalf("Write(%q) = %d, %v", s, n, err)
		}
	}
	lines := strings.Split(buf.String(), "\n")
	want := []string{"one", "two", "", "three", ""}
	if len(lines) != len(want) {
		t.Fatalf("wrote %q", buf.String())
	}
	for i, line := range lines {
		if got := stamped.ReplaceAllString(line, ""); got != want[i] || (want[i] != "") != stamped.MatchString(line) {
			t.Errorf("line %d = %q, want %q stamped (blank lines bare)", i, line, want[i])
		}
	}
}

// logLine is a 100-byte numbered lines, n of them.
func logLine(i int) string {
	return fmt.Sprintf("line %04d %s\n", i, strings.Repeat("x", 100-len("line 0000 \n")))
}

func TestLogRotatesWhileRunning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.log")
	l, err := newRotatingLog(path, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	const lines = 30
	for i := 0; i < lines; i++ {
		if _, err := l.Write([]byte(logLine(i))); err != nil {
			t.Fatal(err)
		}
	}
	old, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("the log was not set aside: %v", err)
	}
	cur, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cur) > 1024+100 {
		t.Errorf("the log is %d bytes, want at most the limit and one line", len(cur))
	}
	// Whatever is kept is the tail of what was written, in order.
	var want strings.Builder
	for i := 0; i < lines; i++ {
		want.WriteString(logLine(i))
	}
	got := string(old) + string(cur)
	if !strings.HasSuffix(want.String(), got) {
		t.Errorf("the two files are not the tail of the lines written, in order:\n.1: %q\nlog: %q", old, cur)
	}
	if len(old) == 0 || len(cur) == 0 {
		t.Errorf("want lines in both files, got %d and %d bytes", len(old), len(cur))
	}
}

func TestLogRotationKeepsOtherHandles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.log")
	l, err := newRotatingLog(path, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	// Stands in for the core's own stderr, which another handle holds.
	other, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	for i := 0; i < 15; i++ {
		l.Write([]byte(logLine(i)))
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("the log was not set aside: %v", err)
	}
	if _, err := other.WriteString("panic: after the rotation\n"); err != nil {
		t.Fatal(err)
	}
	cur, _ := os.ReadFile(path)
	// An append handle goes to the new end, not its old offset: no hole of
	// zero bytes before the text.
	if bytes.ContainsRune(cur, 0) || !strings.HasSuffix(string(cur), "panic: after the rotation\n") || len(cur) > 1024+100 {
		t.Errorf("want the other handle's text after the lines kept, with no hole, in at most the limit and a line; got %d bytes: %q", len(cur), cur)
	}
}

func TestLogRotationBacksOffAfterFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.log")
	// A directory at path.1 makes every rename over it fail.
	if err := os.MkdirAll(filepath.Join(path+".1", "keep"), 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := newRotatingLog(path, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	short := []byte(strings.Repeat("y", 49) + "\n")
	for i := 0; i < 22; i++ {
		l.Write(short)
	}
	// The log is over the limit and the first copy failed.
	for i := 0; i < 2; i++ {
		l.Write(short)
	}
	if l.copies != 1 {
		t.Fatalf("after the first failure, copies = %d, want 1", l.copies)
	}
	// Past a tenth of the limit more, it tries again.
	for i := 0; i < 2; i++ {
		l.Write(short)
	}
	if l.copies != 2 {
		t.Errorf("after the log grew past the back-off, copies = %d, want 2", l.copies)
	}
}

// A service core's stderr is core.log, opened by launchd or systemd before
// the core starts. The log is not renamed at open, which would leave stderr
// writing to core.log.1, but copied aside and emptied at the first write.
func TestServiceLogKeepsItsStderrAtOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.log")
	big := bytes.Repeat([]byte("x"), coreLogLimit+1)
	if err := os.WriteFile(path, big, 0o600); err != nil {
		t.Fatal(err)
	}
	stderr, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	l, err := openDaemonLog(path, control.StartedService, true)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err := l.Write([]byte("starting\n")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if sfi, err := stderr.Stat(); err != nil || !os.SameFile(fi, sfi) {
		t.Fatalf("core.log is no longer the file stderr writes to (%v)", err)
	}
	if b, _ := os.ReadFile(path + ".1"); !bytes.Equal(b, big) {
		t.Errorf("core.log.1 holds %d bytes, want the old log's %d", len(b), len(big))
	}
	if _, err := stderr.WriteString("panic: x\n"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "starting\npanic: x\n" {
		t.Errorf("core.log = %q, want the new line and stderr's", b)
	}
}

// A core that starts while another holds the instance lock is about to be
// turned away, as a service core at login beside a running one: it never sets
// aside the log the running core writes to, at open or after.
func TestLogOfACoreTurnedAwayNeverRotates(t *testing.T) {
	for _, by := range []control.StartedBy{control.StartedService, control.StartedDetached} {
		t.Run(string(by), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "core.log")
			big := bytes.Repeat([]byte("x"), coreLogLimit+1)
			if err := os.WriteFile(path, big, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path+".1", []byte("older\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			l, err := openDaemonLog(path, by, false)
			if err != nil {
				t.Fatal(err)
			}
			const line = "Another Nearhost is already running, so this service start does nothing.\n"
			if _, err := l.Write([]byte(line)); err != nil {
				t.Fatal(err)
			}
			l.Close()
			if b, _ := os.ReadFile(path); len(b) != len(big)+len(line) {
				t.Errorf("core.log holds %d bytes, want the old log and the new line", len(b))
			}
			if b, _ := os.ReadFile(path + ".1"); string(b) != "older\n" {
				t.Errorf("core.log.1 was replaced")
			}
		})
	}
}
