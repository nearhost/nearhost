package tui

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/config"
)

// docReq is a request from Claude Code on apollo to show plans/x.md.
func docReq(m *Model) DocRequest {
	return DocRequest{
		ID: "d1", Origin: "apollo", Requester: "claude-code", Root: "/home/dev/code/app", Doc: "plans/x.md",
		CanAlways: true, Expires: m.now().Add(10 * time.Minute),
	}
}

// askDoc gives the model r, as a request arriving, and runs what follows.
func askDoc(m *Model, r DocRequest) {
	settle(m, func() tea.Msg { return docMsg{r} })
}

func TestDocPrompt(t *testing.T) {
	m, _, _ := projectFormModel(t)
	askDoc(m, docReq(m))
	df, ok := m.form.(*docForm)
	if !ok {
		t.Fatalf("form %T", m.form)
	}
	if got := df.heading().title; got != "Show a document" {
		t.Fatalf("heading %q", got)
	}
	if got := keyLabels(df); !slices.Equal(got, []string{"y", "n", "a"}) {
		t.Fatalf("keys %v", got)
	}
	// A server's root is shown as it was sent.
	wantText(t, m, "Claude Code on apollo wants to show plans/x.md",
		"Nearhost will serve files in /home/dev/code/app, except dotfiles.")
}

func TestDocPromptShortensTheLaptopsHome(t *testing.T) {
	m, _, _ := projectFormModel(t)
	m.homeDir = func() (string, error) { return "/Users/dev", nil }
	r := docReq(m)
	r.Origin, r.Root = config.LocalServer, "/Users/dev/code/app"
	askDoc(m, r)
	wantText(t, m, "Claude Code on this machine wants to show plans/x.md",
		"Nearhost will serve files in ~/code/app, except dotfiles.")

	// On a server, the same path is not the laptop's home.
	m, _, _ = projectFormModel(t)
	m.homeDir = func() (string, error) { return "/home/dev", nil }
	askDoc(m, docReq(m))
	wantText(t, m, "Nearhost will serve files in /home/dev/code/app, except dotfiles.")
}

func TestDocPromptAnswers(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want string
		log  string
	}{
		{"y", "answer d1 1", "Opened plans/x.md for Claude Code on apollo."},
		{"a", "answer d1 2", "Opened plans/x.md for Claude Code on apollo."},
		{"n", "answer d1 0", "Declined to show plans/x.md for Claude Code on apollo."},
		{"esc", "answer d1 0", "Declined to show plans/x.md for Claude Code on apollo."},
	} {
		t.Run(tc.key, func(t *testing.T) {
			m, f, _ := projectFormModel(t)
			askDoc(m, docReq(m))
			settle(m, press(m, tc.key))
			if got := f.documentCalls(); !slices.Equal(got, []string{tc.want}) {
				t.Fatalf("calls %v", got)
			}
			if m.form != nil {
				t.Fatalf("form %T still open", m.form)
			}
			if !strings.Contains(logText(m, homeKey), tc.log) {
				t.Fatalf("log:\n%s", logText(m, homeKey))
			}
		})
	}
}

func TestDocPromptNoAlwaysForAgent(t *testing.T) {
	m, f, _ := projectFormModel(t)
	r := docReq(m)
	r.Requester, r.CanAlways = "agent", false
	askDoc(m, r)
	wantText(t, m, "An agent on apollo wants to show plans/x.md")
	if got := keyLabels(m.form); !slices.Equal(got, []string{"y", "n"}) {
		t.Fatalf("keys %v", got)
	}
	settle(m, press(m, "a"))
	if _, ok := m.form.(*docForm); !ok || len(f.documentCalls()) != 0 {
		t.Fatalf("form %T, calls %v", m.form, f.documentCalls())
	}
}

func TestDocPromptLabel(t *testing.T) {
	m, _, _ := projectFormModel(t)
	r := docReq(m)
	r.Label = "the plan"
	askDoc(m, r)
	wantText(t, m, "Claude Code on apollo wants to show plans/x.md (the plan)")
}

func TestDocPromptYesFailsThenEscCloses(t *testing.T) {
	m, f, _ := projectFormModel(t)
	f.docErr = errors.New("nearhost's page isn't served")
	askDoc(m, docReq(m))
	settle(m, press(m, "y"))
	if _, ok := m.form.(*docForm); !ok {
		t.Fatalf("form %T", m.form)
	}
	wantScreen(t, m, "Nearhost's page isn't served", "This request has ended")
	if got := keyLabels(m.form); !slices.Equal(got, []string{"esc"}) {
		t.Fatalf("keys %v", got)
	}
	settle(m, press(m, "esc"))
	if m.form != nil {
		t.Fatalf("form %T still open", m.form)
	}
	if got := f.documentCalls(); !slices.Equal(got, []string{"answer d1 1"}) {
		t.Fatalf("calls %v, want no No on an ended request", got)
	}
}

func TestDocQueuesWithProposals(t *testing.T) {
	m, f, _ := projectFormModel(t)
	press(m, "s") // the add-server form holds the screen
	propose(m, Proposal{ID: "p1", Origin: "mini", Path: "/home/dev/code/blog"})
	ask(m, passReq(m))
	askDoc(m, docReq(m))
	m.form = nil
	wantScreen(t, m, "agents    ● 3 requests waiting · p to review")
	settle(m, press(m, "p"))
	if _, ok := m.form.(*projectForm); !ok {
		t.Fatalf("first was %T", m.form)
	}
	for i := 0; i < 2; i++ {
		if _, ok := m.form.(*projectForm); ok {
			settle(m, press(m, "esc"))
		}
	}
	if _, ok := m.form.(*passForm); !ok {
		t.Fatalf("second was %T", m.form)
	}
	settle(m, press(m, "n"))
	settle(m, press(m, "p"))
	if _, ok := m.form.(*docForm); !ok {
		t.Fatalf("third was %T", m.form)
	}
	settle(m, press(m, "n"))
	if got := f.documentCalls(); !slices.Equal(got, []string{"answer d1 0"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestDocExpiredSkipped(t *testing.T) {
	m, f, _ := projectFormModel(t)
	press(m, "s")
	askDoc(m, docReq(m))
	m.now = func() time.Time { return time.Now().Add(time.Hour) }
	m.form = nil
	settle(m, press(m, "p"))
	if m.form != nil || len(m.waiting) != 0 || len(f.documentCalls()) != 0 {
		t.Fatalf("form %T, waiting %d, calls %v", m.form, len(m.waiting), f.documentCalls())
	}
	if !strings.Contains(logText(m, homeKey), "The request from Claude Code on apollo to show plans/x.md expired before it was reviewed.") {
		t.Fatalf("log:\n%s", logText(m, homeKey))
	}
}

func TestDocNotification(t *testing.T) {
	m, _, _ := projectFormModel(t)
	m.getenv = envOf(map[string]string{"TERM_PROGRAM": "ghostty"})
	_, cmd := m.Update(docMsg{docReq(m)})
	if got := raws(cmd); len(got) != 1 || got[0] != "\x1b]9;Claude Code on apollo wants to show plans/x.md\x07" {
		t.Fatalf("%q", got)
	}
}

func TestDocShownNotice(t *testing.T) {
	var s Proposals
	s.Shown("apollo", "claude-code", "plans/x.md")
	var got []tea.Msg
	var mu sync.Mutex
	done := make(chan struct{})
	s.attach(func(msg tea.Msg) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, msg)
		close(done)
	})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the notice did not arrive")
	}
	mu.Lock()
	msg := got[0]
	mu.Unlock()

	m, _, _ := projectFormModel(t)
	m.getenv = envOf(map[string]string{"TERM_PROGRAM": "ghostty"})
	_, cmd := m.Update(msg)
	if got := raws(cmd); len(got) != 0 {
		t.Fatalf("notification %q", got)
	}
	want := "Claude Code on apollo opened plans/x.md"
	if !strings.Contains(logText(m, homeKey), want) {
		t.Fatalf("log:\n%s", logText(m, homeKey))
	}
	wantScreen(t, m, want)
}

func TestDocRequestsKeepTheOrderWithProposals(t *testing.T) {
	var s Proposals
	s.Propose(Proposal{ID: "p1"})
	s.Doc(DocRequest{ID: "d1"})
	s.Pass(PassRequest{ID: "r1"})
	props, passes, _, docs, _ := s.Waiting()
	if len(props) != 1 || len(passes) != 1 || len(docs) != 1 || docs[0].ID != "d1" {
		t.Fatalf("waiting %v %v %v", props, passes, docs)
	}
	var mu sync.Mutex
	var got []tea.Msg
	done := make(chan struct{})
	s.attach(func(msg tea.Msg) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, msg)
		if len(got) == 3 {
			close(done)
		}
	})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("not all arrived")
	}
	mu.Lock()
	defer mu.Unlock()
	if _, ok := got[0].(proposalMsg); !ok {
		t.Fatalf("first %#v", got[0])
	}
	if m, ok := got[1].(docMsg); !ok || m.r.ID != "d1" {
		t.Fatalf("second %#v", got[1])
	}
	if _, ok := got[2].(passMsg); !ok {
		t.Fatalf("third %#v", got[2])
	}
}
