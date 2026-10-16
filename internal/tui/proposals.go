package tui

import (
	"fmt"
	"sync"
	"sync/atomic"
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

// Proposals hands the interface the proposals agents make. Attach it to the
// running program; a proposal made before that waits for it.
type Proposals struct {
	p       atomic.Pointer[tea.Program]
	mu      sync.Mutex
	waiting []Proposal
}

// Propose gives the interface p. It never blocks.
func (s *Proposals) Propose(p Proposal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if prog := s.p.Load(); prog != nil {
		go prog.Send(proposalMsg{p})
		return
	}
	s.waiting = append(s.waiting, p)
}

// Attach makes s give its proposals to prog.
func (s *Proposals) Attach(prog *tea.Program) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.p.Store(prog)
	for _, p := range s.waiting {
		go prog.Send(proposalMsg{p})
	}
	s.waiting = nil
}

// proposalMsg is a proposal arriving.
type proposalMsg struct{ p Proposal }

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
	m.b.DeclineProposal(pr.p.ID, pr.form.err)
	m.logs.note(fmt.Sprintf("Declined the proposal to add %s %s.", clean(pr.p.Path), pr.p.where()))
}

// proposalsWaiting is Home's line about proposals waiting to be reviewed.
func (m *Model) proposalsWaiting(width int) []string {
	n := len(m.proposals)
	if n == 0 {
		return nil
	}
	what := "1 proposal waiting"
	if n > 1 {
		what = fmt.Sprintf("%d proposals waiting", n)
	}
	return field("agents", styleAmber.Render("● "+what)+styleDim.Render(" · ")+styleBeacon.Render("p")+styleDim.Render(" to review"), width)
}
