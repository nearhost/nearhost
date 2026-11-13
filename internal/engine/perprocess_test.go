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
// until the project stops. The fake ssh's forward on 127.0.0.1 greets each
// connection and closes it, so the greeting read on [::1] has come through
// the relay and the forward.
func TestPerProcessLoopbackForwardAnswersOnTheIPv6Loopback(t *testing.T) {
	t.Setenv("NEARHOST_TRANSPORT", "per-process")
	_, env := newPerProcessFake(t)
	env = append(env, "FAKE_SSH_FORWARD_GREETING=hello from 127.0.0.1")
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
	got, err := io.ReadAll(c)
	c.Close()
	if err != nil || string(got) != "hello from 127.0.0.1" {
		t.Fatalf("read %q, %v on [::1]; want the forward's greeting", got, err)
	}
	if err := h.eng.StopProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	if c, err := net.DialTimeout("tcp", fmt.Sprintf("[::1]:%d", lb), time.Second); err == nil {
		c.Close()
		t.Error("[::1] still answers after the project stopped")
	}
}

// A server that gives up on authentication closes its passthroughs, and
// its log lines are not held up by a close that is still cancelling.
func TestPerProcessAuthFailedClosesPassthroughs(t *testing.T) {
	t.Setenv("NEARHOST_TRANSPORT", "per-process")
	fake, env := newPerProcessFake(t)
	ssh, flag, _ := authWrapper(t)
	h, _ := endHarness(t, ssh, env...)
	held := h.holdCancels(t, "apollo")
	events, cancel := h.eng.bus.Subscribe(256)
	defer cancel()
	p := openP1(t, h)
	other := passthroughSpec(testutil.FreePort(t))
	other.ID = "p2"
	if _, err := h.eng.OpenPassthrough(bg, other); err != nil {
		t.Fatal(err)
	}
	// Closing p2 waits inside its cancel until the hold is released.
	held.hold()
	closing := make(chan error, 1)
	go func() { closing <- h.eng.ClosePassthrough(bg, "p2", "the agent finished") }()
	testutil.WaitUntil(t, 5*time.Second, func() bool { return held.asked.Load() > 0 }, "p2's cancel to start")
	if err := os.WriteFile(flag, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fake.drop(t, fake.linkPid(t))
	waitStatus(t, h, "auth failed", func(st transport.Status) bool { return st == transport.StatusAuthFailed })
	logged := make(chan struct{})
	go func() {
		h.eng.serverLog("apollo", "still failing")
		close(logged)
	}()
	select {
	case <-logged:
	case <-time.After(5 * time.Second):
		t.Fatal("serverLog blocked while a close was cancelling")
	}
	held.release()
	if err := <-closing; err != nil {
		t.Fatal(err)
	}
	waitClosedEvent(t, events, p.Remote, "apollo's connection failed: user@host: Permission denied (publickey).")
	assertClosed(t, h, p.Local)
}

// While a project is torn down, Status keeps what was last announced and
// nothing is announced; the stop then announces stopped. The teardown is
// held inside its first forward cancel by a wrapper of the transport.
func TestPerProcessStatusDuringTeardownKeepsTheAnnouncedState(t *testing.T) {
	for _, variant := range []string{"healthy", "after a lost forward"} {
		t.Run(variant, func(t *testing.T) {
			t.Setenv("NEARHOST_TRANSPORT", "per-process")
			flag := filepath.Join(t.TempDir(), "forward-fails")
			lb := testutil.FreePort(t)
			h := newHarness(t, remoteConfig(lb), "FAKE_SSH_FORWARD_FAIL_FILE="+flag)
			events, unsub := h.eng.Events().Subscribe(1024)
			defer unsub()
			var before ProjectStatus
			if variant == "healthy" {
				mustStart(t, h, "myapp")
				before = h.eng.Status("myapp")
			} else {
				before = loseLoopbackForward(t, h, flag, lb)
			}
			stateEvents(events, "myapp") // drop what led here

			held := h.holdCancels(t, "mini")
			held.hold()
			stopped := make(chan error, 1)
			go func() { stopped <- h.eng.StopProject(bg, "myapp") }()
			testutil.WaitUntil(t, 10*time.Second, func() bool { return held.asked.Load() > 0 }, "the teardown to reach a cancel")
			for i := 0; i < 20; i++ {
				during := h.eng.Status("myapp")
				if during.State != before.State || during.Message != before.Message {
					t.Fatalf("status during the teardown = %+v, want what was last announced: %+v", during, before)
				}
				time.Sleep(5 * time.Millisecond)
			}
			if states, _ := stateEvents(events, "myapp"); len(states) != 0 {
				t.Fatalf("state events during the teardown: %v", states)
			}

			held.release()
			if err := <-stopped; err != nil {
				t.Fatal(err)
			}
			if st := h.eng.Status("myapp"); st.State != StateStopped {
				t.Fatalf("status = %+v", st)
			}
			if states, _ := stateEvents(events, "myapp"); fmt.Sprint(states) != fmt.Sprint([]State{StateStopped}) {
				t.Fatalf("state events of the stop = %v, want [stopped]", states)
			}
		})
	}
}

// A start asked for while the project is being stopped waits for the stop,
// then starts it with all its forwards.
func TestPerProcessStartDuringAStopWaitsForItAndThenStarts(t *testing.T) {
	t.Setenv("NEARHOST_TRANSPORT", "per-process")
	h := newHarness(t, remoteConfig(testutil.FreePort(t)))
	events, unsub := h.eng.Events().Subscribe(1024)
	defer unsub()
	mustStart(t, h, "myapp")
	stateEvents(events, "myapp") // drop what led here

	// The stop is held inside its first forward cancel.
	held := h.holdCancels(t, "mini")
	held.hold()
	stopped := make(chan error, 1)
	go func() { stopped <- h.eng.StopProject(bg, "myapp") }()
	testutil.WaitUntil(t, 10*time.Second, func() bool { return held.asked.Load() > 0 }, "the teardown to reach a cancel")

	started := make(chan error, 1)
	go func() { started <- h.eng.StartProject(bg, "myapp") }()
	// It has nothing to report while the stop is under way. (A start that
	// only gets going after this pause is caught by what follows.)
	select {
	case err := <-started:
		t.Fatalf("StartProject returned %v while the project was being stopped", err)
	case <-time.After(300 * time.Millisecond):
	}
	held.release()
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-started:
		if err != nil {
			t.Fatalf("StartProject after the stop: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the start never finished")
	}
	if st := h.eng.Status("myapp"); st.State != StateRunning || st.WebLocalPort == 0 {
		t.Fatalf("status = %+v: the start requested during the stop was lost", st)
	}
	if f := h.forwards("mini"); len(f) != 2 || len(h.unapplied("mini")) != 0 || len(h.eng.Sites()) != 1 {
		t.Fatalf("forwards = %v, unapplied %v, sites %v", f, h.unapplied("mini"), h.eng.Sites())
	}
	states, _ := stateEvents(events, "myapp")
	if want := []State{StateStopped, StateStarting, StateRunning}; fmt.Sprint(states) != fmt.Sprint(want) {
		t.Fatalf("state events = %v, want %v", states, want)
	}
}

// A closing passthrough keeps its record, its id and its port until its
// cancel is done.
func TestPerProcessCloseKeepsRecordUntilCancelled(t *testing.T) {
	t.Setenv("NEARHOST_TRANSPORT", "per-process")
	port := testutil.FreePort(t)
	h, _ := endHarness(t, "")
	held := h.holdCancels(t, "apollo")
	if _, err := h.eng.OpenPassthrough(bg, passthroughSpec(port)); err != nil {
		t.Fatal(err)
	}
	held.hold()
	done := make(chan error, 1)
	go func() { done <- h.eng.ClosePassthrough(bg, "p1", "done") }()
	testutil.WaitUntil(t, 10*time.Second, func() bool { return held.asked.Load() > 0 }, "the close to reach its cancel")

	if got := h.eng.Passthroughs(""); len(got) != 0 {
		t.Fatalf("Passthroughs = %+v while it closes", got)
	}
	// A project's start claims its loopback ports this way.
	if holder, ok := h.eng.claimPortFor("one", port); ok || holder != "a passthrough for claude-code on apollo (session 1a2b3c4d)" {
		t.Fatalf("a project's claim got ok %v, holder %q", ok, holder)
	}
	if _, err := h.eng.OpenPassthrough(bg, passthroughSpec(port)); err == nil {
		t.Fatal("an open with the id of a closing passthrough was accepted")
	}
	if h.eng.PortFree(port) {
		t.Fatal("the port was freed before the cancel ended")
	}

	held.release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	h.eng.mu.Lock()
	_, kept := h.eng.passthroughs["p1"]
	h.eng.mu.Unlock()
	if kept {
		t.Fatal("the record is kept after the cancel")
	}
	if !h.eng.PortFree(port) {
		t.Fatal("the port is still held after the cancel")
	}
}

// A second close of a passthrough whose first close is still cancelling
// returns at once, and the passthrough is cancelled and announced closed
// once. (The per-process transport's cancel cannot fail, so the Unix test's
// failing variant has no counterpart here.)
func TestPerProcessConcurrentClosesReleaseOnce(t *testing.T) {
	t.Setenv("NEARHOST_TRANSPORT", "per-process")
	h, _ := endHarness(t, "")
	held := h.holdCancels(t, "apollo")
	events, cancel := h.eng.bus.Subscribe(64)
	defer cancel()
	p := openP1(t, h)
	held.hold()
	first := make(chan error, 1)
	go func() { first <- h.eng.ClosePassthrough(bg, "p1", "done") }()
	testutil.WaitUntil(t, 10*time.Second, func() bool { return held.asked.Load() > 0 }, "the first close to reach its cancel")
	// The second close returns while the first still waits.
	if err := h.eng.ClosePassthrough(bg, "p1", "done again"); err != nil {
		t.Fatalf("second close: %v", err)
	}
	held.release()
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	assertClosed(t, h, p.Local)
	if n := held.asked.Load(); n != 1 {
		t.Fatalf("%d cancels, want 1", n)
	}
	n := 0
	prefix := fmt.Sprintf("closed port %d for ", p.Remote)
	for len(events) > 0 {
		if ev := <-events; strings.HasPrefix(ev.Message, prefix) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d close events, want 1", n)
	}
}
