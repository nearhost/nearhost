package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/hosts"
)

// coreLogLimit is the size past which the core log is set aside, as
// core.log.1, when the core next opens it.
const coreLogLimit = 5 << 20

// errHostsNeedTerminal is the hosts error of a core with no terminal.
var errHostsNeedTerminal = errors.New("the hosts file needs updating, and Nearhost in the background never asks for sudo. Run `nearhost hosts update` in a terminal.")

func newDaemonCmd(b upBackend) *cobra.Command {
	var flags serveFlags
	var startedBy string
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the core with no interface, logging to a file",
		Long: "Run the core with no interface: serve every enabled project, apply\n" +
			"changes to the config as they are saved, and log to core.log in the\n" +
			"config folder. It never asks for sudo: when the hosts file needs\n" +
			"updating, run `nearhost hosts update` in a terminal. This is what a\n" +
			"service runs.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDaemon(cmd, b, flags, startedBy)
		},
	}
	flags.bind(cmd)
	flags.bindCapture(cmd)
	// What started the core, which `nearhost -d`, bare `nearhost` and a
	// service say. Run by hand, it is the default.
	cmd.Flags().StringVar(&startedBy, "started-by", string(control.StartedForeground), "what started the core: foreground, detached, service or tui")
	cmd.Flags().MarkHidden("started-by")
	return cmd
}

// runDaemon is `nearhost daemon`: the core with no terminal, which logs to
// the core log, keeps running with nothing to serve, applies the config as it
// is saved, and never elevates.
func runDaemon(cmd *cobra.Command, b upBackend, flags serveFlags, startedBy string) error {
	by := control.StartedBy(startedBy)
	switch by {
	case control.StartedForeground, control.StartedDetached, control.StartedService, control.StartedTUI:
	default:
		return errors.New("--started-by must be foreground, detached, service or tui")
	}
	path, err := config.LogPath()
	if err != nil {
		return err
	}
	logFile, err := openDaemonLog(path, by, coreLogMayRotate())
	if err != nil {
		return fmt.Errorf("open the core log: %w", err)
	}
	redirected := false
	if by == control.StartedService && b.redirectStderr != nil {
		// The log is the file a service core's panics should land in. Windows
		// stderr then points at this handle, so it is not closed on return:
		// a panic during exit must still find it valid. The process ends next.
		if err := b.redirectStderr(logFile.f); err != nil {
			fmt.Fprintln(logFile, "could not send the core's standard error to this log:", err)
		} else {
			redirected = true
		}
	}
	if !redirected {
		// Closed before the process ends, so nothing stays open on Windows.
		defer logFile.Close()
	}
	log := tsWriter(logFile)
	// A detached core's own output goes nowhere: what the core says on
	// stderr (a CA it could not move) and why it stopped go in the log,
	// where whoever started it looks.
	cmd.SetErr(log)
	fmt.Fprintf(log, "nearhost %s starting (pid %d, started by %s)\n", Version, os.Getpid(), by)
	err = runCore(cmd, b, flags, nil, coreMode{
		out:         log,
		startedBy:   by,
		keepRunning: true,
		watch:       true,
		stamped:     true,
		// Not wrapped in askOnce: the refusal must not stand once the file
		// has been fixed, which HostsChanged then retries.
		hosts: func(_ context.Context, aliases []string) error {
			_, err := hosts.Ensure(hosts.DefaultPath, aliases, func([]string) error { return errHostsNeedTerminal })
			return err
		},
	})
	if by == control.StartedService && engine.IsAlreadyRunning(err) {
		// The service starts a core at login whether or not one runs. That
		// one serves, so this start has nothing to do, and is no failure
		// for the service manager to report or retry.
		fmt.Fprintln(log, "Another Nearhost is already running, so this service start does nothing.")
		return nil
	}
	if err != nil {
		fmt.Fprintln(log, "nearhost:", printableLines(err.Error()))
	}
	return err
}

// coreLogMayRotate reports whether the core log may be set aside: not while
// a core runs, which keeps writing to the log it has open. A core that its
// instance lock is about to turn away must not move that log under it.
func coreLogMayRotate() bool {
	runDir, err := config.RunDir()
	if err != nil {
		return false
	}
	held, err := engine.InstanceRunning(runDir)
	return !held && err == nil
}

// openDaemonLog opens the core log at path for a core started by by. It is
// set aside at open, if it may be, and from here on while the core runs.
// A service core's log is not renamed at open: on macOS and Linux the
// service manager opened it as the core's stderr, which would go on writing
// to core.log.1, and lose its text when that is next replaced. The
// rotating log copies an oversized one aside at its first write instead.
// Without mayRotate, another core holds the instance lock and this one is
// about to be turned away: its log is never set aside, so the running core's
// log, and its core.log.1, stay as they are.
func openDaemonLog(path string, by control.StartedBy, mayRotate bool) (*rotatingLog, error) {
	if !mayRotate {
		return newRotatingLog(path, math.MaxInt64)
	}
	if by != control.StartedService {
		f, err := openCoreLog(path, true)
		if err != nil {
			return nil, err
		}
		f.Close()
	}
	return newRotatingLog(path, coreLogLimit)
}

// openCoreLog opens the log at path to append to it, creating it and its
// folder as needed. With rotate, a log over coreLogLimit is first renamed to path.1,
// replacing any older one. A log that cannot be renamed, as one another core
// still has open on Windows, is appended to.
func openCoreLog(path string, rotate bool) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if fi, err := os.Stat(path); rotate && err == nil && fi.Size() > coreLogLimit {
		_ = os.Rename(path, path+".1")
	}
	return os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
}

// rotatingLog appends to the core log. Once the file passes limit, it copies
// the file to path+".1", replacing that, and truncates the file to 0, under
// its lock, so no line of the core's is lost. Copy then truncate, not rename,
// so a handle someone else holds (the core's own stderr) stays valid on Windows.
type rotatingLog struct {
	path  string
	limit int64
	f     *os.File

	mu sync.Mutex
	// size is an estimate of the file's size: it counts what is written
	// here, and not what another handle (stderr) writes.
	size int64
	// retryAt is the size at which a failed rotation is tried again, so a
	// log that cannot be set aside is not copied afresh on every write.
	retryAt int64
	// copies counts attempts to copy the log aside, for the tests.
	copies int
}

func newRotatingLog(path string, limit int64) (*rotatingLog, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	l := &rotatingLog{path: path, limit: limit, f: f}
	if fi, err := f.Stat(); err == nil {
		l.size = fi.Size()
	}
	return l, nil
}

func (l *rotatingLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.size > l.limit && l.size >= l.retryAt {
		l.rotate()
	}
	n, err := l.f.Write(p)
	l.size += int64(n)
	return n, err
}

// rotate sets the log aside. The caller holds the lock. A failure leaves the
// log as it is: it keeps growing, and is tried again at the next write that
// finds it over the limit.
func (l *rotatingLog) rotate() {
	// Stderr writes are not counted, so ask the file before acting.
	fi, err := l.f.Stat()
	if err != nil {
		return
	}
	l.size = fi.Size()
	if l.size <= l.limit {
		return
	}
	// Back off after a failure, as with a .1 that something holds open on
	// Windows: try again once the log has grown by a tenth of the limit.
	l.retryAt = l.size + l.limit/10
	l.copies++
	if copyAside(l.path, l.path+".1") != nil {
		return
	}
	// A separate handle truncates: on Windows an append-only handle cannot.
	// Text another handle (stderr) writes between the copy and this
	// truncation is lost. That window is a few microseconds, and closing it
	// would need a lock the other handle's writer does not take.
	t, err := os.OpenFile(l.path, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	t.Close()
	l.size = 0
	l.retryAt = 0
}

// copyAside copies src to dst by writing a temporary file beside dst and
// renaming it over dst, so dst is never half written.
func copyAside(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".tmp*")
	if err != nil {
		return err
	}
	_, err = io.Copy(tmp, in)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), dst)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

func (l *rotatingLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}

// stampedWriter is tsWriter's writer.
type stampedWriter struct {
	mu sync.Mutex
	w  io.Writer
	// midLine is set when the last write did not end its line, so the next
	// one goes on with it, unstamped.
	midLine bool
}

// tsWriter returns a writer that puts an RFC 3339 timestamp before each line
// written to w. Blank lines, which set a summary apart, stay blank. It is
// safe for more than one goroutine, and one Write's lines are written to w
// in one go.
func tsWriter(w io.Writer) io.Writer { return &stampedWriter{w: w} }

func (s *stampedWriter) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stamp := time.Now().Format(time.RFC3339) + " "
	var buf []byte
	for rest := b; len(rest) > 0; {
		line, after, ended := bytes.Cut(rest, []byte("\n"))
		if !s.midLine && len(line) > 0 {
			buf = append(buf, stamp...)
		}
		buf = append(buf, line...)
		if ended {
			buf = append(buf, '\n')
		}
		s.midLine = !ended
		rest = after
	}
	if _, err := s.w.Write(buf); err != nil {
		return 0, err
	}
	return len(b), nil
}
