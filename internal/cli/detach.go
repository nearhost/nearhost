package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
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
	// terminal. wait returns once the child has ended, with how it ended,
	// and may be called more than once.
	spawn func(exe string, args []string) (wait func() error, err error)
	// dial reaches the running core, or returns control.ErrNotRunning.
	dial func() (*control.Client, error)
	// logTail returns the last n lines of the core log, or fewer.
	logTail func(n int) []string
	// exe is the path of this program, which the child runs.
	exe func() string
	// timeout is how long to wait for the child to answer; zero is
	// detachTimeout. Tests shorten it.
	timeout time.Duration
}

// systemDetach is the production backend.
func systemDetach() detachBackend {
	return detachBackend{
		spawn: spawnDetached,
		dial: func() (*control.Client, error) {
			dir, err := config.RunDir()
			if err != nil {
				return nil, err
			}
			return control.Dial(dir)
		},
		logTail: func(n int) []string {
			path, err := config.LogPath()
			if err != nil {
				return nil
			}
			return tailLines(path, n)
		},
		exe: func() string {
			if exe, err := os.Executable(); err == nil {
				return exe
			}
			return os.Args[0]
		},
	}
}

// runDetach is `nearhost -d`: it starts `nearhost daemon` detached from the
// terminal and returns once its control socket answers. A core that is
// already running is left be.
func runDetach(cmd *cobra.Command, b detachBackend, flags serveFlags) error {
	if err := flags.validate(); err != nil {
		return err
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	if info, ok := detachInfo(ctx, b); ok {
		fmt.Fprintf(out, "Nearhost is already running in the background (pid %d, started by %s).\n", info.PID, info.StartedBy)
		return nil
	}
	logPath, err := config.LogPath()
	if err != nil {
		return err
	}
	args, err := detachArgs(cmd)
	if err != nil {
		return err
	}
	wait, err := b.spawn(b.exe(), args)
	if err != nil {
		return fmt.Errorf("start Nearhost in the background: %w", err)
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
	for {
		// The socket first: a core that answers and then stops at once
		// was running, and whatever stopped it is not a failure to start.
		if info, ok := detachInfo(ctx, b); ok {
			fmt.Fprintf(out, "Nearhost is running in the background (pid %d). Stop it with nearhost stop.\n", info.PID)
			return nil
		}
		select {
		case werr := <-exited:
			return childStopped(errOut, logPath, b.logTail(detachTailLines), werr)
		default:
		}
		if !time.Now().Before(deadline) {
			msg := "Nearhost is still starting. See " + logPath + " for how it is going."
			fmt.Fprintln(errOut, msg)
			return exitError{code: 1, msg: msg}
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// detachInfo returns what the running core says of itself, and false when
// nothing answers.
func detachInfo(ctx context.Context, b detachBackend) (control.Info, bool) {
	c, err := b.dial()
	if err != nil {
		return control.Info{}, false
	}
	info, err := c.Info(ctx)
	return info, err == nil
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

// detachArgs are the arguments of the core that `nearhost -d` starts: the
// daemon, said to be detached, with --config and the serve flags the user
// gave. Flags not given are left out, so the core's defaults are its own.
func detachArgs(cmd *cobra.Command) ([]string, error) {
	args := []string{"daemon", "--started-by", string(control.StartedDetached)}
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

// spawnDetached starts exe with args as a process of its own: in a session
// (Unix) or detached console-less process (Windows) apart from this one's,
// with the null device for its standard input and output, and in the home
// directory, so that it holds no directory the user may want to remove. Its
// output goes to the core log, which the daemon opens itself.
func spawnDetached(exe string, args []string) (func() error, error) {
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	// The child has its own copies once it has started.
	defer null.Close()
	dir, _ := os.UserHomeDir()
	c, err := startDetached(func() *exec.Cmd {
		c := exec.Command(exe, args...)
		c.Stdin, c.Stdout, c.Stderr = null, null, null
		c.Dir = dir
		return c
	})
	if err != nil {
		return nil, err
	}
	// Waited for at once, whether or not anyone asks, so a child that
	// stops is reaped rather than left a zombie.
	done := make(chan struct{})
	var werr error
	go func() {
		werr = c.Wait()
		close(done)
	}()
	return func() error {
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
	f, err := openCoreLog(path)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(tsWriter(f), line)
}

// tailBytes is how much of the end of the log tailLines reads: enough for
// many more lines than it is asked for.
const tailBytes = 64 << 10

// tailLines returns the last n lines of the file at path, without their
// line endings. A file that is missing or empty has none.
func tailLines(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	off := max(fi.Size()-tailBytes, 0)
	data, err := io.ReadAll(io.NewSectionReader(f, off, fi.Size()-off))
	if err != nil {
		return nil
	}
	if off > 0 {
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
