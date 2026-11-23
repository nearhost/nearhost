package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/service"
	"github.com/nearhost/nearhost/internal/update"
)

// restartCore stops the core c, which says info and st of itself, waits until
// it has gone, and starts it again from exe as it was started: through the
// service when info.StartedBy is service and the installed service runs exe,
// otherwise detached (a tui core comes back detached: no interface would own
// it).
func restartCore(cmd *cobra.Command, b detachBackend, c *control.Client, info control.Info, st control.State, exe string) (*control.Client, control.Info, error) {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	by := control.StartedDetached
	viaService := false
	if info.StartedBy == control.StartedService {
		// The service's core started directly, as when the service runs
		// another exe or cannot be read, says it was started detached: see
		// directArgs.
		var m service.Manager
		var d service.Definition
		b, m, d = b.withService(ctx)
		// With no service to read, the core is started directly, and is
		// then not the service's.
		if m != nil {
			by, viaService = control.StartedService, samePath(d.Exe, exe)
		}
	}
	c, info, _, err := stopAndStart(cmd, b, c, st, exe, by, viaService, nil)
	return c, info, err
}

// stopAndStart stops the core c, which serves as st says, waits until it has
// gone, and starts it again with restartArgs, said to be started by by, from
// exe ("" is b.exe()), through the service when viaService. wrap, when set,
// runs the start, as the interface runs it to catch Ctrl-C.
func stopAndStart(cmd *cobra.Command, b detachBackend, c *control.Client, st control.State, exe string, by control.StartedBy, viaService bool, wrap func(start func() (*control.Client, control.Info, bool, error)) (*control.Client, control.Info, bool, error)) (*control.Client, control.Info, bool, error) {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := stopCore(ctx, b, c); err != nil {
		return nil, control.Info{}, false, err
	}
	args := restartArgs(by, st)
	start := func() (*control.Client, control.Info, bool, error) {
		return startCoreFrom(cmd, b, exe, args, by, viaService)
	}
	if wrap == nil {
		return start()
	}
	return wrap(start)
}

// live counts what a restart closes: open passthroughs, and projects on a
// server other than this machine that are running.
func live(st control.State) (passthroughs, forwards int) {
	passthroughs = len(st.Passthroughs)
	cfg, err := config.Parse([]byte(st.Config))
	if err != nil {
		return passthroughs, 0
	}
	for _, p := range cfg.Projects {
		if p.Server == "" || p.Server == config.LocalServer {
			continue
		}
		if st.Projects[p.Name].State == engine.StateRunning {
			forwards++
		}
	}
	return passthroughs, forwards
}

// liveWords says what live counted, leaving out a part that is none.
func liveWords(passthroughs, forwards int) string {
	switch {
	case forwards == 0:
		return plural(passthroughs, "passthrough")
	case passthroughs == 0:
		return plural(forwards, "forward")
	}
	return plural(passthroughs, "passthrough") + " and " + plural(forwards, "forward")
}

// sayServiceRunsAnother says that the service runs exe, which is the release
// old, and not this copy, of the release this: restarted through the service,
// the core would be old again.
func sayServiceRunsAnother(out io.Writer, exe, old, this string) {
	fmt.Fprintf(out, "Nearhost's service runs %s, which is %s. To have it run this copy (%s), run nearhost service install from it.\n", printable(exe), printable(old), this)
}

// restartAfterUpdate restarts the running core on the new binary exe, now
// that nearhost has been updated to latest: a core in a terminal is the
// user's to restart there, one whose passthroughs and forwards a restart
// would close is asked about first, at a terminal (in), unless yes, and one
// the service starts from another exe is left. noBind says the new binary may
// not bind ports below the first any user may: a core on such a port is left,
// as it would not start again until the setcap advice is followed. With no
// core running, or one of latest already, it does nothing.
func restartAfterUpdate(cmd *cobra.Command, b updateBackend, in *bufio.Reader, yes, noBind bool, latest, exe string) error {
	if b.detach.dial == nil {
		return nil
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	out := cmd.OutOrStdout()
	c, info, ok := detachInfo(ctx, b.detach)
	if !ok || update.SameVersion(info.Version, latest) {
		return nil
	}
	old := printable(info.Version)
	if info.StartedBy == control.StartedForeground {
		fmt.Fprintf(out, "Nearhost running in a terminal (pid %d) is still %s. Restart it there: Ctrl-C, then start it again.\n", info.PID, old)
		return nil
	}
	leave := func() error {
		fmt.Fprintf(out, "Nearhost in the background still runs %s. Restart it when you are ready: nearhost stop, then nearhost -d.\n", old)
		return nil
	}
	// With no path for the new binary there is nothing to restart the
	// core from; and a core that cannot say what it serves cannot be
	// started again as it was. The update went through either way.
	if exe == "" {
		return leave()
	}
	st, err := c.State(ctx)
	if err != nil {
		return leave()
	}
	det := b.detach
	if info.StartedBy == control.StartedService {
		// Looked for once: restartCore asks det again.
		var d service.Definition
		var m service.Manager
		if det, m, d = det.withService(ctx); m != nil && !samePath(d.Exe, exe) {
			sayServiceRunsAnother(out, d.Exe, info.Version, latest)
			return nil
		}
	}
	if port, ok := privilegedPort(b, st); noBind && ok {
		// After the setcap command, apart from it.
		fmt.Fprintf(out, "\nNearhost in the background still runs %s: the new binary cannot serve on port %d until you run the command above. Run it, then restart Nearhost: nearhost stop, then nearhost -d.\n", old, port)
		return nil
	}
	if p, f := live(st); (p > 0 || f > 0) && !yes {
		if !b.interactive() {
			return leave()
		}
		prompt := fmt.Sprintf("Restart Nearhost now to run %s? This closes %s for a moment. [Y/n] ", latest, liveWords(p, f))
		if !askYesNo(in, out, prompt) {
			return leave()
		}
	}
	if info.StartedBy == control.StartedTUI {
		// The interface follows the core it opened on, and quits once that
		// core has gone; the new one is detached, so it does not follow it.
		fmt.Fprintln(out, "The open nearhost window will close; open nearhost again.")
	}
	if _, info, err = restartCore(cmd, det, c, info, st, exe); err != nil {
		return err
	}
	// What the started core says, which is latest unless another copy
	// answered in its place.
	fmt.Fprintf(out, "Restarted Nearhost (pid %d); it now runs %s.\n", info.PID, printable(info.Version))
	return nil
}

// privilegedPort is the lowest port the core that says st of itself serves
// on that is below the first any user may bind, which is taken to be 1024
// when it cannot be read, and false when there is none.
func privilegedPort(b updateBackend, st control.State) (int, bool) {
	first, ok := b.unprivilegedPortStart()
	if !ok {
		first = 1024
	}
	lowest := 0
	for _, p := range []int{st.HTTPSPort, st.HTTPPort} {
		if p > 0 && p < first && (lowest == 0 || p < lowest) {
			lowest = p
		}
	}
	return lowest, lowest > 0
}
