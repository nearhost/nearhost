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
	// exes are the copies each run started.
	exes []string
	// installed answers the check before each restart; nil keeps "nearhost.exe".
	installed func() (string, bool)
}

type superviseResult struct {
	code int
	err  error
	// dur is how long this run takes; 0 uses the fake's runFor.
	dur time.Duration
}

func (f *superviseFake) deps() superviseDeps {
	return superviseDeps{
		exe: "nearhost.exe",
		installed: func() (string, bool) {
			if f.installed != nil {
				return f.installed()
			}
			return "nearhost.exe", true
		},
		run: func(exe string) (int, error) {
			f.exes = append(f.exes, exe)
			r := f.results[min(f.runs, len(f.results)-1)]
			f.runs++
			if r.dur != 0 {
				f.now = f.now.Add(r.dur)
			} else {
				f.now = f.now.Add(f.runFor)
			}
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
	f := newSuperviseFake(time.Second, superviseResult{0, nil, 0})
	if got := supervise(f.deps()); got != 0 {
		t.Errorf("supervise = %d, want 0", got)
	}
	if f.runs != 1 || len(f.slept) != 0 || len(f.notes) != 0 {
		t.Errorf("runs %d, slept %v, notes %q; want one run and nothing else", f.runs, f.slept, f.notes)
	}
}

// A core that stops with an error is started again after 5 s.
func TestSuperviseRestartsAfterFailure(t *testing.T) {
	f := newSuperviseFake(time.Second, superviseResult{1, nil, time.Hour}, superviseResult{0, nil, 0})
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
	f := newSuperviseFake(time.Second, superviseResult{1, nil, 30 * time.Second}, superviseResult{1, nil, 0})
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
		results = append(results, superviseResult{1, nil, 0})
	}
	results = append(results, superviseResult{0, nil, 0})
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
	f := newSuperviseFake(0, superviseResult{1, nil, time.Hour}, superviseResult{0, errors.New("exec: file does not exist"), 0})
	if got := supervise(f.deps()); got != 1 {
		t.Errorf("supervise = %d, want 1", got)
	}
	// The first run, then five starts within a minute.
	if f.runs != 6 {
		t.Errorf("runs %d, want 6", f.runs)
	}
	if len(f.notes) < 2 || !strings.Contains(f.notes[1], "file does not exist") {
		t.Errorf("notes %q", f.notes)
	}
}

// A core that stops within superviseStartGrace of the task's start failed
// to start (port 443 taken, say), which restarting does not mend: the
// supervisor ends with it, so the task stops and nearhost -d or install
// reports the error and the log at once instead of "still starting".
func TestSuperviseDoesNotRestartAFailedFirstStart(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    superviseResult
	}{
		{"exits at once", superviseResult{1, nil, time.Second}},
		{"cannot be started", superviseResult{0, errors.New("exec: file does not exist"), 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSuperviseFake(0, tc.r, superviseResult{0, nil, 0})
			if got := supervise(f.deps()); got != 1 {
				t.Errorf("supervise = %d, want 1", got)
			}
			if f.runs != 1 || len(f.slept) != 0 {
				t.Errorf("runs %d, slept %v; want one run and no restart", f.runs, f.slept)
			}
			if len(f.notes) != 1 || !strings.Contains(f.notes[0], "does not restart") {
				t.Errorf("notes %q", f.notes)
			}
		})
	}
}

// The start limit is counted when the restart is due, after the 5 s wait,
// as systemd counts it: a start that ages out of the minute during the wait
// no longer counts. Starts at 0, 17, 29, 41 and 53 s, the last failing at
// 56 s: by the restart at 61 s the first has aged out.
func TestSuperviseCountsStartsWhenTheRestartIsDue(t *testing.T) {
	f := newSuperviseFake(0,
		superviseResult{1, nil, 12 * time.Second},
		superviseResult{1, nil, 7 * time.Second},
		superviseResult{1, nil, 7 * time.Second},
		superviseResult{1, nil, 7 * time.Second},
		superviseResult{1, nil, 3 * time.Second},
		superviseResult{0, nil, time.Second})
	if got := supervise(f.deps()); got != 0 {
		t.Errorf("supervise = %d, want 0; notes %q", got, f.notes)
	}
	if f.runs != 6 {
		t.Errorf("runs %d, want 6", f.runs)
	}
}

// A service removed while the core was down (service uninstall in the 5 s
// wait) is not restarted: uninstall meant it to stop.
func TestSuperviseStopsWhenTheServiceIsRemoved(t *testing.T) {
	f := newSuperviseFake(0, superviseResult{1, nil, time.Hour})
	f.installed = func() (string, bool) { return "", false }
	if got := supervise(f.deps()); got != 0 {
		t.Errorf("supervise = %d, want 0", got)
	}
	if f.runs != 1 || len(f.notes) != 2 || !strings.Contains(f.notes[1], "removed") {
		t.Errorf("runs %d, notes %q; want one run and a note that the service was removed", f.runs, f.notes)
	}
}

// A restart runs the copy the service runs now, as after service install
// from another copy that did not restart Nearhost.
func TestSuperviseRestartsTheInstalledCopy(t *testing.T) {
	f := newSuperviseFake(0, superviseResult{1, nil, time.Hour}, superviseResult{0, nil, time.Second})
	f.installed = func() (string, bool) { return `C:\new\nearhost.exe`, true }
	supervise(f.deps())
	if want := []string{"nearhost.exe", `C:\new\nearhost.exe`}; !slices.Equal(f.exes, want) {
		t.Errorf("runs started %q, want %q", f.exes, want)
	}
}

// Windows' status codes, such as a kill's 0xFFFFFFFF or Ctrl-C's
// 0xC000013A, read in hex; ordinary exit statuses stay decimal. The wait is
// said from superviseDelay.
func TestSuperviseNotes(t *testing.T) {
	for _, tc := range []struct {
		code int
		want string
	}{
		{1, "(exit status 1)"},
		{4294967295, "(exit status 0xFFFFFFFF)"},
		{3221225786, "(exit status 0xC000013A)"},
	} {
		f := newSuperviseFake(0, superviseResult{tc.code, nil, time.Hour}, superviseResult{0, nil, time.Second})
		supervise(f.deps())
		if len(f.notes) != 1 || !strings.Contains(f.notes[0], tc.want) || !strings.Contains(f.notes[0], "in 5 s") {
			t.Errorf("code %d: notes %q, want one with %q", tc.code, f.notes, tc.want)
		}
	}
}

// Giving up says "failed", which covers cores that stopped and copies that
// could not be started.
func TestSuperviseGiveUpCoversStartErrors(t *testing.T) {
	f := newSuperviseFake(0, superviseResult{1, nil, time.Hour}, superviseResult{0, errors.New("exec: file does not exist"), 0})
	supervise(f.deps())
	if last := f.notes[len(f.notes)-1]; !strings.Contains(last, "failed 5 times within a minute") {
		t.Errorf("last note %q", last)
	}
}
