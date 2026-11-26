package cli

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// superviseFake runs supervise against a clock that only moves when a run
// takes runFor or a sleep passes, and results played back in order (the
// last one repeats).
type superviseFake struct {
	now     time.Time
	runFor  time.Duration
	results []superviseResult
	runs    int
	slept   []time.Duration
	notes   []string
}

type superviseResult struct {
	code int
	err  error
}

func (f *superviseFake) deps() superviseDeps {
	return superviseDeps{
		run: func() (int, error) {
			r := f.results[min(f.runs, len(f.results)-1)]
			f.runs++
			f.now = f.now.Add(f.runFor)
			return r.code, r.err
		},
		sleep: func(d time.Duration) { f.slept = append(f.slept, d); f.now = f.now.Add(d) },
		now:   func() time.Time { return f.now },
		note:  func(line string) { f.notes = append(f.notes, line) },
	}
}

func newSuperviseFake(runFor time.Duration, results ...superviseResult) *superviseFake {
	return &superviseFake{now: time.Date(2026, 11, 26, 16, 0, 0, 0, time.UTC), runFor: runFor, results: results}
}

// A core that exits 0 was stopped on purpose (nearhost stop, an update's
// restart, another core holding the lock): the supervisor ends with it.
func TestSuperviseCleanExitEnds(t *testing.T) {
	f := newSuperviseFake(time.Second, superviseResult{0, nil})
	if got := supervise(f.deps()); got != 0 {
		t.Errorf("supervise = %d, want 0", got)
	}
	if f.runs != 1 || len(f.slept) != 0 || len(f.notes) != 0 {
		t.Errorf("runs %d, slept %v, notes %q; want one run and nothing else", f.runs, f.slept, f.notes)
	}
}

// A core that stops with an error is started again after 5 s.
func TestSuperviseRestartsAfterFailure(t *testing.T) {
	f := newSuperviseFake(time.Second, superviseResult{1, nil}, superviseResult{0, nil})
	if got := supervise(f.deps()); got != 0 {
		t.Errorf("supervise = %d, want 0", got)
	}
	if f.runs != 2 || !slices.Equal(f.slept, []time.Duration{5 * time.Second}) {
		t.Errorf("runs %d, slept %v; want 2 runs and one 5 s wait", f.runs, f.slept)
	}
	if len(f.notes) != 1 || !strings.Contains(f.notes[0], "exit status 1") || !strings.Contains(f.notes[0], "5 s") {
		t.Errorf("notes %q", f.notes)
	}
}

// Five starts within a minute and the supervisor gives up, as systemd's
// StartLimitBurst=5 in StartLimitIntervalSec=60 does.
func TestSuperviseGivesUpAfterFiveStartsInAMinute(t *testing.T) {
	f := newSuperviseFake(time.Second, superviseResult{1, nil})
	if got := supervise(f.deps()); got != 1 {
		t.Errorf("supervise = %d, want 1", got)
	}
	if f.runs != 5 || len(f.slept) != 4 {
		t.Errorf("runs %d, slept %v; want 5 runs and 4 waits", f.runs, f.slept)
	}
	if len(f.notes) == 0 || !strings.Contains(f.notes[len(f.notes)-1], "stopped restarting") {
		t.Errorf("notes %q", f.notes)
	}
}

// Failures further apart than five a minute go on being restarted.
func TestSuperviseKeepsGoingWhenFailuresAreSpreadOut(t *testing.T) {
	results := make([]superviseResult, 0, 9)
	for range 8 {
		results = append(results, superviseResult{1, nil})
	}
	results = append(results, superviseResult{0, nil})
	f := newSuperviseFake(20*time.Second, results...)
	if got := supervise(f.deps()); got != 0 {
		t.Errorf("supervise = %d, want 0", got)
	}
	if f.runs != 9 {
		t.Errorf("runs %d, want 9", f.runs)
	}
}

// A core that cannot be started at all, as a binary an update removed,
// counts as a failure with the same limits, so it never loops.
func TestSuperviseStartErrorCountsAsFailure(t *testing.T) {
	f := newSuperviseFake(0, superviseResult{0, errors.New("exec: file does not exist")})
	if got := supervise(f.deps()); got != 1 {
		t.Errorf("supervise = %d, want 1", got)
	}
	if f.runs != 5 {
		t.Errorf("runs %d, want 5", f.runs)
	}
	if len(f.notes) == 0 || !strings.Contains(f.notes[0], "file does not exist") {
		t.Errorf("notes %q", f.notes)
	}
}
