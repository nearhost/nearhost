package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/transport"
)

// propose gives the model p, as an agent's proposal arriving, and runs what
// follows until the form waits for the user.
func propose(m *Model, p Proposal) {
	if p.Expires.IsZero() {
		p.Expires = m.now().Add(10 * time.Minute)
	}
	settle(m, func() tea.Msg { return proposalMsg{p} })
}

func TestAProposalOpensTheAddFormFilledIn(t *testing.T) {
	m, f, files := projectFormModel(t)
	propose(m, Proposal{ID: "p1", Origin: "mini", Path: "/home/dev/code/blog", Name: "journal", Web: 5173})
	pf, ok := m.form.(*projectForm)
	if !ok || m.proposing == nil {
		t.Fatalf("no form: %T", m.form)
	}
	if len(f.forms.opened) != 1 || f.forms.opened[0].Name != "mini" {
		t.Fatalf("opened %+v", f.forms.opened)
	}
	wantScreen(t, m, "Proposed by an agent on mini", "Add project · routes", "server    mini", "path      /home/dev/code/blog",
		"[x] 5173    web", "[ ] 80      ‹ ignore ›", "https://journal.test serves")
	if !strings.Contains(logText(m, homeKey), "Agent on mini wants to add /home/dev/code/blog.") {
		t.Fatalf("log:\n%s", logText(m, homeKey))
	}
	press(m, "enter")
	wantScreen(t, m, "name      journal▏", "alias     journal.test")
	settle(m, press(m, "enter"))
	settle(m, press(m, "enter"))
	if len(f.forms.saved) != 1 || f.forms.saved[0].Name != "journal" || f.forms.saved[0].Path != "/home/dev/code/blog" {
		t.Fatalf("saved %+v", f.forms.saved)
	}
	if got := f.proposalAnswers(); !slices.Equal(got, []string{"accept p1 journal"}) {
		t.Fatalf("answers %v", got)
	}
	if m.proposing != nil || m.form != nil || files.closed != 1 {
		t.Fatalf("proposing %v, form %T, closed %d", m.proposing, m.form, files.closed)
	}
	_ = pf
}

func TestAWebPortDiscoveryDidNotFindIsLeftToTheUser(t *testing.T) {
	m, _, _ := projectFormModel(t)
	propose(m, Proposal{ID: "p1", Origin: "mini", Path: "/home/dev/code/blog", Web: 9999})
	wantScreen(t, m, "[x] 80      ‹ web ›", "https://blog.test")
}

func TestLeavingAProposalDeclinesIt(t *testing.T) {
	m, f, _ := projectFormModel(t)
	propose(m, Proposal{ID: "p1", Origin: "mini", Path: "/home/dev/code/blog"})
	press(m, "esc") // routes -> path
	wantScreen(t, m, "Add project · path")
	settle(m, press(m, "esc"))
	if got := f.proposalAnswers(); !slices.Equal(got, []string{"decline p1"}) {
		t.Fatalf("answers %v", got)
	}
	if m.form != nil || m.proposing != nil {
		t.Fatalf("form %T", m.form)
	}
	if !strings.Contains(logText(m, homeKey), "Declined the proposal to add /home/dev/code/blog on mini.") {
		t.Fatalf("log:\n%s", logText(m, homeKey))
	}
}

func TestAProposalForAMissingPathDeclinesWithTheReason(t *testing.T) {
	m, f, _ := projectFormModel(t)
	propose(m, Proposal{ID: "p1", Origin: "mini", Path: "/home/dev/nope"})
	wantScreen(t, m, "Add project · path", "/home/dev/nope: no such directory")
	settle(m, press(m, "esc"))
	if got := f.proposalAnswers(); !slices.Equal(got, []string{"decline p1 /home/dev/nope: no such directory"}) {
		t.Fatalf("answers %v", got)
	}
}

func TestAProposalWaitsForTheOpenFormThenOpens(t *testing.T) {
	m, f, _ := projectFormModel(t)
	press(m, "s") // the add-server form
	propose(m, Proposal{ID: "p1", Origin: "mini", Path: "/home/dev/code/blog"})
	propose(m, Proposal{ID: "p2", Origin: config.LocalServer, Path: "/work/here"})
	if _, isServer := m.form.(*serverForm); !isServer || len(m.proposals) != 2 {
		t.Fatalf("form %T, waiting %d", m.form, len(m.proposals))
	}
	m.form = nil
	wantScreen(t, m, "agents    ● 2 proposals waiting · p to review")
	settle(m, press(m, "p"))
	wantScreen(t, m, "Proposed by an agent on mini")
	settle(m, press(m, "esc"))
	settle(m, press(m, "esc"))
	// The next one opens once the first has gone.
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	settle(m, cmd)
	wantScreen(t, m, "Proposed by an agent on this machine", "path      /work/here")
	if got := f.proposalAnswers(); !slices.Equal(got, []string{"decline p1"}) {
		t.Fatalf("answers %v", got)
	}
}

func TestAProposalThatExpiredUnreviewedIsDropped(t *testing.T) {
	m, f, _ := projectFormModel(t)
	press(m, "s")
	propose(m, Proposal{ID: "p1", Origin: "mini", Path: "/home/dev/code/blog"})
	m.now = func() time.Time { return time.Now().Add(time.Hour) }
	m.form = nil
	settle(m, press(m, "p"))
	if m.form != nil || len(m.proposals) != 0 || len(f.proposalAnswers()) != 0 {
		t.Fatalf("form %T, waiting %d, answers %v", m.form, len(m.proposals), f.proposalAnswers())
	}
	if !strings.Contains(logText(m, homeKey), "expired before it was reviewed") {
		t.Fatalf("log:\n%s", logText(m, homeKey))
	}
}

func TestAProposalFromAServerNoLongerConfiguredIsDeclined(t *testing.T) {
	m, f, _ := projectFormModel(t)
	propose(m, Proposal{ID: "p1", Origin: "gone", Path: "/srv/x"})
	if got := f.proposalAnswers(); !slices.Equal(got, []string{"decline p1 gone is no longer in Nearhost's config"}) {
		t.Fatalf("answers %v", got)
	}
}

func TestAProposalWaitsWhileTheFilterIsTyped(t *testing.T) {
	m, _, _ := projectFormModel(t)
	press(m, "/")
	typeText(m, "my")
	propose(m, Proposal{ID: "p1", Origin: "mini", Path: "/home/dev/code/blog"})
	if m.form != nil || len(m.proposals) != 1 {
		t.Fatalf("form %T, waiting %d", m.form, len(m.proposals))
	}
	press(m, "esc") // stops typing the filter
	settle(m, press(m, "p"))
	if m.proposing == nil {
		t.Fatal("p did not open it")
	}
}

func TestPOpensAProposalFromTheLogView(t *testing.T) {
	m, _, _ := projectFormModel(t)
	press(m, "l")
	propose(m, Proposal{ID: "p1", Origin: "mini", Path: "/home/dev/code/blog"})
	if m.form != nil {
		t.Fatal("a proposal opened over the log view")
	}
	settle(m, press(m, "p"))
	if m.logView != nil || m.proposing == nil {
		t.Fatalf("log view %v, proposing %v", m.logView, m.proposing)
	}
}

func TestTheServerPanelSaysWhetherAgentsReachNearhost(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.servers["mini"] = transport.StatusConnected
	f.health["mini"] = engine.ServerHealth{Health: transport.Health{AgentPath: "/home/dev/.nearhost/k3f9q2.sock"}}
	m := sized(f)
	m.sel = rowKey(rowServer, "mini")
	wantScreen(t, m, "agents    ● reach Nearhost at /home/dev/.nearhost/k3f9q2.sock")

	f.health["mini"] = engine.ServerHealth{Health: transport.Health{AgentErr: "the server refused to forward it"}}
	wantScreen(t, m, "agents    ● unavailable the server refused to forward it", "AllowStreamLocalForwarding yes")
	m.sel = homeKey
	wantScreen(t, m, "mini  agents unavailable", "AllowStreamLocalForwarding yes")
}

func TestRetryingOnHomeLeavesAServerWhoseAgentsAloneConnected(t *testing.T) {
	f := newFake(t, sampleConfig())
	f.servers["mini"] = transport.StatusConnected
	f.health["mini"] = engine.ServerHealth{Health: transport.Health{AgentErr: "the server refused to forward it"}}
	m := sized(f)
	settle(m, press(m, "r"))
	if len(f.edit.reconnected) != 0 {
		t.Fatalf("r on Home reconnected %v, whose projects were serving", f.edit.reconnected)
	}
	wantScreen(t, m, "mini  agents unavailable")
}
