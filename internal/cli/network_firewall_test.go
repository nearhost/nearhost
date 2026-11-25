package cli

import (
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/elevate"
	"github.com/nearhost/nearhost/internal/engine"
)

const firewallTestExe = `C:\Program Files\nearhost\nearhost.exe`

// fakeFirewall stands in for the running executable and netsh, recording
// each netsh call.
func fakeFirewall(t *testing.T) *[]string {
	t.Helper()
	origElevated, origExe, origRun := isElevated, firewallExecutable, netshRun
	t.Cleanup(func() { isElevated, firewallExecutable, netshRun = origElevated, origExe, origRun })
	isElevated = func() bool { return false } // CI's Windows runner is elevated
	firewallExecutable = func() (string, error) { return firewallTestExe, nil }
	var calls []string
	netshRun = func(args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[2] == "show" {
			return []byte("No rules match the specified criteria."), io.ErrUnexpectedEOF
		}
		return []byte("Ok."), nil
	}
	return &calls
}

func execFirewallApply(program string) error {
	root := NewRoot()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"network", "firewall", "apply", "--program", program})
	return root.Execute()
}

// The elevated helper adds a rule only for the program that is running: one
// started with another program's path refuses and runs no netsh.
func TestApplyRefusesOtherProgram(t *testing.T) {
	calls := fakeFirewall(t)
	err := execFirewallApply(`C:\evil.exe`)
	if err == nil {
		t.Fatal("apply took another program")
	}
	if code := exitCode(err, io.Discard); code != elevate.HelperRefusedCode {
		t.Errorf("exit code %d, want %d", code, elevate.HelperRefusedCode)
	}
	if len(*calls) != 0 {
		t.Errorf("netsh ran: %q", *calls)
	}
}

// For the running program the helper adds the rule.
func TestApplyAddsRuleForThisProgram(t *testing.T) {
	calls := fakeFirewall(t)
	if err := execFirewallApply(strings.ToLower(firewallTestExe)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if n := len(*calls); n != 2 || !strings.Contains((*calls)[1], "add rule") {
		t.Errorf("netsh calls = %q, want show then add", *calls)
	}
}

// fakeNetEngine is the engine as the firewall watch sees it.
type fakeNetEngine struct {
	mu       sync.Mutex
	on       bool
	rechecks int
	done     chan struct{}
}

func (f *fakeNetEngine) NetworkState() engine.NetworkState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return engine.NetworkState{On: f.on}
}

func (f *fakeNetEngine) RecheckNetwork(context.Context) error {
	f.mu.Lock()
	f.rechecks++
	f.mu.Unlock()
	close(f.done)
	return nil
}

// While the core is networked and the rule is missing, the watch asks for
// it at each tick; when it appears, the engine applies its network again,
// so it listens, and the watch stops asking. While networked mode is off it
// asks nothing.
func TestCoreTurnsListenOnWhenRuleAppears(t *testing.T) {
	var mu sync.Mutex
	answers := []bool{false, true}
	var asked int
	w := &firewallWatch{check: func() bool {
		mu.Lock()
		defer mu.Unlock()
		a := answers[asked]
		asked++
		return a
	}}
	eng := &fakeNetEngine{done: make(chan struct{})}
	var published []string
	w.publish = func(ev engine.Event) { published = append(published, ev.Message) }
	tick := make(chan time.Time)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		w.run(context.Background(), eng, tick)
	}()
	tick <- time.Time{} // networked mode is off: nothing is asked
	eng.mu.Lock()
	eng.on = true
	eng.mu.Unlock()
	tick <- time.Time{} // asked: not yet
	if w.Ready() {
		t.Fatal("ready before the rule appeared")
	}
	tick <- time.Time{} // asked: there
	select {
	case <-eng.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the network was not applied again")
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the watch went on once the rule was there")
	}
	if !w.Ready() {
		t.Error("not ready once the rule appeared")
	}
	mu.Lock()
	defer mu.Unlock()
	if asked != 2 || eng.rechecks != 1 {
		t.Errorf("asked %d times, rechecked %d times; want 2 and 1", asked, eng.rechecks)
	}
	if !slices.ContainsFunc(published, func(s string) bool { return strings.Contains(s, "firewall") }) {
		t.Errorf("published %q, want a note about the firewall", published)
	}
}

// The watch ends with its context.
func TestFirewallWatchEndsWithContext(t *testing.T) {
	w := &firewallWatch{check: func() bool { return false }}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		w.run(ctx, &fakeNetEngine{on: true, done: make(chan struct{})}, make(chan time.Time))
	}()
	cancel()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the watch went on after its context ended")
	}
}
