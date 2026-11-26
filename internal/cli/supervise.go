package cli

import (
	"fmt"
	"time"
)

// A supervised core that stops with an error is started again after
// superviseDelay, until superviseBurst starts have happened within
// superviseWindow: the systemd unit's RestartSec=5, StartLimitBurst=5 and
// StartLimitIntervalSec=60, so the service restarts the same way on every
// system.
const (
	superviseDelay  = 5 * time.Second
	superviseBurst  = 5
	superviseWindow = 60 * time.Second
	// superviseStartGrace is how soon a stop after the task's start is a
	// failure to start, which is reported, not restarted: within
	// detachTimeout, so nearhost -d and install see the task stop and say
	// why rather than "still starting".
	superviseStartGrace = 10 * time.Second
)

// superviseDeps are supervise's seams.
type superviseDeps struct {
	// exe is the copy of nearhost the first run starts.
	exe string
	// installed says, before each restart, which copy the service runs now,
	// and false once the service has been removed.
	installed func() (exe string, ok bool)
	// run starts exe as the core and waits for it. err is set when it could
	// not be started at all.
	run   func(exe string) (code int, err error)
	sleep func(time.Duration)
	now   func() time.Time
	// note writes a line to the core log.
	note func(line string)
}

// supervise runs the core until it exits 0, and then returns 0: a clean exit
// is a stop on purpose (nearhost stop, an update's restart, another core
// holding the lock). A core that exits non-zero, is killed or cannot be
// started is started again after superviseDelay, from the copy the service
// runs by then, until superviseBurst starts have happened within
// superviseWindow, counted when the restart is due as systemd counts them;
// then it says so and returns 1. The first run stopping within
// superviseStartGrace is not restarted either: the core failed to start
// (port 443 taken, say), which restarting does not mend, and ending here lets
// whoever started the task report it at once. A service removed in the
// meantime is not restarted, and that returns 0.
//
// It is what Windows' Task Scheduler lacks: its restart on failure never
// reruns a task whose program exits with an error, and conhost, which runs
// the service's nearhost, always exits 0.
func supervise(d superviseDeps) int {
	var starts []time.Time
	exe := d.exe
	for first := true; ; first = false {
		started := d.now()
		starts = append(starts, started)
		code, err := d.run(exe)
		if err == nil && code == 0 {
			return 0
		}
		why := "stopped as it started (" + exitStatus(code) + ")"
		if err != nil {
			why = fmt.Sprintf("could not be started (%v)", err)
		}
		if first && d.now().Sub(started) < superviseStartGrace {
			d.note(fmt.Sprintf("The core %s, so the service does not restart it.", why))
			return 1
		}
		if err == nil {
			why = "stopped (" + exitStatus(code) + ")"
		}
		// The restart is due after the wait: starts older than a minute by
		// then do not count.
		cutoff := d.now().Add(superviseDelay - superviseWindow)
		recent := starts[:0]
		for _, s := range starts {
			if s.After(cutoff) {
				recent = append(recent, s)
			}
		}
		starts = recent
		if len(starts) >= superviseBurst {
			d.note(fmt.Sprintf("The core %s. It failed %d times within a minute, so the service stopped restarting it. Start it with nearhost -d, or sign out and in.", why, superviseBurst))
			return 1
		}
		d.note(fmt.Sprintf("The core %s, so the service starts it again in %d s.", why, int(superviseDelay/time.Second)))
		d.sleep(superviseDelay)
		next, ok := d.installed()
		if !ok {
			d.note("The service was removed, so it does not start the core again.")
			return 0
		}
		exe = next
	}
}

// exitStatus is how a core's exit code reads in the log: Windows' status
// codes, such as 0xC000013A for Ctrl-C or 0xFFFFFFFF for a process ended
// with -1, in hex as Windows shows them, and ordinary codes in decimal.
func exitStatus(code int) string {
	if code < 0 || code > 0xFFFF {
		return fmt.Sprintf("exit status 0x%X", uint32(code))
	}
	return fmt.Sprintf("exit status %d", code)
}
