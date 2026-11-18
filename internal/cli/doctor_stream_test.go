package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
)

// legacyReport is the renderer doctor used before it streamed: it prints
// every check and project at once, once all have finished. The golden test
// holds the streamed output to it.
func legacyReport(out io.Writer, checks []check, projects []projectReport) error {
	failed, warned := tally(checks, projects)
	for _, c := range checks {
		detail := strings.ReplaceAll(strings.TrimRight(printableLines(c.detail), "\n"), "\n", "\n"+strings.Repeat(" ", 23))
		fmt.Fprintf(out, "%-4s  %-16s %s\n", c.level, printable(c.name), detail)
		if c.fix != "" {
			fmt.Fprintf(out, "      fix: %s\n", upperFirst(printable(c.fix)))
		}
	}
	printProjects(out, projects)
	fmt.Fprintln(out)
	switch {
	case failed > 0:
		err := failure(failed)
		fmt.Fprintln(out, err.Error()+".")
		return err
	case warned > 0:
		fmt.Fprintf(out, "No failures, %s.\n", plural(warned, "warning"))
	default:
		fmt.Fprintln(out, "All checks passed.")
	}
	return nil
}

// legacyDoctor runs the checks as doctor did before streaming and renders
// them with legacyReport.
func legacyDoctor(t *testing.T, b doctorBackend, args ...string) (string, error) {
	t.Helper()
	var flags serveFlags
	cmd := &cobra.Command{Use: "doctor", RunE: func(cmd *cobra.Command, _ []string) error {
		quiet := &doctorStream{quiet: true}
		m := runChecks(cmd, b, flags, quiet)
		var projects []projectReport
		if m.cfg != nil && b.openFiles != nil {
			env := b.env
			env.noHosts = env.noHosts || flags.noHosts
			projects = checkProjects(cmd.Context(), b, env, m.cfg, m.cfgPath, m.reachable)
			projects = withoutReported(projects, m.hostsMissing)
		}
		return legacyReport(cmd.OutOrStdout(), m.checks, projects)
	}}
	flags.bind(cmd)
	return execute(t, cmd, append([]string{"doctor"}, args...)...)
}

// The streamed report equals the report printed at the end, byte for byte,
// for a run with failures, a multi-line detail, a fix and a project section.
func TestDoctorStreamedOutputEqualsTheOldReport(t *testing.T) {
	shopAndBlog(t)
	f := healthy()
	f.hosts = shopAndBlogHosts
	f.portErr = map[string]error{"tcp 80": errors.New("address already in use")}
	f.trust.trusted = false
	var opened []string
	b := withProjects(f.backend(), nil, &opened)
	want, wantErr := legacyDoctor(t, b)
	got, gotErr := execute(t, newDoctorCmd(b), "doctor")
	if got != want {
		t.Errorf("streamed output:\n%s\nold report:\n%s", got, want)
	}
	if fmt.Sprint(gotErr) != fmt.Sprint(wantErr) || gotErr == nil {
		t.Errorf("err = %v, old report's = %v", gotErr, wantErr)
	}
	if !strings.Contains(got, "\nProjects\n") || !strings.Contains(got, "fix: ") {
		t.Errorf("the scenario lacks a project section or a fix:\n%s", got)
	}
}

// gatedServer is a doctor backend whose server check blocks until release
// is closed, after saying it started.
type gatedServer struct {
	entered chan struct{}
	release chan struct{}
}

func newGatedServer(f *fakeMachine) (*gatedServer, doctorBackend) {
	g := &gatedServer{entered: make(chan struct{}), release: make(chan struct{})}
	b := f.backend()
	b.checkServer = func(_ context.Context, _ config.Server) error {
		close(g.entered)
		<-g.release
		return nil
	}
	return g, b
}

// runDoctor runs doctor in the background with separate out and err
// buffers and returns a function that waits for it.
func runDoctor(t *testing.T, b doctorBackend, out, errw io.Writer, args ...string) (wait func() error) {
	t.Helper()
	cmd := newDoctorCmd(b)
	root := newRootCmd()
	root.AddCommand(cmd)
	root.SetOut(out)
	root.SetErr(errw)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(append([]string{"doctor"}, args...))
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(context.Background()) }()
	return func() error { return <-done }
}

// Lines print as their checks finish: while the server check is still
// running, everything before it is already written, and nothing after it.
func TestDoctorPrintsEachCheckBeforeALaterOneReturns(t *testing.T) {
	path := doctorConfig(t)
	g, b := newGatedServer(healthy())
	var out, errw syncBuffer
	wait := runDoctor(t, b, &out, &errw)
	<-g.entered
	want := "" +
		"ok    config           " + path + "\n" +
		"ok    port 443         free\n" +
		"ok    port 80          free\n" +
		"ok    port 6327        free\n" +
		"ok    local CA         trusted by " + trustStore + "\n" +
		"ok    browser stores   the NSS stores trust the CA\n" +
		"ok    hosts block      2 names\n"
	if got := out.String(); got != want {
		t.Errorf("before the server check returned, doctor had printed:\n%s\nwant:\n%s", got, want)
	}
	close(g.release)
	if err := wait(); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.HasSuffix(got, "ok    server mini      ssh mini.lan works without a prompt\n\nAll checks passed.\n") {
		t.Errorf("final output:\n%s", got)
	}
}

// signalWriter says on ch when it has been written to.
type signalWriter struct {
	syncBuffer
	ch chan struct{}
	do sync.Once
}

func (w *signalWriter) Write(p []byte) (int, error) {
	n, err := w.syncBuffer.Write(p)
	w.do.Do(func() { close(w.ch) })
	return n, err
}

// A check that runs past the threshold says so once on a terminal's stderr,
// on its own line, and its result still prints on stdout.
func TestDoctorSaysWhichCheckIsSlow(t *testing.T) {
	doctorConfig(t)
	g, b := newGatedServer(healthy())
	b.stderrTTY = func() bool { return true }
	b.slowAfter = time.Millisecond
	var out syncBuffer
	errw := &signalWriter{ch: make(chan struct{})}
	wait := runDoctor(t, b, &out, errw)
	<-g.entered
	<-errw.ch // the notice arrived
	close(g.release)
	if err := wait(); err != nil {
		t.Fatal(err)
	}
	if got, want := errw.String(), "checking server mini…\n"; got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
	if !strings.Contains(out.String(), "ok    server mini ") || strings.Contains(out.String(), "checking") {
		t.Errorf("stdout:\n%s", out.String())
	}
}

// Without a terminal on stderr, and in --json mode, there is no notice.
func TestDoctorSaysNothingWhenStderrIsNotATerminalOrWithJSON(t *testing.T) {
	for name, tc := range map[string]struct {
		tty  bool
		args []string
	}{"no terminal": {false, nil}, "json": {true, []string{"--json"}}} {
		t.Run(name, func(t *testing.T) {
			doctorConfig(t)
			f := healthy()
			b := f.backend()
			b.stderrTTY = func() bool { return tc.tty }
			b.slowAfter = time.Millisecond
			b.checkServer = func(context.Context, config.Server) error {
				time.Sleep(30 * time.Millisecond) // long past the threshold
				return nil
			}
			var out, errw syncBuffer
			if err := runDoctor(t, b, &out, &errw, tc.args...)(); err != nil {
				t.Fatal(err)
			}
			if errw.String() != "" {
				t.Errorf("stderr = %q, want nothing", errw.String())
			}
		})
	}
}

// --json prints one document at the end, and nothing before it.
func TestDoctorJSONPrintsOnceAtTheEnd(t *testing.T) {
	doctorConfig(t)
	g, b := newGatedServer(healthy())
	var out, errw syncBuffer
	wait := runDoctor(t, b, &out, &errw, "--json")
	<-g.entered
	if got := out.String(); got != "" {
		t.Errorf("--json printed %q before the checks finished", got)
	}
	close(g.release)
	if err := wait(); err != nil {
		t.Fatal(err)
	}
	var r jsonReport
	if err := json.Unmarshal([]byte(out.String()), &r); err != nil || len(r.Checks) != 8 {
		t.Errorf("--json printed %v checks, err %v:\n%s", len(r.Checks), err, out.String())
	}
}

// The notice never lands after the check returned.
func TestDoctorStreamStopsTheNoticeWhenTheCheckReturns(t *testing.T) {
	var errw syncBuffer
	s := &doctorStream{out: io.Discard, errw: &errw, tty: true, after: 20 * time.Millisecond}
	s.slow("quick", func() check { return check{name: "quick"} })
	time.Sleep(60 * time.Millisecond) // long enough for a leaked timer to fire
	if errw.String() != "" {
		t.Errorf("stderr = %q after the check had returned", errw.String())
	}
}
