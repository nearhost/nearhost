package tui

import (
	"fmt"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/config"
)

// Proposal is a project a coding agent asks to add. The interface opens the
// add-project form with it filled in, and the user saves it or dismisses
// it.
type Proposal struct {
	ID     string
	Origin string // the server the agent is on, or config.LocalServer
	Path   string // absolute, on the origin
	Name   string // empty when the agent left it to discovery
	Web    int    // 0 when the agent did not say
	// Expires is when the agent stops waiting for the answer.
	Expires time.Time
}

// where names the proposal's machine in a sentence: "on mini", "on this
// machine".
func (p Proposal) where() string {
	if p.Origin == config.LocalServer {
		return "on this machine"
	}
	return "on " + clean(p.Origin)
}

// Proposals hands the interface the proposals agents make, in the order
// they come. Attach it to the running program; a proposal made before that
// waits for it.
type Proposals struct {
	mu      sync.Mutex
	waiting []tea.Msg
	wake    chan struct{} // nil until attached
}

// PassRequest is a passthrough a coding agent asks for: one port of a
// server, forwarded to this machine, waiting for the user's answer.
type PassRequest struct {
	ID        string
	Origin    string // the server whose port is asked for
	Requester string // the tool that asked, such as claude-code
	Session   string // the requester's session; may be empty
	Label     string // what the port is for, as the requester said
	Remote    int    // the port on the server
	// LocalTaken is set when the same port here is taken, so another is used.
	LocalTaken bool
	// CanAlways is set when Always allow may be offered.
	CanAlways bool
	// Expires is when the agent stops waiting for the answer.
	Expires time.Time
}

// DocRequest is a document a coding agent asks to show: a file on a server,
// or on this machine, served with the folder it is in, waiting for the
// user's answer.
type DocRequest struct {
	ID        string
	Origin    string // the server the document is on, or config.LocalServer
	Requester string // the tool that asked, such as claude-code
	Session   string // the requester's session; may be empty
	Label     string // what the document is, as the requester said
	Root      string // the folder that will be served, absolute on the origin
	Doc       string // the document, relative to Root
	// CanAlways is set when Always allow may be offered.
	CanAlways bool
	// Expires is when the agent stops waiting for the answer.
	Expires time.Time
}

// Propose gives the interface p. It never blocks.
func (s *Proposals) Propose(p Proposal) { s.push(proposalMsg{p}) }

// Pass gives the interface a passthrough request, in order with the
// proposals. It never blocks.
func (s *Proposals) Pass(r PassRequest) { s.push(passMsg{r}) }

// Opened tells the interface that a passthrough of remote on origin opened
// for requester without asking, because of an Always allow. It never blocks.
func (s *Proposals) Opened(origin, requester string, remote int) {
	s.push(passOpenedMsg{origin, requester, remote})
}

// Doc gives the interface a request to show a document, in order with the
// proposals. It never blocks.
func (s *Proposals) Doc(r DocRequest) { s.push(docMsg{r}) }

// Shown tells the interface that doc opened in the browser for requester on
// origin without asking, because of an Always allow or a tree already
// served. It never blocks.
func (s *Proposals) Shown(origin, requester, doc string) {
	s.push(docShownMsg{origin, requester, doc})
}

// Waiting returns a snapshot of the proposals, passthrough requests, notices
// of passthroughs opened without asking, document requests and notices of
// documents shown without asking that are queued and not yet sent to the
// interface, oldest first. Each opened notice is given as a request with
// only Origin, Requester and Remote set, and each shown notice as one with
// only Origin, Requester and Doc set. It exists for tests.
func (s *Proposals) Waiting() (props []Proposal, passes, opened []PassRequest, docs, shown []DocRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.waiting {
		switch m := m.(type) {
		case proposalMsg:
			props = append(props, m.p)
		case passMsg:
			passes = append(passes, m.r)
		case passOpenedMsg:
			opened = append(opened, PassRequest{Origin: m.origin, Requester: m.requester, Remote: m.remote})
		case docMsg:
			docs = append(docs, m.r)
		case docShownMsg:
			shown = append(shown, DocRequest{Origin: m.origin, Requester: m.requester, Doc: m.doc})
		}
	}
	return props, passes, opened, docs, shown
}

// push queues msg for the sender goroutine and wakes it.
func (s *Proposals) push(msg tea.Msg) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waiting = append(s.waiting, msg)
	if s.wake != nil {
		select {
		case s.wake <- struct{}{}:
		default: // the sender is awake already
		}
	}
}

// Attach makes s give its proposals to prog.
func (s *Proposals) Attach(prog *tea.Program) { s.attach(prog.Send) }

// attach starts the one goroutine that sends the proposals, oldest first.
// Program.Send returns at once once the program has ended.
func (s *Proposals) attach(send func(tea.Msg)) {
	s.mu.Lock()
	s.wake = make(chan struct{}, 1)
	wake := s.wake
	s.mu.Unlock()
	go func() {
		for {
			s.mu.Lock()
			if len(s.waiting) == 0 {
				s.mu.Unlock()
				<-wake
				continue
			}
			msg := s.waiting[0]
			s.waiting = s.waiting[1:]
			s.mu.Unlock()
			send(msg)
		}
	}()
}

// proposalMsg is a proposal arriving.
type proposalMsg struct{ p Proposal }

// passMsg is a passthrough request arriving.
type passMsg struct{ r PassRequest }

// passOpenedMsg is a passthrough that opened without asking.
type passOpenedMsg struct {
	origin, requester string
	remote            int
}

// docMsg is a document request arriving.
type docMsg struct{ r DocRequest }

// docShownMsg is a document that opened without asking.
type docShownMsg struct{ origin, requester, doc string }

// proposing is the proposal whose form is open.
type proposing struct {
	p    Proposal
	form *projectForm
}

// gotProposal queues a proposal and says it came.
func (m *Model) gotProposal(p Proposal) tea.Cmd {
	text := fmt.Sprintf("Agent %s wants to add %s", p.where(), clean(p.Path))
	m.logs.note(text + ".")
	m.waiting = append(m.waiting, asked{prop: &p})
	return m.notify(text)
}

// asked is a request waiting to be reviewed: a proposal, a passthrough or a
// document.
type asked struct {
	prop *Proposal
	pass *PassRequest
	doc  *DocRequest
}

// nextAsked opens the oldest request still waiting, when nothing else is
// open over the screen and the filter is not being typed. One whose agent
// stopped waiting is dropped.
func (m *Model) nextAsked() tea.Cmd {
	if m.quitting || m.form != nil || m.password != nil || m.logView != nil || m.filtering || m.proposing != nil {
		return nil
	}
	for len(m.waiting) > 0 {
		a := m.waiting[0]
		m.waiting = m.waiting[1:]
		if a.pass != nil {
			r := *a.pass
			if !m.now().Before(r.Expires) {
				m.logs.note(fmt.Sprintf("The request from %s to open port %d expired before it was reviewed.", r.who(), r.Remote))
				continue
			}
			m.form = &passForm{b: m.b, w: m.work, r: r}
			return nil
		}
		if a.doc != nil {
			r := *a.doc
			if !m.now().Before(r.Expires) {
				m.logs.note(fmt.Sprintf("The request from %s to show %s expired before it was reviewed.", r.who(), clean(r.Doc)))
				continue
			}
			m.form = &docForm{b: m.b, w: m.work, r: r, root: m.shownRoot(r)}
			return nil
		}
		p := *a.prop
		if !m.now().Before(p.Expires) {
			m.logs.note(fmt.Sprintf("The proposal to add %s %s expired before it was reviewed.", clean(p.Path), p.where()))
			continue
		}
		srv, ok := m.cfg.Server(p.Origin)
		if p.Origin == config.LocalServer {
			srv, ok = config.Server{Name: config.LocalServer}, true
		}
		if !ok {
			m.b.DeclineProposal(p.ID, clean(p.Origin)+" is no longer in Nearhost's config")
			continue
		}
		f, connect := newProposalForm(m.b, m.cfg, srv, p, m.work)
		m.form, m.proposing = f, &proposing{p: p, form: f}
		return connect
	}
	return nil
}

// endProposal answers the proposal whose form has closed without saving.
func (m *Model) endProposal() {
	if m.proposing == nil || m.form == m.proposing.form {
		return
	}
	pr := m.proposing
	m.proposing = nil
	reason := pr.form.err
	if reason == "" {
		reason = pr.form.lastErr
	}
	m.b.DeclineProposal(pr.p.ID, reason)
	m.logs.note(fmt.Sprintf("Declined the proposal to add %s %s.", clean(pr.p.Path), pr.p.where()))
}

// proposalsWaiting is Home's line about requests waiting to be reviewed.
func (m *Model) proposalsWaiting(width int) []string {
	n := 0
	for _, a := range m.waiting {
		if m.now().Before(a.expires()) {
			n++
		}
	}
	if n == 0 {
		return nil
	}
	what := "1 request waiting"
	if n > 1 {
		what = fmt.Sprintf("%d requests waiting", n)
	}
	return field("agents", styleAmber.Render("● "+what)+styleDim.Render(" · ")+styleBeacon.Render("p")+styleDim.Render(" to review"), width)
}
