//go:build windows

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/service"
	"github.com/spf13/cobra"
)

// superviseService is the service's process on Windows: it runs nearhost
// with the same arguments and --no-supervisor as the core, and restarts it
// as supervise says, since Task Scheduler never restarts a task that fails.
// The core shares the process's console, conhost's headless one, so it opens
// no window and gets the same Ctrl and sign-out events. Nothing this process
// says reaches a terminal, so its own failures go to the core log.
func superviseService(*cobra.Command) (err error) {
	defer func() {
		v := recover()
		if v == nil {
			return
		}
		dir := ""
		if p, perr := config.LogPath(); perr == nil {
			dir = filepath.Dir(p)
		}
		m := report.CrashedIn(dir, v, debug.Stack())
		noteInCoreLog("The service's supervisor crashed: " + m.Error())
		err = alreadySaid{m}
	}()
	exe, err := os.Executable()
	if err != nil {
		noteInCoreLog(fmt.Sprintf("The service could not find its own program (%v), so it cannot start the core.", err))
		return alreadySaid{err}
	}
	args := append(append([]string(nil), os.Args[1:]...), "--no-supervisor")
	code := supervise(superviseDeps{
		exe:       exe,
		installed: func() (string, bool) { return installedServiceExe(exe) },
		run: func(exe string) (int, error) {
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

// installedServiceExe is the copy of nearhost the service's task runs now,
// and false once the task has been removed. Should the task not be readable,
// the copy running now, current, is started again.
func installedServiceExe(current string) (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return current, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	d, ok, err := service.New(service.DefaultNames, home, service.ExecRunner).Installed(ctx)
	switch {
	case err != nil:
		return current, true
	case !ok:
		return "", false
	case d.Exe == "":
		return current, true
	}
	return d.Exe, true
}
