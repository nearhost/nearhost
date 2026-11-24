package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/service"
)

// statusNotRunning is what `nearhost status` says when no core answers.
const statusNotRunning = "Nearhost is not running. Start it with nearhost -d, or open nearhost."

// statusNotRunningCode is status's exit code when no core answers, as a
// service manager's status command gives for a service that is not running.
const statusNotRunningCode = 3

// newStatusCmd is `nearhost status`. manager gives the service manager, which
// it asks only when no core runs; nil, or one that returns nil, is none.
func newStatusCmd(manager func() service.Manager) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show what the background Nearhost is serving",
		Long: "Show the running Nearhost, as started by `nearhost -d`: its version, what\n" +
			"started it, and the state of each server and project it serves. With\n" +
			"--json it prints the same as JSON. It exits 3 when Nearhost is not running.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(cmd, asJSON, manager)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the status as JSON")
	return cmd
}

// runStatus is `nearhost status`.
func runStatus(cmd *cobra.Command, asJSON bool, manager func() service.Manager) error {
	runDir, err := config.RunDir()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	var st control.Status
	c, err := control.Dial(runDir)
	if err == nil {
		st, err = c.Status(ctx)
	}
	if errors.Is(err, control.ErrNotRunning) {
		// JSON output stays JSON, or nothing: the message goes to stderr.
		w := cmd.OutOrStdout()
		if asJSON {
			w = cmd.ErrOrStderr()
		}
		fmt.Fprintln(w, statusNotRunning)
		if serviceInstalled(ctx, manager) {
			fmt.Fprintln(w, "A service is installed: start it with nearhost -d.")
		}
		return exitError{code: statusNotRunningCode, msg: statusNotRunning}
	}
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if asJSON {
		b, err := json.MarshalIndent(st, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "%s\n", b)
		return err
	}
	// The key is the core's: the one its page checks, wherever this
	// process would look for it. A pipe or a log keeps no secret.
	page := ""
	if st.Page != "" && stdoutIsTerminal() {
		if keyed, err := c.PageKeyURL(ctx); err == nil {
			page = keyed
		}
	}
	return printStatus(out, st, page)
}

// serviceInstalled reports whether a service is installed. A manager that
// cannot say adds nothing to what status tells.
func serviceInstalled(ctx context.Context, manager func() service.Manager) bool {
	if manager == nil {
		return false
	}
	m := manager()
	if m == nil {
		return false
	}
	// A manager may start a program to answer, as Windows does: it is not
	// waited for long.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, ok, err := m.Installed(ctx)
	return ok && err == nil
}

// stdoutIsTerminal reports whether status writes to a terminal, where the
// page's key may be shown. Tests replace it.
var stdoutIsTerminal = func() bool { return term.IsTerminal(int(os.Stdout.Fd())) }

// printStatus writes st as a line about the core, then a table of its
// servers and one of its projects, aligned as `ls` aligns its own, then what
// waits for an answer and the page's address: keyed, the address with the
// page's key, when it is set, and otherwise the address without it.
func printStatus(out io.Writer, st control.Status, keyed string) error {
	// The first write that fails is the error, as for a closed pipe.
	w := &stickyWriter{w: out}
	info := st.Info
	fmt.Fprintf(w, "Nearhost %s is running (pid %d, started by %s at %s).\n",
		printable(info.Version), info.PID, printable(string(info.StartedBy)), info.Started.Local().Format("2006-01-02 15:04:05"))
	if st.Update != "" {
		fmt.Fprintf(w, "Nearhost %s is available. Run nearhost update.\n", printable(st.Update))
	}
	if !info.Reloads {
		fmt.Fprintln(w, "It serves the projects it was started with and does not reload the config.")
	}
	// What a server or project says can come from ssh or a remote
	// directory: it may not act on the terminal or start a line.
	if len(st.Servers) > 0 {
		rows := [][]string{{"SERVER", "STATE", "MESSAGE"}}
		for _, s := range st.Servers {
			rows = append(rows, []string{printable(s.Name), printable(s.State), printable(s.Message)})
		}
		fmt.Fprintln(w)
		if err := writeTable(w, rows); err != nil {
			return err
		}
	}
	fmt.Fprintln(w)
	if len(st.Projects) == 0 {
		fmt.Fprintln(w, "No projects are being served. Enable one in the config, or add one with `nearhost add <path>`.")
	} else {
		rows := [][]string{{"PROJECT", "STATE", "URL", "MESSAGE"}}
		for _, p := range st.Projects {
			rows = append(rows, []string{printable(p.Name), printable(p.State), printable(p.URL), printable(p.Message)})
		}
		if err := writeTable(w, rows); err != nil {
			return err
		}
	}
	if len(st.Approvals) > 0 {
		fmt.Fprintf(w, "\nWaiting for your answer:\n")
		for _, a := range st.Approvals {
			if a.URL == "" {
				fmt.Fprintf(w, "  %s\n", printable(a.Summary))
			} else {
				fmt.Fprintf(w, "  %s: %s\n", printable(a.Summary), printable(a.URL))
			}
		}
	}
	if st.Page != "" {
		page := st.Page
		if keyed != "" {
			page = keyed
		}
		fmt.Fprintf(w, "\nPage: %s\n", printable(page))
	}
	return w.err
}

// stickyWriter writes to w until a write fails, then keeps that error and
// writes nothing more.
type stickyWriter struct {
	w   io.Writer
	err error
}

func (s *stickyWriter) Write(b []byte) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	n, err := s.w.Write(b)
	s.err = err
	return n, err
}

// writeTable writes rows in aligned columns. Every cell ends with a tab, so
// each column is aligned down the whole table even where a row's last cells
// are empty, and the padding that leaves at the end of a line is trimmed.
func writeTable(w io.Writer, rows [][]string) error {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	for _, row := range rows {
		fmt.Fprintln(tw, strings.Join(row, "\t")+"\t")
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, line := range strings.SplitAfter(buf.String(), "\n") {
		if line == "" {
			continue
		}
		if _, err := io.WriteString(w, strings.TrimRight(line, " \n")+"\n"); err != nil {
			return err
		}
	}
	return nil
}
