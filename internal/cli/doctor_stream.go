package cli

import (
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/doctor"
)

// slowCheckAfter is how long a check runs before doctor says it is still
// working on it.
const slowCheckAfter = 2 * time.Second

// doctorStream prints doctor's text report line by line as the checks
// finish, and tells a user on a terminal which check is slow. The checks run
// one after another, so lines already come in report order. The report's
// columns have fixed widths, so no line depends on a later check.
type doctorStream struct {
	// mu serializes writes to both streams, so a notice never lands inside
	// a result line, and guards the fields below.
	mu    sync.Mutex
	out   io.Writer
	errw  io.Writer
	quiet bool // --json: print nothing until the end
	tty   bool // stderr is a terminal
	after time.Duration
	// projects is set once the project section's heading is printed.
	projects bool
}

func newDoctorStream(cmd *cobra.Command, b doctorBackend, quiet bool) *doctorStream {
	s := &doctorStream{out: cmd.OutOrStdout(), errw: cmd.ErrOrStderr(), quiet: quiet, after: b.slowAfter}
	if s.after <= 0 {
		s.after = slowCheckAfter
	}
	s.tty = b.stderrTTY != nil && b.stderrTTY()
	return s
}

// printChecks prints the checks' lines now.
func (s *doctorStream) printChecks(cs ...check) {
	if s.quiet {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range cs {
		printCheck(s.out, c)
	}
}

// watch starts the slow-check notice for name and returns what stops it.
// After stop returns, the notice is never written.
func (s *doctorStream) watch(name string) (stop func()) {
	if s.quiet || !s.tty {
		return func() {}
	}
	done := false
	t := time.AfterFunc(s.after, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !done {
			fmt.Fprintf(s.errw, "checking %s…\n", printable(name))
		}
	})
	return func() {
		s.mu.Lock()
		done = true
		s.mu.Unlock()
		t.Stop()
	}
}

// slow runs f, which makes the check called name, and says so on stderr if
// it takes long.
func (s *doctorStream) slow(name string, f func() check) check {
	defer s.watch(name)()
	return f()
}

// slowMany is slow for a function that makes several checks, which it then
// hands to add.
func (s *doctorStream) slowMany(name string, f func() []check, add func(...check)) {
	add(s.watched(name, f)...)
}

// watched runs f under the notice and returns its checks.
func (s *doctorStream) watched(name string, f func() []check) []check {
	defer s.watch(name)()
	return f()
}

// projectHooks prints the project section as each project's report is
// ready. It applies the filters the final report applies, so the lines
// match it: the first hosts.dns-unchecked finding stays and the names the
// hosts block check reported leave hosts.unresolved findings out.
func (s *doctorStream) projectHooks(hostsMissing []string) projectHooks {
	if s.quiet {
		return projectHooks{}
	}
	seen := false
	return projectHooks{
		begin: func(name string) func() { return s.watch(name) },
		emit: func(r projectReport) {
			r.findings = slices.DeleteFunc(slices.Clone(r.findings), func(f doctor.Finding) bool {
				drop := f.ID == "hosts.dns-unchecked" && seen
				seen = seen || f.ID == "hosts.dns-unchecked"
				return drop
			})
			r = withoutReported([]projectReport{r}, hostsMissing)[0]
			s.mu.Lock()
			defer s.mu.Unlock()
			if !s.projects {
				s.projects = true
				printProjectsHeading(s.out)
			}
			printProject(s.out, r)
		},
	}
}

// summary prints the closing lines and returns the error doctor exits with.
func (s *doctorStream) summary(checks []check, projects []projectReport) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return printSummary(s.out, checks, projects)
}
