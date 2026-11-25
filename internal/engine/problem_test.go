package engine

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/transport"
)

// A project whose site the proxy cannot listen for, as the user may not
// bind the HTTPS port, is in error with a message that names the port and
// says how to fix it, while its text stays what older clients read.
func TestProjectErrorCarriesProblem(t *testing.T) {
	h := newHarness(t, instanceConfig(t))
	denied := &net.OpError{Op: "listen", Net: "tcp", Addr: &net.TCPAddr{Port: 443}, Err: os.NewSyscallError("bind", syscall.EACCES)}
	h.eng.applySites = func([]proxy.Site) error {
		return fmt.Errorf("load proxy config: loading new config: http app module: start: listening on :443: %w", denied)
	}
	if err := h.eng.StartProject(bg, "web"); err == nil {
		t.Fatal("StartProject succeeded with the port denied")
	}
	st := h.eng.Status("web")
	if st.State != StateError || st.Problem == nil {
		t.Fatalf("status = %+v, want an error with a problem", st)
	}
	if st.Problem.Code != report.PortNeedsPermission {
		t.Errorf("code = %q, want %q", st.Problem.Code, report.PortNeedsPermission)
	}
	if got := st.Problem.Data["port"]; got != 443 {
		t.Errorf(`Data["port"] = %v (%T), want 443`, got, got)
	}
	if st.Message != st.Problem.Title {
		t.Errorf("Message %q differs from the problem's title %q", st.Message, st.Problem.Title)
	}
	if len(st.Problem.Fix) == 0 {
		t.Error("the problem says nothing about how to fix it")
	}
}

// A project that runs while its server cannot be reached shows the
// server's SSH problem, with the step that fixes access.
func TestDegradedServerProblemIsSSHCode(t *testing.T) {
	cfg := remoteConfig(3306)
	eng := New(cfg, Options{RunDir: t.TempDir()})
	sess := &fakeTransport{host: "mini.example", status: transport.StatusReconnecting, last: "connection refused"}
	eng.mu.Lock()
	eng.servers["mini"] = &serverConn{sess: sess}
	// Recorded running and not announced, so Status shows what follows
	// from the session each time.
	eng.runs["myapp"].state, eng.runs["myapp"].announced = StateRunning, ""
	eng.mu.Unlock()

	st := eng.Status("myapp")
	if st.State != StateDegraded || st.Problem == nil {
		t.Fatalf("status = %+v, want degraded with a problem", st)
	}
	if st.Problem.Code != report.SSHUnreachable {
		t.Errorf("code = %q, want %q", st.Problem.Code, report.SSHUnreachable)
	}
	if st.Message != st.Problem.Title {
		t.Errorf("Message %q differs from the problem's title %q", st.Message, st.Problem.Title)
	}
	// The step names no key of an interface: each adds its own.
	want := report.Step{Text: "To fix access, run ssh once in a terminal:", Command: "ssh mini.example"}
	if len(st.Problem.Fix) == 0 || st.Problem.Fix[0] != want {
		t.Errorf("fix = %+v, want the step %+v", st.Problem.Fix, want)
	}

	// Once it gives up for good, the problem says the login was refused.
	sess.status, sess.last = transport.StatusAuthFailed, "Permission denied (publickey)."
	if st := eng.Status("myapp"); st.Problem == nil || st.Problem.Code != report.SSHAuthFailed {
		t.Errorf("auth failed: problem = %+v, want %q", st.Problem, report.SSHAuthFailed)
	}

	// A project whose state needs nothing has no problem.
	sess.status, sess.last = transport.StatusConnected, ""
	if st := eng.Status("myapp"); st.State != StateRunning || st.Problem != nil {
		t.Errorf("connected: status = %+v, want running with no problem", st)
	}
}

// The warning that Docker did not answer reaches the event stream as a
// message with its own code.
func TestEventsCarryReports(t *testing.T) {
	f := &fakeDocker{answers: []dockerAnswer{{err: errors.New("no Docker socket found")}}}
	h := dockerHarness(t, f, 0, optedIn(t, "a"))
	events, unsubscribe := h.eng.Events().Subscribe(256)
	defer unsubscribe()
	mustStart(t, h, "a")
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Project != "a" || !strings.HasPrefix(ev.Message, "Docker did not answer") {
				continue
			}
			if ev.Report == nil {
				t.Fatalf("event %q carries no report", ev.Message)
			}
			if ev.Report.Level != report.Warning || ev.Report.Code != report.DockerUnreachable {
				t.Fatalf("report = %s %q, want a warning %q", ev.Report.Level, ev.Report.Code, report.DockerUnreachable)
			}
			if ev.Report.Title != ev.Message {
				t.Fatalf("report title %q differs from the event's message %q", ev.Report.Title, ev.Message)
			}
			return
		case <-deadline:
			t.Fatal("no event said Docker did not answer")
		}
	}
}

// Another Nearhost holding the run directory is a message that says how to
// stop it, and still matches ErrAlreadyRunning.
func TestInstanceErrorIsAMessage(t *testing.T) {
	dir := t.TempDir()
	release, err := lockInstance(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, err = lockInstance(dir)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second lock = %v, want ErrAlreadyRunning", err)
	}
	m, ok := report.As(err)
	if !ok {
		t.Fatalf("second lock = %v, want a message", err)
	}
	if m.Code != report.CoreAlreadyRunning {
		t.Errorf("code = %q, want %q", m.Code, report.CoreAlreadyRunning)
	}
	if want := fmt.Sprintf("Another Nearhost is already running (pid %d).", os.Getpid()); m.Title != want {
		t.Errorf("title = %q, want %q", m.Title, want)
	}
	step := "Stop it with `nearhost stop`, or see it with `nearhost status`."
	if len(m.Fix) != 1 || m.Fix[0].Text != step {
		t.Errorf("fix = %+v, want the step %q", m.Fix, step)
	}
	if m.Data["pid"] != os.Getpid() {
		t.Errorf(`Data["pid"] = %v, want %d`, m.Data["pid"], os.Getpid())
	}
}

// A port the proxy cannot listen on is a message with the port, a code for
// why, and a fix.
func TestPortErrorReport(t *testing.T) {
	denied := &portError{what: "HTTPS", port: 443, err: os.NewSyscallError("bind", syscall.EACCES)}
	inUse := &portError{what: "HTTP", port: 80, err: os.NewSyscallError("bind", syscall.EADDRINUSE), heldBy: " by node (pid 4242)"}
	for _, tc := range []struct {
		pe   *portError
		code report.Code
	}{{denied, report.PortNeedsPermission}, {inUse, report.PortInUse}} {
		m, ok := report.As(fmt.Errorf("start: %w", tc.pe))
		if !ok {
			t.Fatalf("%v holds no message", tc.pe)
		}
		if m.Code != tc.code || m.Data["port"] != tc.pe.port || len(m.Fix) == 0 {
			t.Errorf("%v: message = %+v, want %q for port %d with a fix", tc.pe, m, tc.code, tc.pe.port)
		}
		if m.Title != tc.pe.Error() {
			t.Errorf("title = %q, want the error's text %q", m.Title, tc.pe.Error())
		}
		if !errors.Is(tc.pe, tc.pe.err) {
			t.Errorf("%v no longer wraps its cause", tc.pe)
		}
	}
	if !BindNotPermitted(denied) || BindNotPermitted(inUse) {
		t.Error("BindNotPermitted no longer tells the two apart")
	}
	if m, _ := report.As(inUse); m.Data["holder"] != "node (pid 4242)" {
		t.Errorf(`Data["holder"] = %v`, m.Data["holder"])
	}
}

// fakeTransport is a transport whose connection state a test sets.
type fakeTransport struct {
	transport.Transport
	host, last string
	status     transport.Status
}

func (f *fakeTransport) Host() string { return f.host }
func (f *fakeTransport) Status() transport.Status {
	return f.status
}
func (f *fakeTransport) LastError() string { return f.last }
func (f *fakeTransport) Health() transport.Health {
	return transport.Health{Status: f.status, LastError: f.last,
		Problem: transport.ConnectionProblem(f.status, f.last, "mini", f.host)}
}
func (f *fakeTransport) ForwardStates() (transport.Status, []transport.ForwardState) {
	return f.status, nil
}
func (f *fakeTransport) Forwards() [][2]int { return nil }

// Only an error of listening on a port is a port problem: a permission
// refused on a file the proxy reads is the proxy's own error.
func TestBindProblemOnlyForListening(t *testing.T) {
	e := &Engine{opts: Options{HTTPSPort: 443, HTTPPort: 80}}
	listen := &net.OpError{Op: "listen", Net: "tcp", Addr: &net.TCPAddr{Port: 80}, Err: os.NewSyscallError("listen", syscall.EACCES)}
	for _, tc := range []struct {
		err  error
		port bool
	}{
		{errors.New("loading new config: listen tcp :443: bind: permission denied"), true},
		{fmt.Errorf("start: %w", listen), true},
		{fmt.Errorf("open /var/lib/caddy/cert.pem: %w", os.ErrPermission), false},
		{&os.PathError{Op: "open", Path: "/etc/key", Err: syscall.EACCES}, false},
		{&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, false},
	} {
		_, isMessage := report.As(e.bindProblem(tc.err))
		if isMessage != tc.port {
			t.Errorf("bindProblem(%v) is a port message: %v, want %v", tc.err, isMessage, tc.port)
		}
	}
}

// A hosts refusal whose fix updates the hosts file from a terminal needs no
// step to start the project again: the core starts it once that is done.
func TestHostsProblemNoRetryAfterHostsUpdate(t *testing.T) {
	refusal := report.Errorf(report.HostsNeedsUpdate, "the hosts file needs updating.").
		Step("Update it from a terminal:", HostsUpdateCommand)
	m := HostsProblem("web", refusal)
	if len(m.Fix) != 1 || m.Fix[0].Command != HostsUpdateCommand {
		t.Errorf("steps %+v, want only the hosts update", m.Fix)
	}
	other := report.Errorf(report.HostsReadOnly, "read-only.").Step("Clear it:", "attrib -r hosts")
	if m := HostsProblem("web", other); m.Fix[len(m.Fix)-1].Text != RetryThenText {
		t.Errorf("steps %+v, want the retry last", m.Fix)
	}
}
