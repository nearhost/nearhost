package cli

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nearhost/nearhost/internal/agentapi"
	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/documents"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/grants"
	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/transport"
	"github.com/nearhost/nearhost/internal/tui"
)

// redialAfter is how long the backend waits to follow the core's stream
// again once it breaks. The stream is followed again once; if that fails,
// the core is lost.
const redialAfter = 500 * time.Millisecond

// refreshEvery is how often the backend fetches the state while nothing else
// has, for the changes that publish no envelope, such as a passthrough's age
// or a document opened.
const refreshEvery = time.Second

// missedNote is the log line said when the stream skipped envelopes.
const missedNote = "Some log lines were missed."

// keptExchanges is how many answers about captured exchanges the backend
// keeps once it has fetched them: the exchanges, and the IDs the core no
// longer keeps.
const keptExchanges = 32

// capturedRetry is how long the backend waits before it asks the core again
// for an exchange it could not ask for. The interface asks on every redraw.
const capturedRetry = 3 * time.Second

// clientBackend is the interface's backend over the control API of a core
// running in another process. It mirrors the core's state, which every read
// answers from, and sends the actions to the core; what touches the terminal
// or this machine runs here, in localOps, the hosts file among it.
type clientBackend struct {
	*localOps
	c      *control.Client
	mirror *mirror
	bus    *engine.Bus    // the interface subscribes here
	props  *tui.Proposals // approvals from the stream
	lost   chan error     // gets one error when the core cannot be reached
	// started is set when this process started the core.
	started bool
	// gone, when set, waits until the core has stopped and let go of the
	// instance lock, as `nearhost stop` waits. nil waits for nothing.
	gone func(ctx context.Context) error
	// from is the number of the state the backend was made with, which
	// Start follows the stream from.
	from uint64

	// ctx ends when Close is called or the core is lost, which ends the
	// goroutines; work counts them. Start begins them, once: running is set
	// then, under lostMu, unless Close has been called.
	ctx     context.Context
	cancel  context.CancelFunc
	work    sync.WaitGroup
	running bool
	// want asks for the state to be fetched.
	want chan struct{}

	// fetchMu lets one fetch of the state run at a time. lastFetch is when
	// the last one ended.
	fetchMu   sync.Mutex
	lastFetch time.Time

	// queueMu guards queue, the events waiting for a state as new as they
	// are, oldest first. They are published under it, so in order.
	queueMu sync.Mutex
	queue   []control.Envelope

	// given holds the approvals given to the interface, so that none is
	// given twice. Only the constructor and the stream's goroutine use it.
	given map[string]bool

	// lostMu orders Close and the send on lost, so nothing is sent once
	// Close has been called.
	lostMu sync.Mutex
	closed bool

	capturedMu sync.Mutex
	captured   map[string]capturedAnswer
	capturedIn []string // the IDs in captured, oldest first

	// hosts fixes the hosts file for the projects the core leaves waiting
	// on it (see client_hosts.go).
	hosts *clientHosts
}

// capturedAnswer is what the core said about a captured exchange: the
// exchange, or that it is not kept. When the core could not be asked,
// retry is when to ask again.
type capturedAnswer struct {
	ex    proxy.Exchange
	kept  bool
	retry time.Time
}

var _ tui.Backend = (*clientBackend)(nil)

// newClientBackend returns a backend that mirrors the core c talks to,
// starting from st, and gives the approvals waiting to props. Once Start is
// called, it follows the core's stream until Close is called or the core is
// lost. local runs what stays in this process; the hosts state it checks is
// the mirror's config. started says this process started the core.
func newClientBackend(c *control.Client, st control.State, local *localOps, props *tui.Proposals, started bool) *clientBackend {
	ctx, cancel := context.WithCancel(context.Background())
	b := &clientBackend{
		localOps: local,
		c:        c,
		mirror:   newMirror(st),
		bus:      engine.NewBus(),
		props:    props,
		lost:     make(chan error, 1),
		started:  started,
		from:     st.Seq,
		ctx:      ctx,
		cancel:   cancel,
		want:     make(chan struct{}, 1),
		given:    map[string]bool{},
		captured: map[string]capturedAnswer{},
		hosts:    newClientHosts(),
	}
	local.running = b.mirror.config
	b.give(st.Approvals)
	return b
}

// Start follows the core's stream from the state the backend was made with,
// and fixes the hosts file for the projects waiting on it. The interface
// calls it once it has subscribed to Events, so it misses no event. Only the
// first call does anything, and none once Close has been called.
func (b *clientBackend) Start(context.Context) error {
	b.lostMu.Lock()
	defer b.lostMu.Unlock()
	if b.running || b.closed {
		return nil
	}
	b.running = true
	b.work.Add(3)
	go func() { defer b.work.Done(); b.follow(b.from) }()
	go func() { defer b.work.Done(); b.refresher() }()
	go func() { defer b.work.Done(); b.hostsFixer() }()
	b.hostsChanged()
	return nil
}

// Close stops following the core and returns once the backend's goroutines
// have ended. It leaves the core running, and may be called more than once.
func (b *clientBackend) Close() {
	b.lostMu.Lock()
	b.closed = true
	b.lostMu.Unlock()
	b.cancel()
	b.work.Wait()
}

// Lost gets one error when the core can no longer be reached: its stream
// broke and following it again failed.
func (b *clientBackend) Lost() <-chan error { return b.lost }

// follow reads the core's stream from the envelope after after. When the
// stream breaks it is followed again once, after redialAfter, from the last
// envelope taken; if that fails too, the error goes on lost.
func (b *clientBackend) follow(after uint64) {
	// floor is the number of the last state taken in place of the stream:
	// the envelopes up to it are in that state.
	floor := after
	redialed := false
	for {
		err := b.stream(&after, &floor, func() { redialed = false })
		if b.ctx.Err() != nil {
			return
		}
		if redialed {
			b.lostMu.Lock()
			if !b.closed {
				b.lost <- err
			}
			b.lostMu.Unlock()
			b.cancel()
			return
		}
		redialed = true
		select {
		case <-time.After(redialAfter):
		case <-b.ctx.Done():
			return
		}
	}
}

// stream follows one stream of the core's from after until it breaks, and
// returns why. connected is called once the stream answers.
func (b *clientBackend) stream(after, floor *uint64, connected func()) error {
	ctx, cancel := context.WithCancel(b.ctx)
	defer cancel()
	envs, why, err := b.c.Events(ctx, *after)
	if err != nil {
		return err
	}
	connected()
	for e := range envs {
		switch {
		case e.Kind == control.KindReset:
			st, err := b.c.State(ctx)
			if err != nil {
				// Followed again from where it was, the stream gives
				// what was missed or another reset.
				cancel()
				why()
				return err
			}
			b.reset(st)
			*after, *floor = e.Seq, st.Seq
		case e.Seq <= *floor:
			*after = max(*after, e.Seq)
		default:
			b.take(e)
			*after = e.Seq
		}
	}
	if err := why(); err != nil {
		return err
	}
	return errors.New("the core's event stream ended")
}

// reset takes st in place of the envelopes the stream skipped: the events
// waiting go out, as they came before the gap, the approvals in st not given
// yet are given, and the log says lines were missed.
func (b *clientBackend) reset(st control.State) {
	b.fetchMu.Lock()
	b.mirror.replace(st)
	b.lastFetch = time.Now()
	b.flush(true)
	b.fetchMu.Unlock()
	b.give(st.Approvals)
	b.bus.Publish(engine.Event{Message: missedNote})
	b.hostsChanged()
}

// take does what the envelope e says.
func (b *clientBackend) take(e control.Envelope) {
	r := reviewer{b.props}
	switch e.Kind {
	case control.KindEvent:
		if e.Event == nil {
			return
		}
		b.queueMu.Lock()
		b.queue = append(b.queue, e)
		b.queueMu.Unlock()
		if b.flush(false) {
			b.ask()
		}
	case control.KindApproval:
		if e.Approval != nil {
			b.give([]control.Approval{*e.Approval})
		}
	case control.KindOpened:
		if e.Opened != nil {
			r.Opened(*e.Opened)
		}
	case control.KindShown:
		if e.Shown != nil {
			r.Shown(e.Shown.Origin, e.Shown.Requester, e.Shown.Doc)
		}
	case control.KindEnded:
		// The interface has no way yet to take back a request it shows: one
		// that has ended says so when it is answered. The ID is forgotten,
		// as nothing will give it again; an unknown one needs nothing.
		delete(b.given, e.ID)
	}
}

// give gives the interface each approval in as not given before.
func (b *clientBackend) give(as []control.Approval) {
	r := reviewer{b.props}
	for _, a := range as {
		if b.given[a.ID] {
			continue
		}
		switch {
		case a.Proposal != nil:
			r.Review(*a.Proposal)
		case a.Pass != nil:
			r.ReviewPassthrough(*a.Pass)
		case a.Doc != nil:
			r.ReviewDocument(*a.Doc)
		default:
			continue
		}
		b.given[a.ID] = true
	}
}

// flush publishes the events waiting that the mirror's state is as new as,
// or all of them when all is set, and reports whether any still wait.
func (b *clientBackend) flush(all bool) bool {
	b.queueMu.Lock()
	defer b.queueMu.Unlock()
	seq := b.mirror.seq()
	n := 0
	for n < len(b.queue) && (all || b.queue[n].Seq <= seq) {
		b.bus.Publish(*b.queue[n].Event)
		n++
	}
	b.queue = append(b.queue[:0], b.queue[n:]...)
	return len(b.queue) > 0
}

// ask asks for the state to be fetched. Asks made while a fetch runs are
// answered by the next.
func (b *clientBackend) ask() {
	select {
	case b.want <- struct{}{}:
	default:
	}
}

// refresher fetches the state when asked, and every refreshEvery when no
// other fetch has.
func (b *clientBackend) refresher() {
	tick := time.NewTicker(refreshEvery)
	defer tick.Stop()
	for {
		select {
		case <-b.ctx.Done():
			return
		case <-b.want:
		case <-tick.C:
			b.fetchMu.Lock()
			recent := time.Since(b.lastFetch) < refreshEvery/2
			b.fetchMu.Unlock()
			if recent {
				continue
			}
		}
		b.refresh(b.ctx)
	}
}

// refresh fetches the state and takes it, then publishes the events it is
// as new as. A failed fetch leaves the mirror as it was: a core that has gone
// breaks the stream too, which says so.
func (b *clientBackend) refresh(ctx context.Context) {
	b.fetchMu.Lock()
	defer b.fetchMu.Unlock()
	st, err := b.c.State(ctx)
	b.lastFetch = time.Now()
	if err != nil {
		return
	}
	b.mirror.take(st)
	if b.flush(false) {
		b.ask()
	}
	b.hostsChanged()
}

// acted returns err, the error of an action made with ctx, once the state
// is fetched again with ctx: what the action changed shows when it returns,
// even when it publishes no event.
func (b *clientBackend) acted(ctx context.Context, err error) error {
	b.refresh(ctx)
	return err
}

// logged publishes err as a log line, for an action the interface wants no
// error from.
func (b *clientBackend) logged(err error) {
	if err = b.acted(context.Background(), err); err != nil {
		b.bus.Publish(engine.Event{Message: err.Error()})
	}
}

// The reads answer from the mirror.

func (b *clientBackend) Config() *config.Config { return b.mirror.config() }
func (b *clientBackend) ConfigPath() string     { return b.mirror.configPath() }

// Status is the project's state as the core gives it, but for a project
// waiting on the hosts file, which this process fixes: see hostsStatus.
func (b *clientBackend) Status(project string) engine.ProjectStatus {
	return b.hostsStatus(project, b.mirror.status(project))
}
func (b *clientBackend) ServerStatus(server string) (transport.Status, string) {
	s := b.mirror.server(server)
	return s.Status, s.Message
}
func (b *clientBackend) ServerHealth(server string) engine.ServerHealth {
	return b.mirror.server(server).Health
}
func (b *clientBackend) Events() *engine.Bus { return b.bus }
func (b *clientBackend) Ports() (https, http int) {
	https, http, _ = b.mirror.ports()
	return https, http
}
func (b *clientBackend) WebPort() int {
	_, _, web := b.mirror.ports()
	return web
}
func (b *clientBackend) Passthroughs(server string) []engine.Passthrough {
	return b.mirror.passthroughs(server)
}
func (b *clientBackend) Documents(server string) []documents.Tree {
	return b.mirror.documents(server)
}
func (b *clientBackend) Grants(server string) ([]grants.Grant, error) {
	return b.mirror.grants(server)
}

// Shutdown asks the core to stop, stops following it, and returns once the
// core has stopped and let go of the instance lock, as `nearhost stop` waits:
// a Nearhost started at once after finds the ports free. ctx ending cuts the
// wait short. A core that has already gone has done what was asked.
func (b *clientBackend) Shutdown(ctx context.Context) error {
	err := b.c.Stop(ctx)
	b.Close()
	if err != nil && !errors.Is(err, control.ErrNotRunning) {
		return err
	}
	if b.gone == nil {
		return nil
	}
	return b.gone(ctx)
}

// Leave stops following the core, which keeps running.
func (b *clientBackend) Leave(context.Context) error {
	b.Close()
	return nil
}

// QuitChoice asks whether quitting stops the core only when this process
// started it and is its one client: the choice saved as keep_running
// answers for the user, and a core started elsewhere, or that another client
// follows, is left running.
func (b *clientBackend) QuitChoice() tui.QuitChoice {
	if !b.started || b.mirror.clients() >= 2 {
		return tui.QuitLeave
	}
	switch keep := b.mirror.config().KeepRunning; {
	case keep == nil:
		return tui.QuitAsk
	case *keep:
		return tui.QuitLeave
	}
	return tui.QuitStop
}

// Reload has the core load its config file and apply it.
func (b *clientBackend) Reload(ctx context.Context) error { return b.acted(ctx, b.c.Reload(ctx)) }

// StartProject has the core start the project. When the core leaves it
// waiting on the hosts file, the hosts file is fixed here, asking for the
// password again even after a refusal: the user asked for this start.
func (b *clientBackend) StartProject(ctx context.Context, name string) error {
	asked := askClock()
	err := b.acted(ctx, b.c.StartProject(ctx, name))
	if !b.waitsOnHosts(name) {
		return err
	}
	if err := b.fixHostsAsked(ctx, asked); err != nil {
		return hostsNotFixed(name, err)
	}
	// The core retries the projects that waited once told the file is fixed;
	// this start waits for its own.
	return b.acted(ctx, b.c.StartProject(ctx, name))
}

func (b *clientBackend) StopProject(ctx context.Context, name string) error {
	return b.acted(ctx, b.c.StopProject(ctx, name))
}

func (b *clientBackend) Recheck(ctx context.Context, project string) error {
	return b.acted(ctx, b.c.Recheck(ctx, project))
}

func (b *clientBackend) AcceptChange(project, sha string) error {
	return b.acted(context.Background(), b.c.AcceptChange(context.Background(), project, sha))
}

func (b *clientBackend) DeclineChange(project, sha string) {
	b.logged(b.c.DeclineChange(context.Background(), project, sha))
}

func (b *clientBackend) ConnectServer(ctx context.Context, name string) error {
	return b.acted(ctx, b.c.ConnectServer(ctx, name))
}

func (b *clientBackend) DisconnectServer(ctx context.Context, name string) error {
	return b.acted(ctx, b.c.DisconnectServer(ctx, name))
}

func (b *clientBackend) ReconnectServer(ctx context.Context, name string) error {
	return b.acted(ctx, b.c.ReconnectServer(ctx, name))
}

func (b *clientBackend) AcceptProposal(id, name string) {
	b.logged(b.c.AcceptProposal(context.Background(), id, name))
}

func (b *clientBackend) DeclineProposal(id, reason string) {
	b.logged(b.c.DeclineProposal(context.Background(), id, reason))
}

func (b *clientBackend) ClosePassthrough(ctx context.Context, id string) error {
	return b.acted(ctx, b.c.ClosePassthrough(ctx, id))
}

func (b *clientBackend) AnswerPassthrough(ctx context.Context, id string, a tui.Answer) error {
	return b.acted(ctx, tuiAnswerError(b.c.AnswerPassthrough(ctx, id, answerWord(a))))
}

func (b *clientBackend) AnswerDocument(ctx context.Context, id string, a tui.Answer) error {
	return b.acted(ctx, tuiAnswerError(b.c.AnswerDocument(ctx, id, answerWord(a))))
}

func (b *clientBackend) OpenDocument(id string) error {
	return b.acted(context.Background(), b.c.OpenDocument(context.Background(), id))
}

func (b *clientBackend) RemoveDocuments(id string) error {
	return b.acted(context.Background(), b.c.RemoveDocuments(context.Background(), id))
}

func (b *clientBackend) RevokeGrant(server, requester, kind string) error {
	return b.acted(context.Background(), b.c.RevokeGrant(context.Background(), server, requester, kind))
}

// Captured fetches the exchange kept under id from the core, once: the
// backend keeps its answer, the exchange or that it is not kept, for the
// last keptExchanges IDs it has asked about. When the core cannot be asked,
// the answer is that it is not kept, and the core is not asked about id
// again for capturedRetry. The fetch is made in the call: the core is on
// this machine, and answers at once.
func (b *clientBackend) Captured(id string) (proxy.Exchange, bool) {
	b.capturedMu.Lock()
	a, ok := b.captured[id]
	b.capturedMu.Unlock()
	if ok && (a.retry.IsZero() || time.Now().Before(a.retry)) {
		return a.ex, a.kept
	}
	ex, err := b.c.CapturedExchange(context.Background(), id)
	switch {
	case err == nil:
		a = capturedAnswer{ex: ex, kept: true}
	case errors.Is(err, control.ErrNotFound):
		a = capturedAnswer{}
	default:
		a = capturedAnswer{retry: time.Now().Add(capturedRetry)}
	}
	b.capturedMu.Lock()
	defer b.capturedMu.Unlock()
	if _, had := b.captured[id]; !had {
		b.capturedIn = append(b.capturedIn, id)
		if len(b.capturedIn) > keptExchanges {
			delete(b.captured, b.capturedIn[0])
			b.capturedIn = b.capturedIn[1:]
		}
	}
	b.captured[id] = a
	return a.ex, a.kept
}

func (b *clientBackend) Replay(ctx context.Context, id string) (int, error) {
	return b.c.Replay(ctx, id)
}

// OpenPage opens Nearhost's page at the keyed address the core gives, here.
// The keyed address is opened only: no error names it.
func (b *clientBackend) OpenPage() error {
	keyed, err := b.c.PageKeyURL(context.Background())
	if err != nil {
		return err
	}
	if err := b.Open(keyed); err != nil {
		base := keyed
		if u, perr := url.Parse(keyed); perr == nil {
			base = u.Scheme + "://" + u.Host
		}
		return errors.New(strings.ReplaceAll(err.Error(), keyed, base))
	}
	return nil
}

// answerWord is the control API's word for a.
func answerWord(a tui.Answer) string {
	switch a {
	case tui.AnswerYes:
		return "yes"
	case tui.AnswerAlways:
		return "always"
	case tui.AnswerClose:
		return "close"
	}
	return "no"
}

// tuiAnswerError is the core's error for an answer, marked for the
// interface as the core marked it: as an Always allow that was not saved,
// and as the end of its request. It reads as the core's.
func tuiAnswerError(err error) error {
	if errors.Is(err, control.ErrAllowNotSaved) {
		err = notSaved{err}
	}
	if errors.Is(err, control.ErrRequestEnded) {
		return tui.RequestEnded(err)
	}
	return err
}

// reviewer hands what the core's stream says the agents ask for, and what
// they were given without asking, to the interface.
type reviewer struct{ to *tui.Proposals }

func (r reviewer) Review(p agentapi.Proposal) {
	r.to.Propose(tui.Proposal{ID: p.ID, Origin: p.Origin, Path: p.Path, Name: p.Name, Web: p.Web, Expires: p.Expires})
}

// ReviewPassthrough shows the interface a passthrough request.
func (r reviewer) ReviewPassthrough(p agentapi.PassRequest) {
	r.to.Pass(tui.PassRequest{
		ID: p.ID, Origin: p.Origin, Requester: p.Requester, Session: p.Session, Label: p.Label,
		Remote: p.Remote, LocalTaken: p.LocalTaken, CanAlways: p.CanAlways, Expires: p.Expires,
	})
}

// Opened tells the interface of a passthrough that opened without asking.
func (r reviewer) Opened(p agentapi.Passthrough) { r.to.Opened(p.Origin, p.Requester, p.Remote) }

// ReviewDocument shows the interface a request to show a document.
func (r reviewer) ReviewDocument(d agentapi.DocRequest) {
	r.to.Doc(tui.DocRequest{
		ID: d.ID, Origin: d.Origin, Requester: d.Requester, Session: d.Session, Label: d.Label,
		Root: d.Root, Doc: d.Doc, CanAlways: d.CanAlways, NewRoot: d.NewRoot, Expires: d.Expires,
	})
}

// Shown tells the interface of a document opened without asking.
func (r reviewer) Shown(origin, requester, doc string) { r.to.Shown(origin, requester, doc) }
