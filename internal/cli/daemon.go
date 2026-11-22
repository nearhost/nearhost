package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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
	logFile, err := openCoreLog(path, coreLogMayRotate())
	if err != nil {
		return fmt.Errorf("open the core log: %w", err)
	}
	// Closed before the process ends, so nothing stays open on Windows.
	defer logFile.Close()
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
