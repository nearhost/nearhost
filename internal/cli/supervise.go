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
)

// superviseDeps are supervise's seams.
type superviseDeps struct {
	// run starts the core and waits for it. err is set when it could not be
	// started at all.
	run   func() (code int, err error)
	sleep func(time.Duration)
	now   func() time.Time
	// note writes a line to the core log.
	note func(line string)
}

// supervise runs the core until it exits 0, and then returns 0: a clean exit
// is a stop on purpose (nearhost stop, an update's restart, another core
// holding the lock). A core that exits non-zero, is killed or cannot be
// started is started again after superviseDelay, until superviseBurst starts
// have happened within superviseWindow; then it says so and returns 1.
//
// It is what Windows' Task Scheduler lacks: its restart on failure never
// reruns a task whose program exits with an error, and conhost, which runs
// the service's nearhost, always exits 0.
func supervise(d superviseDeps) int {
	var starts []time.Time
	for {
		starts = append(starts, d.now())
		code, err := d.run()
		if err == nil && code == 0 {
			return 0
		}
		cutoff := d.now().Add(-superviseWindow)
		recent := starts[:0]
		for _, s := range starts {
			if s.After(cutoff) {
				recent = append(recent, s)
			}
		}
		starts = recent
		if len(starts) >= superviseBurst {
			d.note(fmt.Sprintf("The core stopped %d times within a minute, so the service stopped restarting it. Start it with nearhost -d, or sign out and in.", superviseBurst))
			return 1
		}
		if err != nil {
			d.note(fmt.Sprintf("The core could not be started (%v), so the service tries again in 5 s.", err))
		} else {
			d.note(fmt.Sprintf("The core stopped (exit status %d), so the service starts it again in 5 s.", code))
		}
		d.sleep(superviseDelay)
	}
}
