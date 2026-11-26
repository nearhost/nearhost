package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/report"
)

type fakePair struct {
	info    control.PairInfo
	states  []control.PairState
	polls   int
	pairErr error
}

func (f *fakePair) Pair(context.Context) (control.PairInfo, error) { return f.info, f.pairErr }
func (f *fakePair) PairStatus(context.Context, string) (control.PairState, error) {
	i := f.polls
	f.polls++
	if i >= len(f.states) {
		i = len(f.states) - 1
	}
	return f.states[i], nil
}

func pairCmd(f *fakePair) func() (pairAPI, error) {
	return func() (pairAPI, error) { return f, nil }
}

func quickPair(t *testing.T) {
	old := pairPoll
	pairPoll = time.Millisecond
	t.Cleanup(func() { pairPoll = old })
}

func TestPairCommandWaitsForDevice(t *testing.T) {
	quickPair(t)
	f := &fakePair{
		info:   control.PairInfo{Code: "ABCD2345", URL: "https://nearhost.x/pair/ABCD2345", Expires: time.Now().Add(10 * time.Minute)},
		states: []control.PairState{{}, {}, {Device: "iPad", Paired: true}},
	}
	out, err := execute(t, newPairCmd(pairCmd(f)), "pair")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"https://nearhost.x/pair/ABCD2345", "ABCD2345", `Paired "iPad".`} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if f.polls != 3 {
		t.Errorf("polled %d times, want 3", f.polls)
	}
	// Not on a terminal, so no QR code.
	if strings.ContainsAny(out, "▀▄█") {
		t.Errorf("a QR code was drawn off a terminal:\n%s", out)
	}
	f.polls = 0
	out, err = execute(t, newPairCmd(pairCmd(f)), "pair", "--qr")
	if err != nil || !strings.ContainsAny(out, "▀▄█") {
		t.Errorf("--qr: %v, no QR code in:\n%s", err, out)
	}
}

func TestPairCommandExpired(t *testing.T) {
	quickPair(t)
	f := &fakePair{
		info:   control.PairInfo{Code: "ABCD2345", URL: "https://nearhost.x/pair/ABCD2345", Expires: time.Now().Add(10 * time.Minute)},
		states: []control.PairState{{}, {Expired: true}},
	}
	_, err := execute(t, newPairCmd(pairCmd(f)), "pair")
	if m, ok := report.As(err); !ok || m.Code != report.PairCodeInvalid {
		t.Fatalf("error = %v, want code %s", err, report.PairCodeInvalid)
	}
}

// A code the core has pruned reports nothing, so the command stops at the
// code's own deadline.
func TestPairCommandStopsAtItsDeadline(t *testing.T) {
	quickPair(t)
	f := &fakePair{
		info:   control.PairInfo{Code: "ABCD2345", URL: "https://nearhost.x/pair/ABCD2345", Expires: time.Now().Add(20 * time.Millisecond)},
		states: []control.PairState{{}},
	}
	_, err := execute(t, newPairCmd(pairCmd(f)), "pair")
	if m, ok := report.As(err); !ok || m.Code != report.PairCodeInvalid {
		t.Fatalf("error = %v, want code %s", err, report.PairCodeInvalid)
	}
}

func TestPairCommandNetworkOff(t *testing.T) {
	f := &fakePair{pairErr: control.ErrNetworkOff}
	_, err := execute(t, newPairCmd(pairCmd(f)), "pair")
	m, ok := report.As(err)
	if !ok || m.Code != report.NetworkOff || !strings.Contains(m.Why, "network: section") || !strings.Contains(m.Why, networkDocs) {
		t.Fatalf("error = %#v, want %s saying how to turn it on", err, report.NetworkOff)
	}
}

func TestPairCommandNotRunning(t *testing.T) {
	_, err := execute(t, newPairCmd(func() (pairAPI, error) { return nil, control.ErrNotRunning }), "pair")
	if m, ok := report.As(err); !ok || m.Code != report.CoreNotRunning {
		t.Fatalf("error = %v, want code %s", err, report.CoreNotRunning)
	}
}
