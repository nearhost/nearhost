package cli

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/hosts"
	"github.com/nearhost/nearhost/internal/proxy"
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
	// before every line.
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
	// The message can carry text from ssh or the server, and the source a
	// project's name from a remote directory: neither may act on the
	// terminal or start a line of its own.
	line := fmt.Sprintf("[%s] %s", printable(source), printable(msg))
	if !p.stamped {
		line = ev.Time.Format("15:04:05") + " " + line
	}
	fmt.Fprintln(p.out, line)
	if ev.State == "" && ev.Project != "" && strings.HasPrefix(ev.Message, engine.PendingPrefix) {
		// Approving takes the interface, which shows each change and why it
		// needs approval; up only says where to go. The interface cannot
		// start while a core in the background runs.
		if p.keepRunning {
			fmt.Fprintln(p.out, "  run `nearhost stop` and open nearhost to approve them; Nearhost in the background cannot")
		} else {
			fmt.Fprintln(p.out, "  open nearhost to approve them; nearhost up cannot")
		}
	}
	if ev.State != "" && p.advise {
		// A state change reads "state: <state>" or "state: <state>: <reason>".
		reason := strings.TrimPrefix(strings.TrimPrefix(msg, "state: "+string(ev.State)), ": ")
		p.fix(ev.Project, ev.State, reason)
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
		reason := st.Message
		if reason == "" && startErrs[i] != nil {
			reason = startErrs[i].Error()
		}
		line := fmt.Sprintf("%s: %s", printable(name), st.State)
		if reason != "" {
			line += ": " + printable(reason)
		}
		fmt.Fprintln(p.out, line)
		p.fix(name, st.State, reason)
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

// fix prints what to do about a project in the given state, if anything.
func (p *upPrinter) fix(name string, st engine.State, reason string) {
	cfg := p.eng.Config()
	proj, ok := cfg.Project(name)
	if !ok {
		return
	}
	host := ""
	if srv, ok := cfg.Server(proj.Server); ok {
		host = srv.SSHHost
	}
	var fix string
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
		case reason == "" || hosts.IsReadOnlyMessage(reason) || strings.Contains(reason, errNoTerminal.Error()) || strings.Contains(reason, errHostsNeedTerminal.Error()):
		case p.keepRunning:
			fix = "run `nearhost hosts update` in a terminal, and Nearhost starts the project"
		default:
			fix = "run `nearhost up` again and " + hostsApprove + ", or add the project's hosts to " + hosts.DefaultPath + " yourself and pass --no-hosts"
		}
	case engine.StateError:
		fix = "resolve what is named above (a port in use: stop the process holding it), then " + p.restart()
		if transport.IsSocketPathMessage(reason) {
			// Nothing reached the server, and nothing will until the run
			// directory is shorter: ssh by hand would work and fix nothing.
			// XDG_CONFIG_HOME also says where the config is read from, so
			// the folder goes with it.
			fix = "set XDG_CONFIG_HOME to a shorter path and move the nearhost folder that holds your config there, then " + p.restart()
		} else if status, _ := p.eng.ServerStatus(proj.Server); status != transport.StatusConnected {
			fix = sshHint(host, p.restart())
			// A session with a next attempt tries by itself, and the
			// project waits for it and starts once it connects. One that
			// gave up for good (it cannot run at all) has none.
			if !p.eng.ServerHealth(proj.Server).NextAttempt.IsZero() {
				fix = fmt.Sprintf("run `ssh %s` once in a terminal to fix access; nearhost tries again by itself", host)
			}
		}
	}
	if fix != "" {
		fmt.Fprintln(p.out, "  fix: "+upperFirst(printable(fix)))
	}
}
