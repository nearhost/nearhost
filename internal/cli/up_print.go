package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
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
	fmt.Fprintf(p.out, "%s [%s] %s\n", ev.Time.Format("15:04:05"), source, msg)
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
func (p *upPrinter) summary(names []string, startErrs []error) {
	fmt.Fprintln(p.out)
	for i, name := range names {
		st := p.eng.Status(name)
		if st.State == engine.StateRunning {
			fmt.Fprintf(p.out, "%s: running at https://%s\n", name, st.Alias)
			continue
		}
		reason := st.Message
		if reason == "" && startErrs[i] != nil {
			reason = startErrs[i].Error()
		}
		line := fmt.Sprintf("%s: %s", name, st.State)
		if reason != "" {
			line += ": " + reason
		}
		fmt.Fprintln(p.out, line)
		p.fix(name, st.State, reason)
	}
	if len(p.eng.Sites()) == 0 {
		fmt.Fprintln(p.out, "\nNothing is being served. Press Ctrl-C to stop.")
		return
	}
	fmt.Fprintln(p.out, "\nServing until interrupted. Press Ctrl-C to stop.")
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
	case engine.StateStopped:
		// A stopped project with a reason was refused its hosts entry; one
		// without was simply stopped.
		if reason != "" {
			fix = "run `nearhost up` again and approve the password prompt, or add the project's hosts to /etc/hosts yourself and pass --no-hosts"
		}
	case engine.StateError:
		fix = "resolve what is named above (a port in use: stop the process holding it), then run `nearhost up` again"
		if status, _ := p.eng.ServerStatus(proj.Server); status != transport.StatusConnected {
			fix = sshHint(host, "run `nearhost up` again")
		}
	}
	if fix != "" {
		fmt.Fprintln(p.out, "  fix: "+fix)
	}
}
