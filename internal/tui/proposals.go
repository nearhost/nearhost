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

// proposing is the proposal whose form is open.
type proposing struct {
	p    Proposal
	form *projectForm
}

// gotProposal queues a proposal and says it came.
func (m *Model) gotProposal(p Proposal) tea.Cmd {
	text := fmt.Sprintf("Agent %s wants to add %s", p.where(), clean(p.Path))
	m.logs.note(text + ".")
	m.proposals = append(m.proposals, p)
	return m.notify(text)
}

// nextProposal opens the oldest proposal still waiting, when nothing else
// is open over the screen and the filter is not being typed. One whose
// agent stopped waiting is dropped.
func (m *Model) nextProposal() tea.Cmd {
	if m.quitting || m.form != nil || m.password != nil || m.logView != nil || m.filtering || m.proposing != nil {
		return nil
	}
	for len(m.proposals) > 0 {
		p := m.proposals[0]
		m.proposals = m.proposals[1:]
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

// proposalsWaiting is Home's line about proposals waiting to be reviewed.
func (m *Model) proposalsWaiting(width int) []string {
	n := 0
	for _, p := range m.proposals {
		if m.now().Before(p.Expires) {
			n++
		}
	}
	if n == 0 {
		return nil
	}
	what := "1 proposal waiting"
	if n > 1 {
		what = fmt.Sprintf("%d proposals waiting", n)
	}
	return field("agents", styleAmber.Render("● "+what)+styleDim.Render(" · ")+styleBeacon.Render("p")+styleDim.Render(" to review"), width)
}
