package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/hosts"
	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/transport"
)

// upPrinter writes what `up` reports. It is used from one goroutine.
type upPrinter struct {
	out io.Writer
	eng *engine.Engine
	cfg *config.Config
	// advise makes a state change come with what to do about it. It is off
	// while the projects start, because the summary that follows says it.
	advise bool
	// shown is the last state line printed for each project.
	shown map[string]string
}

// event prints one engine event as "<time> [<source>] <message>", and under a
// state change that needs attention, what to do about it.
func (p *upPrinter) event(ev engine.Event) {
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
	fmt.Fprintf(p.out, "%s [%s] %s\n", ev.Time.Format("15:04:05"), printable(source), printable(msg))
	if ev.State == "" && ev.Project != "" && strings.HasPrefix(ev.Message, engine.PendingPrefix) {
		// Approving takes the interface, which shows each change and why it
		// needs approval; up only says where to go.
		fmt.Fprintln(p.out, "  open nearhost to approve them; nearhost up cannot")
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
// ends with the line that says up is stopping.
func (p *upPrinter) summary(names []string, startErrs []error) (nothing bool) {
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
	if p.nothingToServe(names) {
		fmt.Fprintln(p.out, "\nNothing can be served, so nearhost up is stopping.")
		return true
	}
	if len(p.eng.Sites()) == 0 {
		fmt.Fprintln(p.out, "\nNothing is being served. Press Ctrl-C to stop.")
		return false
	}
	fmt.Fprintln(p.out, "\nServing until interrupted. Press Ctrl-C to stop.")
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

// fix prints what to do about a project in the given state, if anything.
func (p *upPrinter) fix(name string, st engine.State, reason string) {
	proj, ok := p.cfg.Project(name)
	if !ok {
		return
	}
	host := ""
	if srv, ok := p.cfg.Server(proj.Server); ok {
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
		// prompt can appear, and up has already said what to do instead.
		if reason != "" && !hosts.IsReadOnlyMessage(reason) && !strings.Contains(reason, errNoTerminal.Error()) {
			fix = "run `nearhost up` again and " + hostsApprove + ", or add the project's hosts to " + hosts.DefaultPath + " yourself and pass --no-hosts"
		}
	case engine.StateError:
		fix = "resolve what is named above (a port in use: stop the process holding it), then run `nearhost up` again"
		if transport.IsSocketPathMessage(reason) {
			// Nothing reached the server, and nothing will until the run
			// directory is shorter: ssh by hand would work and fix nothing.
			// XDG_CONFIG_HOME also says where the config is read from, so
			// the folder goes with it.
			fix = "set XDG_CONFIG_HOME to a shorter path and move the nearhost folder that holds your config there, then run `nearhost up` again"
		} else if status, _ := p.eng.ServerStatus(proj.Server); status != transport.StatusConnected {
			fix = sshHint(host, "run `nearhost up` again")
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
