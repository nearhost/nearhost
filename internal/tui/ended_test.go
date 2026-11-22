package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// end tells the model that the request id waits no longer, as the core says
// when another client answered it, and runs what follows.
func end(m *Model, id string) {
	settle(m, func() tea.Msg { return requestEndedMsg{id} })
}

// A request answered elsewhere while it waits in the queue is dropped from
// it, and never opens.
func TestARequestEndedWhileQueuedIsDropped(t *testing.T) {
	m, f, _ := projectFormModel(t)
	press(m, "s") // the add-server form keeps the requests queued
	propose(m, Proposal{ID: "p1", Origin: "mini", Path: "/home/dev/code/blog"})
	ask(m, passReq(m))
	askDoc(m, docReq(m))
	if len(m.waiting) != 3 {
		t.Fatalf("waiting %d, want 3", len(m.waiting))
	}
	end(m, "r1")
	end(m, "p1")
	end(m, "d1")
	if len(m.waiting) != 0 {
		t.Fatalf("waiting %d after each ended, want none", len(m.waiting))
	}
	m.form = nil
	settle(m, press(m, "p"))
	if m.form != nil || len(f.proposalAnswers()) != 0 || len(f.passCalls) != 0 || len(f.docCalls) != 0 {
		t.Fatalf("form %T, answers %v, pass %v, doc %v", m.form, f.proposalAnswers(), f.passCalls, f.docCalls)
	}
	log := logText(m, homeKey)
	for _, want := range []string{
		"The proposal to add /home/dev/code/blog on mini was answered in another window.",
		"The request from Claude Code on apollo to open port 41873 was answered in another window.",
		"The request from Claude Code on apollo to show plans/x.md was answered in another window.",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("the log does not say %q:\n%s", want, log)
		}
	}
}

// A proposal answered elsewhere while its form is open closes the form,
// which declines nothing and adds nothing: the first answer wins.
func TestAProposalEndedWhileOpenClosesItsForm(t *testing.T) {
	m, f, _ := projectFormModel(t)
	propose(m, Proposal{ID: "p1", Origin: "mini", Path: "/home/dev/code/blog"})
	if _, ok := m.form.(*projectForm); !ok || m.proposing == nil {
		t.Fatalf("form %T, proposing %v", m.form, m.proposing)
	}
	end(m, "p1")
	if m.form != nil || m.proposing != nil {
		t.Fatalf("form %T, proposing %v after the proposal ended", m.form, m.proposing)
	}
	if got := f.proposalAnswers(); len(got) != 0 {
		t.Errorf("answers %v, want none", got)
	}
	if len(f.forms.saved) != 0 {
		t.Errorf("saved %+v", f.forms.saved)
	}
	want := "The proposal to add /home/dev/code/blog on mini was answered in another window, so this form closed without adding it."
	if log := logText(m, homeKey); !strings.Contains(log, want) {
		t.Errorf("the log does not say %q:\n%s", want, log)
	}
}

// A passthrough or document request answered elsewhere while its box is
// open closes the box, which answers nothing.
func TestARequestEndedWhileOpenClosesItsBox(t *testing.T) {
	t.Run("passthrough", func(t *testing.T) {
		m, f, _ := projectFormModel(t)
		ask(m, passReq(m))
		if _, ok := m.form.(*passForm); !ok {
			t.Fatalf("form %T", m.form)
		}
		end(m, "r1")
		if m.form != nil || len(f.passCalls) != 0 {
			t.Fatalf("form %T, calls %v", m.form, f.passCalls)
		}
		want := "The request from Claude Code on apollo to open port 41873 was answered in another window, so this box closed."
		if log := logText(m, homeKey); !strings.Contains(log, want) {
			t.Errorf("the log does not say %q:\n%s", want, log)
		}
	})
	t.Run("document", func(t *testing.T) {
		m, f, _ := projectFormModel(t)
		askDoc(m, docReq(m))
		if _, ok := m.form.(*docForm); !ok {
			t.Fatalf("form %T", m.form)
		}
		end(m, "d1")
		if m.form != nil || len(f.docCalls) != 0 {
			t.Fatalf("form %T, calls %v", m.form, f.docCalls)
		}
		want := "The request from Claude Code on apollo to show plans/x.md was answered in another window, so this box closed."
		if log := logText(m, homeKey); !strings.Contains(log, want) {
			t.Errorf("the log does not say %q:\n%s", want, log)
		}
	})
	t.Run("an answer of this window's under way", func(t *testing.T) {
		m, _, _ := projectFormModel(t)
		ask(m, passReq(m))
		cmd := press(m, "y")
		end(m, "r1")
		if _, ok := m.form.(*passForm); !ok {
			t.Fatalf("form %T: the box closed while its own answer ran", m.form)
		}
		settle(m, cmd)
	})
	t.Run("expired", func(t *testing.T) {
		m, _, _ := projectFormModel(t)
		ask(m, passReq(m))
		m.now = func() time.Time { return time.Now().Add(time.Hour) }
		end(m, "r1")
		if m.form != nil {
			t.Fatalf("form %T", m.form)
		}
		want := "The request from Claude Code on apollo to open port 41873 expired before it was answered, so this box closed."
		if log := logText(m, homeKey); !strings.Contains(log, want) {
			t.Errorf("the log does not say %q:\n%s", want, log)
		}
	})
}

// Ended drops a request still queued for the interface, so the interface
// never sees it, and otherwise tells the interface in order.
func TestProposalsEnded(t *testing.T) {
	var s Proposals
	s.Propose(Proposal{ID: "p1"})
	s.Pass(PassRequest{ID: "r1"})
	s.Doc(DocRequest{ID: "d1"})
	s.Ended("r1")
	s.Ended("p1")
	s.Ended("d1")
	if props, passes, _, docs, _ := s.Waiting(); len(props)+len(passes)+len(docs) != 0 {
		t.Fatalf("still queued: %v %v %v", props, passes, docs)
	}
	got := make(chan tea.Msg, 4)
	s.attach(func(msg tea.Msg) { got <- msg })
	s.Ended("x1")
	select {
	case msg := <-got:
		if msg != (requestEndedMsg{"x1"}) {
			t.Fatalf("sent %#v", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ended sent nothing")
	}
	select {
	case msg := <-got:
		t.Fatalf("also sent %#v", msg)
	default:
	}
}

// A proposal that ends while its form saves the project lets the save run to
// its end: the project is added, and the agent, answered already, is not
// answered again.
func TestAProposalEndedWhileSavingLetsTheSaveEnd(t *testing.T) {
	m, f, _ := projectFormModel(t)
	propose(m, Proposal{ID: "p1", Origin: "mini", Path: "/home/dev/code/blog", Name: "journal", Web: 5173})
	press(m, "enter")
	settle(m, press(m, "enter"))
	save := press(m, "enter")
	if pf, ok := m.form.(*projectForm); !ok || !pf.saving() {
		t.Fatalf("form %T is not saving", m.form)
	}
	end(m, "p1")
	if _, ok := m.form.(*projectForm); !ok {
		t.Fatalf("form %T: the save was cut short", m.form)
	}
	settle(m, save)
	if len(f.forms.saved) != 1 || m.form != nil || m.proposing != nil {
		t.Fatalf("saved %+v, form %T, proposing %v", f.forms.saved, m.form, m.proposing)
	}
	if got := f.proposalAnswers(); len(got) != 0 {
		t.Errorf("answers %v, want none", got)
	}
	want := "Added journal. The agent's proposal on mini was answered in another window while this one saved."
	if log := logText(m, homeKey); !strings.Contains(log, want) {
		t.Errorf("the log does not say %q:\n%s", want, log)
	}
}
