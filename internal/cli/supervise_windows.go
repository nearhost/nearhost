//go:build windows

package cli

import (
	"errors"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"
)

// superviseService is the service's process on Windows: it runs this binary
// with the same arguments and --no-supervisor as the core, and restarts it
// as supervise says, since Task Scheduler never restarts a task that fails.
// The core shares the process's console, conhost's headless one, so it opens
// no window and gets the same Ctrl and sign-out events.
func superviseService(*cobra.Command) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := append(append([]string(nil), os.Args[1:]...), "--no-supervisor")
	code := supervise(superviseDeps{
		run: func() (int, error) {
			c := exec.Command(exe, args...)
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := c.Start(); err != nil {
				return 0, err
			}
			// A core that is killed or fails has a non-zero exit code; Wait's
			// error says no more than that.
			_ = c.Wait()
			return c.ProcessState.ExitCode(), nil
		},
		sleep: time.Sleep,
		now:   time.Now,
		note:  noteInCoreLog,
	})
	if code != 0 {
		return alreadySaid{errors.New("the core kept stopping, so the service stopped restarting it")}
	}
	return nil
}
