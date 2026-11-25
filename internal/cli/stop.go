package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/report"
)

var (
	// stopWait is how long `nearhost stop` waits for the core to stop: longer
	// than the core's own shutdownTimeout, so a shutdown that runs its full
	// course is still waited for.
	stopWait = shutdownTimeout + 5*time.Second
	// stopPoll is how often it looks meanwhile.
	stopPoll = 100 * time.Millisecond
)

func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the Nearhost running in the background",
		Long: "Stop the running Nearhost, as started by `nearhost -d`, and wait until it\n" +
			"has stopped serving and let go of its ports, so that it can be started\n" +
			"again at once. A `nearhost up` in another terminal stops too. Nothing\n" +
			"running is no failure: stop says so and exits 0.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStop(cmd)
		},
	}
}

// runStop is `nearhost stop`: it asks the core to stop over the control
// socket, then waits until the core has gone.
func runStop(cmd *cobra.Command) error {
	runDir, err := config.RunDir()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	c, err := control.Dial(runDir)
	if errors.Is(err, control.ErrNotRunning) {
		_, err := fmt.Fprintln(out, "Nearhost is not running.")
		return err
	}
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	// A core that stops between the dial and this call has done what was
	// asked.
	if err := c.Stop(ctx); err != nil && !errors.Is(err, control.ErrNotRunning) {
		return err
	}
	if err := waitStopped(ctx, runDir); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, "Stopped.")
	return err
}

// waitStopped waits until the core with the run directory runDir has gone.
// The core closes its control socket as soon as it begins to stop, before
// its projects and ssh connections have, so the socket alone does not say
// it has finished: the engine's instance lock, which it lets go of last,
// once its ports are free, does.
func waitStopped(ctx context.Context, runDir string) error {
	deadline := time.Now().Add(stopWait)
	tick := time.NewTicker(stopPoll)
	defer tick.Stop()
	for {
		if _, err := control.Dial(runDir); errors.Is(err, control.ErrNotRunning) {
			held, err := engine.InstanceRunning(runDir)
			if err != nil {
				return err
			}
			if !held {
				return nil
			}
		}
		if !time.Now().Before(deadline) {
			m := report.Errorf(report.CoreStopTimeout, "Nearhost has not stopped after %s.", stopWait)
			if path, err := config.LogPath(); err == nil {
				m.Step("See why in its log:", path).With("log", path)
			}
			return m
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
