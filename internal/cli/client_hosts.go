package cli

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/hosts"
	"github.com/nearhost/nearhost/internal/report"
)

// A core never asks for the password: it leaves a project whose names the
// hosts file lacks stopped, with errHostsNeedTerminal in its message. The
// interface has a terminal, so it brings the hosts block up to date itself,
// asking for the password in its box as it always has, then tells the core,
// which starts the projects again.
//
// A refusal (a cancelled password box, three wrong passwords, a declined
// Windows prompt) stands as a start's did in the interface's own engine (see
// askOnce): nothing asks again until the user starts the project, and the
// project says why it waits.

// clientHosts is what the client knows of the fixes it has made.
type clientHosts struct {
	// want asks the fixer to look at the projects again.
	want chan struct{}
	// turn lets one fix run at a time, so that one waiting finds what the
	// one before it did.
	turn sync.Mutex

	mu sync.Mutex
	// fixing holds the projects a fix is asking for now.
	fixing map[string]bool
	// refused holds the projects whose fix was refused, with why and when.
	refused map[string]hostsRefusal
}

// hostsRefusal is a refused fix: why, and when it was refused.
type hostsRefusal struct {
	err error
	at  time.Time
}

func newClientHosts() *clientHosts {
	return &clientHosts{
		want:    make(chan struct{}, 1),
		fixing:  map[string]bool{},
		refused: map[string]hostsRefusal{},
	}
}

// hostsChanged asks the fixer to look at the projects again, as the mirror
// has changed.
func (b *clientBackend) hostsChanged() {
	select {
	case b.hosts.want <- struct{}{}:
	default:
	}
}

// hostsFixer fixes the hosts file each time it is asked to, until the
// backend's work ends. Its refusals show in the projects' state.
func (b *clientBackend) hostsFixer() {
	for {
		select {
		case <-b.ctx.Done():
			return
		case <-b.hosts.want:
			_ = b.fixHosts(b.ctx)
		}
	}
}

// waitsOnHosts reports whether the core left the project stopped because the
// hosts file lacks its names.
func (b *clientBackend) waitsOnHosts(name string) bool {
	st := b.mirror.status(name)
	return st.State == engine.StateStopped && strings.Contains(st.Message, errHostsNeedTerminal.Error())
}

// fixHosts brings the hosts block up to date for the projects the core says
// wait on it, asking for the password in the interface, then tells the core.
// It asks once per call, and not for a project whose last fix was refused.
func (b *clientBackend) fixHosts(ctx context.Context) error {
	return b.fixHostsAsked(ctx, time.Time{}, "")
}

// coreNotTold is the error of a hosts file fixed that the core could not be
// told of: the file was written.
type coreNotTold struct{ err error }

func (e coreNotTold) Error() string { return "tell Nearhost the hosts file is fixed: " + e.err.Error() }
func (e coreNotTold) Unwrap() error { return e.err }

// fixHostsAsked is fixHosts for a start of the project started the user
// asked for at asked: it also asks for a project whose fix was refused
// before then, and for started even when it is not enabled. A zero asked is
// no start's. The error is the fix's, the refusal that stands, or a
// coreNotTold.
func (b *clientBackend) fixHostsAsked(ctx context.Context, asked time.Time, started string) error {
	if b.flags.noHosts {
		return nil
	}
	// A start's fix ends with the backend too: once the interface has quit,
	// nothing may ask for the password.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(b.ctx, cancel)()
	h := b.hosts
	h.turn.Lock()
	defer h.turn.Unlock()
	if err := b.ctx.Err(); err != nil {
		return err
	}
	cfg := servedConfig(b.mirror.config(), started, func(name string) bool {
		return b.mirror.status(name).State == engine.StateRunning
	})
	var ask []config.Project
	var standing error
	h.mu.Lock()
	for _, p := range cfg.Projects {
		if !p.Enabled || !b.waitsOnHosts(p.Name) {
			continue
		}
		if r, ok := h.refused[p.Name]; ok && (asked.IsZero() || !asked.After(r.at)) {
			standing = r.err
			continue
		}
		ask = append(ask, p)
	}
	// A refusal is forgotten once its project no longer waits: it runs, or
	// is no longer to be served.
	for name := range h.refused {
		p, ok := cfg.Project(name)
		if !ok || !p.Enabled || b.mirror.status(name).State == engine.StateRunning {
			delete(h.refused, name)
		}
	}
	for _, p := range ask {
		h.fixing[p.Name] = true
	}
	h.mu.Unlock()
	if len(ask) == 0 {
		return standing
	}
	defer func() {
		h.mu.Lock()
		for _, p := range ask {
			delete(h.fixing, p.Name)
		}
		h.mu.Unlock()
	}()
	names := make([]string, len(ask))
	for i, p := range ask {
		names[i] = p.Alias
	}
	// The names `nearhost hosts update` writes, which are those the core
	// checks the block against.
	block := hosts.Wanted(ctx, cfg.HostsFileNames(), b.b.lookup)
	if err := hostsThrough(b.root, hosts.DefaultPath, b.b.applier)(ctx, names, block); err != nil {
		// An update cut short by a quit or a stop is not a refusal.
		if ctx.Err() == nil {
			at := askClock()
			h.mu.Lock()
			for _, p := range ask {
				h.refused[p.Name] = hostsRefusal{err: err, at: at}
			}
			h.mu.Unlock()
			// A start's refusal is its error, which the interface logs; the
			// fixer's is logged here, as the engine's start logged it.
			if asked.IsZero() {
				for _, p := range ask {
					m := hostsNotFixed(p.Name, err)
					b.bus.Publish(engine.Event{Time: at, Project: p.Name, State: engine.StateStopped, Message: m.Title, Report: m})
				}
			}
		}
		return err
	}
	h.mu.Lock()
	for _, p := range ask {
		delete(h.refused, p.Name)
	}
	h.mu.Unlock()
	if err := b.acted(ctx, b.c.HostsChanged(ctx)); err != nil {
		return coreNotTold{err}
	}
	return nil
}

// servedConfig is cfg with the projects that are served marked enabled: the
// one being started, and each running, as a project started by hand is
// served whether or not it is enabled. Their names are then wanted in the
// hosts block as an enabled project's are, and a later fix keeps them. cfg
// is shared, so it is cfg itself when none changes, and a copy otherwise.
func servedConfig(cfg *config.Config, started string, running func(string) bool) *config.Config {
	var out *config.Config
	for i, p := range cfg.Projects {
		if p.Enabled || (p.Name != started && !running(p.Name)) {
			continue
		}
		if out == nil {
			c := *cfg
			c.Projects = slices.Clone(cfg.Projects)
			out = &c
		}
		out.Projects[i].Enabled = true
	}
	if out == nil {
		return cfg
	}
	return out
}

// hostsStatus is st, the core's state of project, as the interface shows it:
// a project waiting on the hosts file is starting while this process asks for
// it, as it was while the interface's own engine asked, and says why it waits
// once that was refused, in place of the core's advice to run a command.
func (b *clientBackend) hostsStatus(project string, st engine.ProjectStatus) engine.ProjectStatus {
	if st.State != engine.StateStopped || !strings.Contains(st.Message, errHostsNeedTerminal.Error()) {
		return st
	}
	h := b.hosts
	h.mu.Lock()
	fixing := h.fixing[project]
	r, refused := h.refused[project]
	h.mu.Unlock()
	switch {
	case fixing:
		st.State, st.Message, st.Problem = engine.StateStarting, "", nil
	case refused:
		st.Message = strings.Replace(st.Message, errHostsNeedTerminal.Error(), engine.HostsCause(r.err), 1)
		// The core's message says to run a command; the refusal says why
		// the project waits, with its own fix.
		problem := hostsNotFixed(project, r.err)
		problem.Title = st.Message
		st.Problem = problem
	}
	return st
}

// hostsNotFixed is the error of a start that waits on a hosts file that was
// not fixed, as the engine words it: engine.HostsProblem.
func hostsNotFixed(project string, err error) *report.Message {
	return engine.HostsProblem(project, err)
}
