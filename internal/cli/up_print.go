package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/hosts"
	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/transport"
)

// upPrinter writes what `up` reports. It is used from one goroutine, but
// the core's approval lines go to the same output from another, so each
// event with what follows it, and the summary, is written in one Write (see
// atOnce): a line from elsewhere falls between them, never inside one.
type upPrinter struct {
	out io.Writer
	// eng is read for the config too: a reload can add projects and
	// servers, which the fixes name.
	eng *engine.Engine
	// advise makes a state change come with what to do about it. It is off
	// while the projects start, because the summary that follows says it.
	advise bool
	// shown is the last state line printed for each project.
	shown map[string]string
	// keepRunning is set for a core that keeps going with nothing to
	// serve and that Ctrl-C does not reach, as it runs in the background:
	// the summary then never says it is stopping, nor to press Ctrl-C, and
	// a fix says to run `nearhost stop` and `nearhost -d`, not `nearhost
	// up` again (see restart).
	keepRunning bool
	// stamped leaves the time out of event lines: out puts a full one
	// before every line. It is set for the core log, which also says each
	// problem's level and code, and writes a warning or an error as a
	// message block (see report.LogText).
	stamped bool
}

// event prints one engine event as "<time> [<source>] <message>", or without
// the time when p.stamped, and under a state change that needs attention,
// what to do about it.
func (p *upPrinter) event(ev engine.Event) {
	p.atOnce(func() { p.writeEvent(ev) })
}

// atOnce runs write with p.out held back in a buffer, then writes what it
// wrote in one Write.
func (p *upPrinter) atOnce(write func()) {
	out := p.out
	var buf bytes.Buffer
	p.out = &buf
	defer func() {
		p.out = out
		if buf.Len() > 0 {
			out.Write(buf.Bytes()) // unchecked, as every other line up prints
		}
	}()
	write()
}

func (p *upPrinter) writeEvent(ev engine.Event) {
	source, msg := "proxy", ev.Message
	switch {
	case ev.Project != "":
		source = ev.Project
	case ev.Server != "":
		source = "server " + ev.Server
		msg = strings.TrimPrefix(msg, ev.Server+": ") // the session names itself
	}
	if ev.State == "" {
		msg = proxy.PlainLog(msg)
	} else {
		// The same state comes again when its reason changes (a server that
		// keeps failing differently, a port whose holder was found): each
		// distinct line is printed, a repeated one is not.
		if p.shown == nil {
			p.shown = map[string]string{}
		}
		if p.shown[ev.Project] == msg {
			return
		}
		p.shown[ev.Project] = msg
	}
	pending := ev.State == "" && ev.Project != "" && strings.HasPrefix(ev.Message, engine.PendingPrefix)
	// Approving takes the interface, which shows each change and why it
	// needs approval; up only says where to go. The interface cannot
	// start while a core in the background runs.
	approve := "open nearhost to approve them; nearhost up cannot"
	if p.keepRunning {
		approve = "run `nearhost stop` and open nearhost to approve them; Nearhost in the background cannot"
	}
	if p.stamped && ev.State == "" && ev.Report != nil {
		// A warning or an error said as it happens, as a block with its
		// level, code and what to do.
		m := *ev.Report
		m.Context, m.Fix = source, slices.Clone(m.Fix)
		if pending {
			m.Then(upperFirst(approve) + ".")
		}
		fmt.Fprintln(p.out, report.LogText(&m))
		return
	}
	// The message can carry text from ssh or the server, and the source a
	// project's name from a remote directory: neither may act on the
	// terminal or start a line of its own.
	line := fmt.Sprintf("%s[%s] %s", p.codePrefix(ev.Report), printable(source), printable(msg))
	if !p.stamped {
		line = ev.Time.Format("15:04:05") + " " + line
	}
	fmt.Fprintln(p.out, line)
	if pending {
		fmt.Fprintln(p.out, "  "+approve)
	} else if ev.State == "" && ev.Report != nil {
		// In a terminal, a warning or an error is an event line like any
		// other, which keeps its time, with what to do under it.
		p.writeSteps(ev.Report.Fix)
	}
	if ev.State != "" && p.advise {
		// A state change reads "state: <state>" or "state: <state>: <reason>".
		reason := strings.TrimPrefix(strings.TrimPrefix(msg, "state: "+string(ev.State)), ": ")
		p.writeSteps(p.steps(ev.Project, ev.State, reason, ev.Report))
	}
}

// codePrefix is what goes before a line about problem in the log: its level
// and code, as a message block starts. A line in a terminal, or one about no
// problem, has none.
func (p *upPrinter) codePrefix(problem *report.Message) string {
	if !p.stamped || problem == nil || problem.Code == "" {
		return ""
	}
	return problem.Level.String() + " [" + printable(string(problem.Code)) + "] "
}

// writeSteps writes each step under the line it fixes: "  fix: <text>",
// and its command, if any, on a line of its own, so it can be copied.
func (p *upPrinter) writeSteps(steps []report.Step) {
	for _, s := range steps {
		if text := printableLines(s.Text); text != "" {
			fmt.Fprintln(p.out, "  fix: "+strings.ReplaceAll(text, "\n", "\n       "))
		}
		if c := printable(s.Command); c != "" {
			fmt.Fprintln(p.out, "        "+c)
		}
	}
}

// buffered prints the events that have already arrived.
func (p *upPrinter) buffered(events <-chan engine.Event) {
	for {
		select {
		case ev := <-events:
			p.event(ev)
		default:
			return
		}
	}
}

// summary prints one line per started project with the state it is in now,
// which is not always what its start returned: a project can come up degraded.
// It reports whether nothing can be served (see nothingToServe), and then
// ends with the line that says up is stopping, unless p.keepRunning. A core
// that keeps running is stopped with `nearhost stop`, not Ctrl-C.
func (p *upPrinter) summary(names []string, startErrs []error) (nothing bool) {
	p.atOnce(func() { nothing = p.writeSummary(names, startErrs) })
	return nothing
}

func (p *upPrinter) writeSummary(names []string, startErrs []error) (nothing bool) {
	fmt.Fprintln(p.out)
	for i, name := range names {
		st := p.eng.Status(name)
		if st.State == engine.StateRunning {
			fmt.Fprintf(p.out, "%s: running at https://%s\n", printable(name), printable(st.Alias))
			continue
		}
		reason, problem := st.Message, st.Problem
		if reason == "" && startErrs[i] != nil {
			reason = startErrs[i].Error()
		}
		if problem == nil && startErrs[i] != nil {
			problem, _ = report.As(startErrs[i])
		}
		line := fmt.Sprintf("%s%s: %s", p.codePrefix(problem), printable(name), st.State)
		if reason != "" {
			line += ": " + printable(reason)
		}
		fmt.Fprintln(p.out, line)
		p.writeSteps(p.steps(name, st.State, reason, problem))
	}
	if !p.keepRunning && p.nothingToServe(names) {
		fmt.Fprintln(p.out, "\nNothing can be served, so nearhost up is stopping.")
		return true
	}
	switch {
	case len(p.eng.Sites()) == 0 && p.keepRunning:
		fmt.Fprintln(p.out, "\nNothing is being served.")
	case len(p.eng.Sites()) == 0:
		fmt.Fprintln(p.out, "\nNothing is being served. Press Ctrl-C to stop.")
	case p.keepRunning:
		fmt.Fprintln(p.out, "\nServing until stopped with `nearhost stop`.")
	default:
		fmt.Fprintln(p.out, "\nServing until interrupted. Press Ctrl-C to stop.")
	}
	return false
}

// nothingToServe reports whether up has nothing to serve and nothing to
// wait for: no site is published (a project in error can still serve its
// web routes), and none of names is running or waiting. Projects in error
// or stopped stay so until the user does something.
func (p *upPrinter) nothingToServe(names []string) bool {
	if len(p.eng.Sites()) > 0 {
		return false
	}
	for _, name := range names {
		st := p.eng.Status(name)
		if st.State == engine.StateRunning || waiting(st, p.eng.ServerHealth(st.Waiting)) {
			return false
		}
	}
	return true
}

// waiting reports whether a project with status st will change by itself:
// Nearhost is still starting it, or retrying for it. Starting is a start
// in flight, or one that waits for its server's session to connect;
// degraded is a project whose server's session, or one of its forwards, is
// reconnecting. A start that waits for its server (st.Waiting) also shows
// the error of a server that refused the login, and the session tries that
// login again by itself unless it gave up for good, which health, the
// waited-for server's, tells by a zero NextAttempt. Any state that means
// "Nearhost will retry by itself" must be added here, so that up never
// stops while one may still come to serve.
func waiting(st engine.ProjectStatus, health engine.ServerHealth) bool {
	switch st.State {
	case engine.StateStarting, engine.StateDegraded:
		return true
	}
	if st.Waiting == "" || health.Held {
		return false
	}
	return health.Status != transport.StatusAuthFailed || !health.NextAttempt.IsZero()
}

// restart is how a fix says to start Nearhost again: a core in the
// background is stopped and started in the background, not with up.
func (p *upPrinter) restart() string {
	if p.keepRunning {
		return "run `nearhost stop` and `nearhost -d`"
	}
	return "run `nearhost up` again"
}

// steps is what to do about the project called name in the state st, for
// the reason given, whose problem is problem (nil when none is known), or
// nil when there is nothing to do. Most problems carry their own steps.
// Those about a server's access, the hosts file and the run directory are
// worded here for up and a core in the background, which start again
// differently from the interface the engine words them for.
func (p *upPrinter) steps(name string, st engine.State, reason string, problem *report.Message) []report.Step {
	cfg := p.eng.Config()
	proj, ok := cfg.Project(name)
	if !ok {
		return nil
	}
	host := ""
	if srv, ok := cfg.Server(proj.Server); ok {
		host = srv.SSHHost
	}
	var code report.Code
	if problem != nil {
		code = problem.Code
	}
	fix := ""
	switch st {
	case engine.StateDegraded:
		fix = fmt.Sprintf("none needed while nearhost reconnects; if the project stays degraded, run `ssh %s` once in a terminal to fix access", host)
	case engine.StateStarting:
		// A start that waits for its server runs by itself once it connects.
		// The reason is the event's own, read when it happened: a start
		// that runs as the session connects has none, and needs no hint.
		if reason != "" && p.eng.Status(name).Waiting != "" {
			fix = fmt.Sprintf("none needed: the project starts once nearhost connects to its server; if it keeps waiting, run `ssh %s` once in a terminal to fix access", host)
		}
	case engine.StateStopped:
		// A stopped project with a reason was refused its hosts entry; one
		// without was simply stopped.
		// A read-only hosts file's reason already says how to clear it, and
		// approving again would fail the same way. Without a terminal no
		// prompt can appear, and up has already said what to do instead. A
		// core in the background asks for no prompt: `hosts update`
		// updates the file in a terminal and has the core try again.
		switch {
		case reason == "" || code == report.HostsReadOnly ||
			problem != nil && (errors.Is(problem, errNoTerminal) || errors.Is(problem, errHostsNeedTerminal)):
		case p.keepRunning:
			fix = "run `nearhost hosts update` in a terminal, and Nearhost starts the project"
		default:
			fix = "run `nearhost up` again and " + hostsApprove + ", or add the project's hosts to " + hosts.DefaultPath + " yourself and pass --no-hosts"
		}
	case engine.StateError:
		status, _ := p.eng.ServerStatus(proj.Server)
		switch {
		case code == report.SSHSocketPathTooLong:
			// Nothing reached the server, and nothing will until the run
			// directory is shorter: ssh by hand would work and fix nothing.
			// XDG_CONFIG_HOME also says where the config is read from, so
			// the folder goes with it.
			fix = "set XDG_CONFIG_HOME to a shorter path and move the nearhost folder that holds your config there, then " + p.restart()
		case status != transport.StatusConnected:
			fix = sshHint(host, p.restart())
			// A session with a next attempt tries by itself, and the
			// project waits for it and starts once it connects. One that
			// gave up for good (it cannot run at all) has none.
			if !p.eng.ServerHealth(proj.Server).NextAttempt.IsZero() {
				fix = fmt.Sprintf("run `ssh %s` once in a terminal to fix access; nearhost tries again by itself", host)
			}
		case problem != nil && code != report.Unexpected && len(problem.Fix) > 0:
			return problem.Fix
		default:
			fix = "resolve what is named above (a port in use: stop the process holding it), then " + p.restart()
		}
	}
	if fix == "" {
		return nil
	}
	return []report.Step{{Text: upperFirst(fix)}}
}
