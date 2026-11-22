package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/engine"
)

const (
	// detachTimeout is how long `nearhost -d` waits for the core it started
	// to answer before it leaves it to go on starting.
	detachTimeout = 15 * time.Second
	// detachPoll is how often it tries the control socket meanwhile.
	detachPoll = 100 * time.Millisecond
	// detachTailLines is how much of the core log it shows when the core
	// stops while starting.
	detachTailLines = 20
)

// detachBackend is the seam between `nearhost -d` and the machine.
type detachBackend struct {
	// spawn starts exe with args detached from this process and its
	// terminal, and returns the child's pid. wait returns once the child has
	// ended, with how it ended, and may be called more than once. A pid of
	// 0 says nothing of which core is the child's (tests).
	spawn func(exe string, args []string) (pid int, wait func() error, err error)
	// dial reaches the running core, or returns control.ErrNotRunning.
	dial func() (*control.Client, error)
	// logSize is the size of the core log, 0 when there is none.
	logSize func() int64
	// logTail returns the last n lines, or fewer, of what was written to
	// the core log from offset from on.
	logTail func(from int64, n int) []string
	// exe is the path of this program, which the child runs.
	exe func() string
	// gone waits until the core has stopped and let go of the instance
	// lock, as `nearhost stop` waits.
	gone func(ctx context.Context) error
	// held reports whether a core holds the instance lock; nil is never.
	held func() bool
	// timeout is how long to wait for the child to answer; zero is
	// detachTimeout. Tests shorten it.
	timeout time.Duration
}

// systemDetach is the production backend.
func systemDetach() detachBackend {
	return detachBackend{
		spawn: func(exe string, args []string) (int, func() error, error) {
			path, err := config.LogPath()
			if err != nil {
				return 0, nil, err
			}
			return spawnDetached(exe, args, path)
		},
		dial: func() (*control.Client, error) {
			dir, err := config.RunDir()
			if err != nil {
				return nil, err
			}
			return control.Dial(dir)
		},
		logSize: func() int64 {
			path, err := config.LogPath()
			if err != nil {
				return 0
			}
			fi, err := os.Stat(path)
			if err != nil {
				return 0
			}
			return fi.Size()
		},
		logTail: func(from int64, n int) []string {
			path, err := config.LogPath()
			if err != nil {
				return nil
			}
			return tailLines(path, from, n)
		},
		exe: func() string {
			if exe, err := os.Executable(); err == nil {
				return exe
			}
			return os.Args[0]
		},
		gone: func(ctx context.Context) error {
			dir, err := config.RunDir()
			if err != nil {
				return err
			}
			return waitStopped(ctx, dir)
		},
		held: func() bool {
			dir, err := config.RunDir()
			if err != nil {
				return false
			}
			held, err := engine.InstanceRunning(dir)
			return held && err == nil
		},
	}
}

// runDetach is `nearhost -d`: it starts `nearhost daemon` detached from the
// terminal and returns once its control socket answers. A core that is
// already running is left be.
func runDetach(cmd *cobra.Command, b detachBackend, flags serveFlags) error {
	_, info, spawned, err := ensureCore(cmd, b, flags, control.StartedDetached)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	switch {
	case spawned:
		fmt.Fprintf(out, "Nearhost is running in the background (pid %d). Stop it with nearhost stop.\n", info.PID)
	case info.StartedBy == control.StartedForeground:
		// `nearhost up`, or a daemon run by hand, in a terminal.
		fmt.Fprintf(out, "Nearhost is already running in a terminal (pid %d). To run it in the background instead, stop it with `nearhost stop` and run `nearhost -d` again.\n", info.PID)
	default:
		fmt.Fprintf(out, "Nearhost is already running in the background (pid %d, started by %s).\n", info.PID, printable(string(info.StartedBy)))
	}
	return nil
}

// ensureCore dials the core, or starts one with startedBy and waits for it
// as -d does. spawned says this call started it. flags are refused before
// anything is dialled when they do not go together; the core is given those
// the user set on cmd. A core that stops while starting, or is still starting
// once the wait is over, is said on standard error, and the error is the exit
// status 1 for Main.
func ensureCore(cmd *cobra.Command, b detachBackend, flags serveFlags, startedBy control.StartedBy) (c *control.Client, info control.Info, spawned bool, err error) {
	if err := flags.validate(); err != nil {
		return nil, control.Info{}, false, err
	}
	args, err := detachArgs(cmd, startedBy)
	if err != nil {
		return nil, control.Info{}, false, err
	}
	return startCore(cmd, b, args, startedBy)
}

// startCore dials the core, or starts one with args, which say it was
// started by startedBy, and waits for it, as ensureCore says.
func startCore(cmd *cobra.Command, b detachBackend, args []string, startedBy control.StartedBy) (c *control.Client, info control.Info, spawned bool, err error) {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	errOut := cmd.ErrOrStderr()
	if c, info, ok := detachInfo(ctx, b); ok {
		return c, info, false, nil
	}
	logPath, err := config.LogPath()
	if err != nil {
		return nil, control.Info{}, false, err
	}
	// The log may hold earlier runs: only what this one writes after this
	// says why it stopped.
	logFrom := b.logSize()
	pid, wait, err := b.spawn(b.exe(), args)
	if err != nil {
		return nil, control.Info{}, false, fmt.Errorf("start Nearhost in the background: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- wait() }()
	timeout := b.timeout
	if timeout == 0 {
		timeout = detachTimeout
	}
	deadline := time.Now().Add(timeout)
	tick := time.NewTicker(detachPoll)
	defer tick.Stop()
	// lost is set once the child has stopped because another core, started
	// at the same moment, holds the lock: that core is waited for instead.
	lost := false
	for {
		// The socket first: a core that answers and then stops at once
		// was running, and whatever stopped it is not a failure to start.
		if c, info, ok := detachInfo(ctx, b); ok {
			// Another nearhost may have started the core that answers.
			return c, info, !lost && (pid == 0 || info.PID == pid), nil
		}
		if !lost {
			select {
			case werr := <-exited:
				if b.held == nil || !b.held() {
					return nil, control.Info{}, false, childStopped(errOut, logPath, b.logTail(logFrom, detachTailLines), werr)
				}
				lost = true
			default:
			}
		}
		if !time.Now().Before(deadline) {
			msg := "Nearhost is still starting. See " + logPath + " for how it is going."
			fmt.Fprintln(errOut, msg)
			return nil, control.Info{}, false, exitError{code: 1, msg: msg}
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			fate := fateLeft
			switch {
			case lost:
				fate = fateOther
			case startedBy == control.StartedTUI:
				fate = stopSpawned(b, pid, exited, deadline)
			}
			return nil, control.Info{}, false, interruptedStart{err: ctx.Err(), fate: fate}
		}
	}
}

// spawnedFate is what became of a core whose start was interrupted.
type spawnedFate int

const (
	// fateLeft is a core left to go on starting: one started for -d, or
	// to restart a core that was not the interface's.
	fateLeft spawnedFate = iota
	// fateStopped is a core stopped once it answered.
	fateStopped
	// fateExited is a core that stopped by itself before it answered.
	fateExited
	// fateNoAnswer is a core that did not answer by the deadline, and may
	// still be starting.
	fateNoAnswer
	// fateStopping is a core asked to stop that had not let go of the
	// instance lock by the deadline.
	fateStopping
	// fateOther is a core that stopped because another nearhost's holds the
	// lock, or was found answering in place of the one started here: the
	// core that runs is another's, and is left running.
	fateOther
)

// interruptedStart is the error of a start whose context ended: the
// context's error, and what became of the core started.
type interruptedStart struct {
	err  error
	fate spawnedFate
}

func (e interruptedStart) Error() string { return e.err.Error() }
func (e interruptedStart) Unwrap() error { return e.err }

// stopSpawned stops the core this process spawned for the interface, which
// was interrupted while the core started: with no interface, nobody would
// know to stop it. The core is stopped once it answers, as it cannot be
// asked before; one that stops by itself, or still does not answer at
// deadline, is left. It returns what became of the core.
func stopSpawned(b detachBackend, pid int, exited <-chan error, deadline time.Time) spawnedFate {
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	tick := time.NewTicker(detachPoll)
	defer tick.Stop()
	for {
		if c, info, ok := detachInfo(ctx, b); ok {
			if pid != 0 && info.PID != pid {
				return fateOther
			}
			if err := c.Stop(ctx); err != nil && !errors.Is(err, control.ErrNotRunning) {
				return fateNoAnswer
			}
			// Gone, as `nearhost stop` waits for, so a nearhost run next
			// does not find it still stopping.
			if b.gone != nil && b.gone(ctx) != nil {
				return fateStopping
			}
			return fateStopped
		}
		select {
		case <-exited:
			return fateExited
		case <-tick.C:
		case <-ctx.Done():
			return fateNoAnswer
		}
	}
}

// detachInfo returns a client of the running core and what the core says of
// itself, and false when nothing answers.
func detachInfo(ctx context.Context, b detachBackend) (*control.Client, control.Info, bool) {
	c, err := b.dial()
	if err != nil {
		return nil, control.Info{}, false
	}
	info, err := c.Info(ctx)
	return c, info, err == nil
}

// childStopped prints that the core stopped while starting, with the end of
// its log, where it said why, and returns the exit status 1 for Main.
func childStopped(w io.Writer, logPath string, tail []string, werr error) error {
	var msg string
	if len(tail) == 0 {
		why := ""
		if werr != nil {
			why = " (" + werr.Error() + ")"
		}
		msg = "Nearhost stopped while starting" + why + ", before it wrote to " + logPath + "."
	} else {
		msg = "Nearhost stopped while starting. The end of " + logPath + ":\n" + printableLines(strings.Join(tail, "\n"))
	}
	fmt.Fprintln(w, msg)
	return exitError{code: 1, msg: msg}
}

// detachServeFlags are the flags of bare `nearhost` that the core takes too.
var detachServeFlags = []string{"https-port", "http-port", "web-port", "no-hosts", "no-capture"}

// detachArgs are the arguments of the core that `nearhost -d` and bare
// `nearhost` start: the daemon, said to be started by startedBy, with
// --config and the serve flags the user gave. Flags not given are left out,
// so the core's defaults are its own.
func detachArgs(cmd *cobra.Command, startedBy control.StartedBy) ([]string, error) {
	args := []string{"daemon", "--started-by", string(startedBy)}
	if cmd.Flags().Changed("config") {
		path, err := cmd.Flags().GetString("config")
		if err != nil {
			return nil, err
		}
		// The core runs in another directory, so a relative path would
		// name another file.
		if path != "" {
			if path, err = filepath.Abs(path); err != nil {
				return nil, err
			}
		}
		args = append(args, "--config", path)
	}
	for _, name := range detachServeFlags {
		f := cmd.Flags().Lookup(name)
		if f == nil || !f.Changed {
			continue
		}
		switch v := f.Value.String(); {
		case f.Value.Type() != "bool":
			args = append(args, "--"+name, v)
		case v == "true":
			args = append(args, "--"+name)
		default:
			args = append(args, "--"+name+"="+v)
		}
	}
	return args, nil
}

// restartArgs are the arguments that start a core again as the one that
// says st of itself was started: by startedBy, on the config file it served
// and on its ports. --no-hosts and --no-capture are not in a core's state,
// so a core started with them is started again without.
func restartArgs(startedBy control.StartedBy, st control.State) []string {
	args := []string{"daemon", "--started-by", string(startedBy)}
	// The default file is not named: a core given --config refuses a file
	// that does not exist yet, and the default one may not.
	if def, err := config.DefaultPath(); st.ConfigPath != "" && (err != nil || !samePath(st.ConfigPath, def)) {
		args = append(args, "--config", st.ConfigPath)
	}
	if st.HTTPSPort > 0 {
		args = append(args, "--https-port", strconv.Itoa(st.HTTPSPort))
	}
	if st.HTTPPort > 0 {
		args = append(args, "--http-port", strconv.Itoa(st.HTTPPort))
	}
	// 0 is no page.
	return append(args, "--web-port", strconv.Itoa(st.WebPort))
}

// spawnDetached starts exe with args as a process of its own: in a session
// (Unix) or detached console-less process (Windows) apart from this one's,
// with the null device for its standard input and output, and in the home
// directory, so that it holds no directory the user may want to remove. Its
// output goes to the core log, which the daemon opens itself; its standard
// error is appended to the log at logPath too, so that what the runtime says
// there, as a panic, which no one else would see, is kept. It is unstamped,
// and both append, so it lands between the core's own lines.
func spawnDetached(exe string, args []string, logPath string) (int, func() error, error) {
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return 0, nil, err
	}
	// The child has its own copies once it has started.
	defer null.Close()
	// Opened as the core opens it, so a log over the limit is set aside
	// now, and the core finds it small: once the child holds it open,
	// Windows would not let it be renamed.
	logFile, err := openCoreLog(logPath, coreLogMayRotate())
	if err != nil {
		return 0, nil, fmt.Errorf("open the core log: %w", err)
	}
	defer logFile.Close()
	dir, _ := os.UserHomeDir()
	c, err := startDetached(func() *exec.Cmd {
		c := exec.Command(exe, args...)
		c.Stdin, c.Stdout, c.Stderr = null, null, logFile
		c.Dir = dir
		return c
	})
	if err != nil {
		return 0, nil, err
	}
	// Waited for at once, whether or not anyone asks, so a child that
	// stops is reaped rather than left a zombie.
	done := make(chan struct{})
	var werr error
	go func() {
		werr = c.Wait()
		close(done)
	}()
	return c.Process.Pid, func() error {
		<-done
		return werr
	}, nil
}

// noteInCoreLog adds line to the core log, stamped as the core's own lines
// are. It is for what happens before the core can say it, and failing to
// write it is not worth failing for.
func noteInCoreLog(line string) {
	path, err := config.LogPath()
	if err != nil {
		return
	}
	// Never set aside: the core may have it open already.
	f, err := openCoreLog(path, false)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(tsWriter(f), line)
}

// tailBytes is how much of the end of the log tailLines reads: enough for
// many more lines than it is asked for.
const tailBytes = 64 << 10

// tailLines returns the last n lines of what the file at path holds from
// offset from on, without their line endings. A file that is missing or has
// nothing past from has none. A file shorter than from has been set aside
// and started afresh since (see openCoreLog), so all of it is read.
func tailLines(path string, from int64, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	if from > fi.Size() {
		from = 0
	}
	off := max(fi.Size()-tailBytes, from)
	data, err := io.ReadAll(io.NewSectionReader(f, off, fi.Size()-off))
	if err != nil {
		return nil
	}
	if off > from {
		// The first line read is most likely the end of a longer one.
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	data = bytes.TrimRight(data, "\r\n")
	if len(data) == 0 {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
}
