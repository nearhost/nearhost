package engine

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/testutil"
	"github.com/nearhost/nearhost/internal/transport"
)

// The engine under the per-process transport: one ssh for the link and one
// `ssh -N -L` per forward, each of which may die and be relaunched alone.
// Each test asks for it with NEARHOST_TRANSPORT, which TestMain unsets for
// everything else.

// perProcessFake is where the fake ssh of a per-process test reports the
// pids of its plain processes, and the directory a pid is dropped through.
type perProcessFake struct {
	pidLog, dropDir string
}

// newPerProcessFake returns the fake's files and the environment that
// points the fake ssh at them.
func newPerProcessFake(t *testing.T) (*perProcessFake, []string) {
	t.Helper()
	dir := t.TempDir()
	f := &perProcessFake{pidLog: filepath.Join(dir, "pid.log"), dropDir: filepath.Join(dir, "drop")}
	if err := os.Mkdir(f.dropDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return f, []string{"FAKE_SSH_PID_LOG=" + f.pidLog, "FAKE_SSH_DROP_DIR=" + f.dropDir}
}

// pids returns the pid and argv of each plain process the fake started, in
// order.
func (f *perProcessFake) pids() (pids []int, argvs []string) {
	b, _ := os.ReadFile(f.pidLog)
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		first, argv, _ := strings.Cut(line, " ")
		if pid, err := strconv.Atoi(first); err == nil {
			pids, argvs = append(pids, pid), append(argvs, argv)
		}
	}
	return pids, argvs
}

// forwardPids returns the pids of the processes started for the forward
// from local port, oldest first.
func (f *perProcessFake) forwardPids(local int) []int {
	var out []int
	pids, argvs := f.pids()
	for i, argv := range argvs {
		if strings.Contains(" "+argv+" ", fmt.Sprintf(" -L 127.0.0.1:%d:", local)) {
			out = append(out, pids[i])
		}
	}
	return out
}

// linkPid returns the pid of the newest link process: one without -L.
func (f *perProcessFake) linkPid(t *testing.T) int {
	t.Helper()
	pids, argvs := f.pids()
	for i := len(pids) - 1; i >= 0; i-- {
		if !strings.Contains(" "+argvs[i]+" ", " -L ") {
			return pids[i]
		}
	}
	t.Fatalf("no link process in %q", argvs)
	return 0
}

// hostLinkPid returns the pid of the newest link process to host.
func (f *perProcessFake) hostLinkPid(t *testing.T, host string) int {
	t.Helper()
	pids, argvs := f.pids()
	for i := len(pids) - 1; i >= 0; i-- {
		argv := " " + argvs[i] + " "
		if !strings.Contains(argv, " -L ") && strings.HasSuffix(argv, " "+host+" ") {
			return pids[i]
		}
	}
	t.Fatalf("no link process to %s in %q", host, argvs)
	return 0
}

// drop makes the fake ssh with pid exit as a lost connection does.
func (f *perProcessFake) drop(t *testing.T, pid int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dropDir, strconv.Itoa(pid)), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// holderCalls is a holder lookup that records the ports it is asked about
// and names every holder as "squatter (pid 1)".
type holderCalls struct {
	mu    sync.Mutex
	ports []int
}

func (c *holderCalls) lookup(_ context.Context, port int) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ports = append(c.ports, port)
	return "squatter (pid 1)"
}

func (c *holderCalls) asked() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.ports)
}

// localPorts returns the local ports of a project's forwards.
func (h *harness) localPorts(project string) []int {
	h.eng.mu.Lock()
	defer h.eng.mu.Unlock()
	var out []int
	for _, f := range h.eng.runs[project].forwards {
		out = append(out, f.local)
	}
	return out
}

func TestPerProcessRemoteProjectServes(t *testing.T) {
	t.Setenv("NEARHOST_TRANSPORT", "per-process")
	_, env := newPerProcessFake(t)
	h := newHarness(t, remoteConfig(testutil.FreePort(t)), env...)
	mustStart(t, h, "myapp")
	if st := h.eng.Status("myapp"); st.State != StateRunning || st.Message != "" {
		t.Fatalf("status = %+v", st)
	}
	var links, forwards []string
	for _, line := range strings.Split(strings.TrimSpace(h.log()), "\n") {
		argv := " " + line + " "
		if strings.Contains(argv, " -M ") || strings.Contains(argv, " -O ") {
			t.Errorf("a ControlMaster invocation under per-process: %q", line)
		}
		switch {
		case strings.Contains(argv, " -N ") && strings.Contains(argv, " -L "):
			forwards = append(forwards, line)
		case strings.Contains(argv, " -N "):
			links = append(links, line)
		}
	}
	if len(links) != 1 {
		t.Errorf("want one link, got %q", links)
	}
	// A web route and a loopback route; the ignored route has no forward.
	for _, local := range h.localPorts("myapp") {
		n := 0
		for _, line := range forwards {
			if strings.Contains(" "+line+" ", fmt.Sprintf(" -L 127.0.0.1:%d:", local)) {
				n++
			}
		}
		if n != 1 {
			t.Errorf("want one ssh -N -L for local port %d, got %d in %q", local, n, forwards)
		}
	}
	if len(forwards) != 2 {
		t.Errorf("want two forwards, got %q", forwards)
	}
}

func TestPerProcessStopStartTouchesOnlyThatProject(t *testing.T) {
	t.Setenv("NEARHOST_TRANSPORT", "per-process")
	fake, env := newPerProcessFake(t)
	cfg := &config.Config{
		Servers: []config.Server{{Name: "mini", SSHHost: "mini"}},
		Projects: []config.Project{
			remoteProject("first", "mini", testutil.FreePort(t)),
			remoteProject("second", "mini", testutil.FreePort(t)),
		},
	}
	h := newHarness(t, cfg, env...)
	mustStart(t, h, "first")
	mustStart(t, h, "second")
	firstPorts, secondPorts := h.localPorts("first"), h.localPorts("second")
	secondPids := map[int][]int{}
	for _, local := range secondPorts {
		secondPids[local] = fake.forwardPids(local)
		if len(secondPids[local]) != 1 {
			t.Fatalf("second's forward from %d has pids %v", local, secondPids[local])
		}
	}

	if err := h.eng.StopProject(bg, "first"); err != nil {
		t.Fatal(err)
	}
	// The first project's forwards end: their ports are free again.
	for _, local := range firstPorts {
		testutil.WaitUntil(t, 5*time.Second, func() bool { return !portOpen(local) }, fmt.Sprintf("first's forward from %d to end", local))
	}
	// The second's go on in the processes they had.
	for _, local := range secondPorts {
		if got := fake.forwardPids(local); !slices.Equal(got, secondPids[local]) {
			t.Errorf("second's forward from %d: pids %v, were %v", local, got, secondPids[local])
		}
		if !portOpen(local) {
			t.Errorf("second's forward from %d no longer listens", local)
		}
	}
	if st := h.eng.Status("second"); st.State != StateRunning || st.Message != "" {
		t.Fatalf("second = %+v", st)
	}

	// Starting the first again starts only its own forwards.
	mustStart(t, h, "first")
	for _, local := range secondPorts {
		if got := fake.forwardPids(local); !slices.Equal(got, secondPids[local]) {
			t.Errorf("after restarting first, second's forward from %d: pids %v, were %v", local, got, secondPids[local])
		}
	}
	for _, name := range []string{"first", "second"} {
		if st := h.eng.Status(name); st.State != StateRunning || st.Message != "" {
			t.Errorf("%s = %+v", name, st)
		}
	}
}

// A forward whose ssh died is relaunched by the transport by itself. Until
// it is back the project is degraded, saying so, not in error over a port
// that nothing holds, and nobody is asked who holds it.
func TestPerProcessLostForwardIsDegradedNotAnError(t *testing.T) {
	t.Setenv("NEARHOST_TRANSPORT", "per-process")
	fake, env := newPerProcessFake(t)
	h := buildHarness(t, remoteConfig(testutil.FreePort(t)), nil, env...)
	holders := &holderCalls{}
	h.eng.holderOf = holders.lookup
	if err := h.eng.Start(bg); err != nil {
		t.Fatal(err)
	}
	mustStart(t, h, "myapp")
	events, unsub := h.eng.Events().Subscribe(1024)
	defer unsub()
	web := h.eng.Status("myapp").WebLocalPort
	pids := fake.forwardPids(web)
	if len(pids) != 1 {
		t.Fatalf("web forward pids %v", pids)
	}

	fake.drop(t, pids[0])
	prefix := "the web route for port 80 is reconnecting: "
	var seen []string
	sawDegraded := false
	deadline := time.Now().Add(15 * time.Second)
	for {
		st := h.eng.Status("myapp")
		if shown := string(st.State) + ": " + st.Message; len(seen) == 0 || seen[len(seen)-1] != shown {
			seen = append(seen, shown)
		}
		if st.State == StateError {
			t.Fatalf("the project showed an error: %+v", st)
		}
		if st.State == StateDegraded {
			if !strings.HasPrefix(st.Message, prefix) || len(st.Message) == len(prefix) {
				t.Fatalf("degraded message = %q, want %q and ssh's error", st.Message, prefix)
			}
			sawDegraded = true
		}
		if sawDegraded && st.State == StateRunning && len(fake.forwardPids(web)) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the forward never came back by way of degraded; statuses %q", seen)
		}
		time.Sleep(5 * time.Millisecond)
	}
	states, last := stateEvents(events, "myapp")
	if fmt.Sprint(states) != fmt.Sprint([]State{StateDegraded, StateRunning}) {
		t.Fatalf("state events = %v (%v)", states, last)
	}
	if !strings.HasPrefix(last[StateDegraded], "state: degraded: "+prefix) {
		t.Fatalf("degraded event = %q", last[StateDegraded])
	}
	if asked := holders.asked(); len(asked) != 0 {
		t.Fatalf("the holder of ports %v was looked up", asked)
	}
}

// A forward relaunched onto a local port that something else took in the
// meantime is in error, naming the port and its holder, as ever.
func TestPerProcessTakenPortIsStillAnError(t *testing.T) {
	t.Setenv("NEARHOST_TRANSPORT", "per-process")
	fake, env := newPerProcessFake(t)
	h := buildHarness(t, remoteConfig(testutil.FreePort(t)), nil, env...)
	holders := &holderCalls{}
	h.eng.holderOf = holders.lookup
	if err := h.eng.Start(bg); err != nil {
		t.Fatal(err)
	}
	mustStart(t, h, "myapp")
	web := h.eng.Status("myapp").WebLocalPort
	pids := fake.forwardPids(web)
	if len(pids) != 1 {
		t.Fatalf("web forward pids %v", pids)
	}

	// The forward's ssh dies, and something takes its port before the
	// relaunch, which waits a second first.
	fake.drop(t, pids[0])
	var squatter net.Listener
	testutil.WaitUntil(t, 5*time.Second, func() bool {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", web))
		squatter = l
		return err == nil
	}, "the web forward's port to be free")
	defer squatter.Close()

	want := fmt.Sprintf("the web route for port 80 is not forwarded: ssh could not bind local port %d (in use by squatter (pid 1))", web)
	testutil.WaitUntil(t, 15*time.Second, func() bool {
		st := h.eng.Status("myapp")
		return st.State == StateError && st.Message == want
	}, "the taken port to show as an error")
	if !slices.Contains(holders.asked(), web) {
		t.Fatalf("the holder of %d was not looked up; asked %v", web, holders.asked())
	}
}

func TestPerProcessRevivesAServerThatGaveUp(t *testing.T) {
	t.Setenv("NEARHOST_TRANSPORT", "per-process")
	fake, env := newPerProcessFake(t)
	wrapper, flag, _ := authWrapper(t)
	h := buildHarness(t, remoteConfig(testutil.FreePort(t)), func(o *Options) { o.SSHPath = wrapper }, env...)
	if err := h.eng.Start(bg); err != nil {
		t.Fatal(err)
	}
	mustStart(t, h, "myapp")
	running := h.eng.Status("myapp")

	// The link drops and the key is no longer accepted.
	if err := os.WriteFile(flag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	fake.drop(t, fake.linkPid(t))
	testutil.WaitUntil(t, 20*time.Second, func() bool {
		st, _ := h.eng.ServerStatus("mini")
		return st == transport.StatusAuthFailed
	}, "the server to give up")
	if st := h.eng.Status("myapp"); st.State != StateDegraded || !strings.Contains(st.Message, "Permission denied") {
		t.Fatalf("status = %+v", st)
	}

	if err := os.Remove(flag); err != nil {
		t.Fatal(err)
	}
	mustStart(t, h, "myapp")
	testutil.WaitUntil(t, 10*time.Second, func() bool { return h.eng.Status("myapp").State == StateRunning }, "recovery")
	if got := h.eng.Status("myapp"); got.WebLocalPort != running.WebLocalPort || len(h.forwards("mini")) != 2 {
		t.Fatalf("status = %+v (was %+v), forwards %v", got, running, h.forwards("mini"))
	}
	if !portOpen(running.WebLocalPort) {
		t.Fatalf("the web forward from %d does not listen", running.WebLocalPort)
	}
}

// A running project whose server's link drops is degraded, saying why,
// until the link is back, and then runs again on the local ports it had.
// A local project is not affected.
func TestPerProcessRunningProjectBecomesDegradedWhenItsLinkDrops(t *testing.T) {
	t.Setenv("NEARHOST_TRANSPORT", "per-process")
	fake, env := newPerProcessFake(t)
	cfg := remoteConfig(testutil.FreePort(t))
	cfg.Projects = append(cfg.Projects, localProject("web", upstream(t, "ok")))
	h := newHarness(t, cfg, env...)
	events, unsub := h.eng.Events().Subscribe(1024)
	defer unsub()
	mustStart(t, h, "myapp")
	mustStart(t, h, "web")
	running := h.eng.Status("myapp")

	fake.drop(t, fake.linkPid(t))
	testutil.WaitUntil(t, 20*time.Second, func() bool {
		return h.connections() == 2 && h.eng.Status("myapp").State == StateRunning
	}, "the project to run again on a new link")
	if got := h.eng.Status("myapp"); got.Message != "" || got.WebLocalPort != running.WebLocalPort || len(h.forwards("mini")) != 2 {
		t.Fatalf("status = %+v (was %+v), forwards %v", got, running, h.forwards("mini"))
	}
	if !portOpen(running.WebLocalPort) {
		t.Fatalf("the web forward from %d does not listen", running.WebLocalPort)
	}
	states, last := stateEvents(events, "myapp")
	if want := []State{StateStarting, StateRunning, StateDegraded, StateRunning}; fmt.Sprint(states) != fmt.Sprint(want) {
		t.Fatalf("state events = %v, want %v", states, want)
	}
	if !strings.Contains(last[StateDegraded], "Broken pipe") {
		t.Fatalf("degraded event %q does not say why the link dropped", last[StateDegraded])
	}
	if states, _ := stateEvents(events, "web"); len(states) != 0 {
		t.Fatalf("the local project's state changed with the link: %v", states)
	}
	if st := h.eng.Status("web"); st.State != StateRunning {
		t.Fatalf("a local project is not affected by a server dropping: %+v", st)
	}
}

// Shutdown does not wait for a start whose link has not come up.
func TestPerProcessShutdownInterruptsAStartInProgress(t *testing.T) {
	t.Setenv("NEARHOST_TRANSPORT", "per-process")
	_, env := newPerProcessFake(t)
	hold := filepath.Join(t.TempDir(), "link-holds")
	if err := os.WriteFile(hold, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, remoteConfig(testutil.FreePort(t)), append(env, "FAKE_SSH_LINK_HOLD="+hold)...)
	t.Cleanup(func() { os.Remove(hold) }) // before the harness shuts down: never leave ssh waiting
	started := make(chan error, 1)
	go func() { started <- h.eng.StartProject(bg, "myapp") }()
	testutil.WaitUntil(t, 5*time.Second, func() bool {
		return h.eng.Status("myapp").State == StateStarting && h.connections() == 1
	}, "the start to be waiting for ssh")
	begin := time.Now()
	if err := h.eng.Shutdown(bg); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if d := time.Since(begin); d > 10*time.Second {
		t.Fatalf("Shutdown waited %s for a start that could not finish", d)
	}
	if err := <-started; err == nil {
		t.Fatal("the interrupted start must report an error")
	}
	if st := h.eng.Status("myapp"); st.State != StateStopped || len(h.eng.Sites()) != 0 {
		t.Fatalf("status = %+v", st)
	}
}

// A loopback forward answers on [::1] too, through a relay to 127.0.0.1,
// until the project stops. The fake ssh's forward on 127.0.0.1 accepts a
// connection and closes it, so a connection on [::1] is accepted and ends.
func TestPerProcessLoopbackForwardAnswersOnTheIPv6Loopback(t *testing.T) {
	t.Setenv("NEARHOST_TRANSPORT", "per-process")
	_, env := newPerProcessFake(t)
	probe, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback: %v", err)
	}
	lb := probe.Addr().(*net.TCPAddr).Port
	probe.Close()
	h := newHarness(t, remoteConfig(lb), env...)
	mustStart(t, h, "myapp")
	h.eng.mu.Lock()
	_, relayed := h.eng.relays[lb]
	h.eng.mu.Unlock()
	if !relayed {
		t.Fatalf("no relay on [::1] for the loopback forward from %d", lb)
	}
	c, err := net.DialTimeout("tcp", fmt.Sprintf("[::1]:%d", lb), 5*time.Second)
	if err != nil {
		t.Fatalf("dial [::1]: %v", err)
	}
	c.SetDeadline(time.Now().Add(5 * time.Second))
	_, err = io.ReadAll(c)
	c.Close()
	if err != nil {
		t.Fatalf("the connection on [::1] did not end as the forward ended it: %v", err)
	}
	if err := h.eng.StopProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	if c, err := net.DialTimeout("tcp", fmt.Sprintf("[::1]:%d", lb), time.Second); err == nil {
		c.Close()
		t.Error("[::1] still answers after the project stopped")
	}
}

// A server that gives up on authentication closes its passthroughs.
func TestPerProcessAuthFailedClosesPassthroughs(t *testing.T) {
	t.Setenv("NEARHOST_TRANSPORT", "per-process")
	fake, env := newPerProcessFake(t)
	ssh, flag, _ := authWrapper(t)
	h, _ := endHarness(t, ssh, env...)
	events, cancel := h.eng.bus.Subscribe(256)
	defer cancel()
	p := openP1(t, h)
	if err := os.WriteFile(flag, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fake.drop(t, fake.linkPid(t))
	waitStatus(t, h, "auth failed", func(st transport.Status) bool { return st == transport.StatusAuthFailed })
	waitClosedEvent(t, events, p.Remote, "apollo's connection failed: user@host: Permission denied (publickey).")
	assertClosed(t, h, p.Local)
}
