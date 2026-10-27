package tui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// passReq is a request from Claude Code on apollo for port 41873.
func passReq(m *Model) PassRequest {
	return PassRequest{
		ID: "r1", Origin: "apollo", Requester: "claude-code", Remote: 41873,
		CanAlways: true, Expires: m.now().Add(10 * time.Minute),
	}
}

// ask gives the model r, as a request arriving, and runs what follows.
func ask(m *Model, r PassRequest) {
	settle(m, func() tea.Msg { return passMsg{r} })
}

func keyLabels(f form) []string {
	var out []string
	for _, k := range f.keys() {
		out = append(out, k.key)
	}
	return out
}

func TestPassPromptOpens(t *testing.T) {
	m, _, _ := projectFormModel(t)
	ask(m, passReq(m))
	pf, ok := m.form.(*passForm)
	if !ok {
		t.Fatalf("form %T", m.form)
	}
	if got := pf.heading().title; got != "Open a port" {
		t.Fatalf("heading %q", got)
	}
	if got := keyLabels(pf); !slices.Equal(got, []string{"y", "n", "a"}) {
		t.Fatalf("keys %v", got)
	}
	wantScreen(t, m, "Open a port", "Claude Code on apollo wants to open port 41873 on this laptop.")
}

func TestPassPromptLabel(t *testing.T) {
	m, _, _ := projectFormModel(t)
	r := passReq(m)
	r.Label = "browser preview"
	ask(m, r)
	wantText(t, m, "Claude Code on apollo wants to open port 41873 on this laptop (browser preview).")
}

func TestPassPromptNoAlwaysForAgent(t *testing.T) {
	m, f, _ := projectFormModel(t)
	r := passReq(m)
	r.Requester, r.CanAlways = "agent", false
	ask(m, r)
	wantScreen(t, m, "An agent on apollo wants to open port 41873")
	if got := keyLabels(m.form); !slices.Equal(got, []string{"y", "n"}) {
		t.Fatalf("keys %v", got)
	}
	settle(m, press(m, "a"))
	if _, ok := m.form.(*passForm); !ok || len(f.passthroughCalls()) != 0 {
		t.Fatalf("form %T, calls %v", m.form, f.passthroughCalls())
	}
}

func TestPassPromptLocalTaken(t *testing.T) {
	m, _, _ := projectFormModel(t)
	r := passReq(m)
	r.LocalTaken = true
	ask(m, r)
	wantScreen(t, m, "Port 41873 is in use here, so it will open on another port.")
}

func TestPassPromptAnswers(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want string
		log  string
	}{
		{"y", "answer r1 1", "Opened port 41873 for Claude Code on apollo."},
		{"a", "answer r1 2", "Opened port 41873 for Claude Code on apollo."},
		{"n", "answer r1 0", "Declined to open port 41873 for Claude Code on apollo."},
		{"esc", "answer r1 0", "Declined to open port 41873 for Claude Code on apollo."},
	} {
		t.Run(tc.key, func(t *testing.T) {
			m, f, _ := projectFormModel(t)
			ask(m, passReq(m))
			settle(m, press(m, tc.key))
			if got := f.passthroughCalls(); !slices.Equal(got, []string{tc.want}) {
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

func TestPassPromptYesFailsThenEscCloses(t *testing.T) {
	m, f, _ := projectFormModel(t)
	f.passErr = errors.New("the forward failed")
	ask(m, passReq(m))
	settle(m, press(m, "y"))
	if _, ok := m.form.(*passForm); !ok {
		t.Fatalf("form %T", m.form)
	}
	wantScreen(t, m, "The forward failed", "This request has ended")
	if got := keyLabels(m.form); !slices.Equal(got, []string{"esc"}) {
		t.Fatalf("keys %v", got)
	}
	settle(m, press(m, "y")) // another yes would only fail again
	if _, ok := m.form.(*passForm); !ok {
		t.Fatalf("form %T", m.form)
	}
	settle(m, press(m, "esc"))
	if m.form != nil {
		t.Fatalf("form %T still open", m.form)
	}
	if got := f.passthroughCalls(); !slices.Equal(got, []string{"answer r1 1"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestPassPromptExpiredNoCloses(t *testing.T) {
	m, f, _ := projectFormModel(t)
	ask(m, passReq(m))
	f.mu.Lock()
	f.endPass("r1", errors.New("the request for port 41873 has ended: expired"))
	f.mu.Unlock()
	settle(m, press(m, "n"))
	if m.form != nil {
		t.Fatalf("form %T still open", m.form)
	}
	if got := f.passthroughCalls(); !slices.Equal(got, []string{"answer r1 0"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestPassPromptExpiredYesThenNCloses(t *testing.T) {
	m, f, _ := projectFormModel(t)
	ask(m, passReq(m))
	f.mu.Lock()
	f.endPass("r1", errors.New("the request for port 41873 has ended: expired"))
	f.mu.Unlock()
	settle(m, press(m, "y"))
	wantScreen(t, m, "The request for port 41873 has ended: expired", "This request has ended")
	settle(m, press(m, "n"))
	if m.form != nil {
		t.Fatalf("form %T still open", m.form)
	}
	if got := f.passthroughCalls(); !slices.Equal(got, []string{"answer r1 1"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestPassPromptSessionClosedWhileShownEscCloses(t *testing.T) {
	m, f, _ := projectFormModel(t)
	ask(m, passReq(m))
	// The agent's session ends while the box is shown.
	f.mu.Lock()
	f.endPass("r1", errors.New("the request for port 41873 has ended: closed"))
	f.mu.Unlock()
	settle(m, press(m, "a"))
	wantScreen(t, m, "The request for port 41873 has ended: closed", "This request has ended")
	settle(m, press(m, "esc"))
	if m.form != nil {
		t.Fatalf("form %T still open", m.form)
	}
	if got := f.passthroughCalls(); !slices.Equal(got, []string{"answer r1 2"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestPassPromptErrorNotEndedSaysNothingOfEnding(t *testing.T) {
	m, f, _ := projectFormModel(t)
	ask(m, passReq(m))
	f.mu.Lock()
	f.passErr = errors.New("no coding agent can reach Nearhost")
	f.mu.Unlock()
	settle(m, press(m, "n"))
	pf, ok := m.form.(*passForm)
	if !ok {
		t.Fatalf("form %T", m.form)
	}
	if text := strings.Join(pf.lines(80), "\n"); !strings.Contains(text, "No coding agent can reach Nearhost") || strings.Contains(text, "has ended") {
		t.Fatalf("lines:\n%s", text)
	}
	settle(m, press(m, "esc"))
	if got := f.passthroughCalls(); m.form != nil || !slices.Equal(got, []string{"answer r1 0", "answer r1 0"}) {
		t.Fatalf("form %T, calls %v, want the No sent again on close", m.form, got)
	}
}

func TestPassAnsweredErrorAfterTheBoxHasGoneLogs(t *testing.T) {
	m, _, _ := projectFormModel(t)
	m.Update(passAnsweredMsg{seq: 1, r: passReq(m), answer: AnswerYes, err: errors.New("the server isn't connected")})
	if !strings.Contains(logText(m, homeKey), "Could not open port 41873 for Claude Code on apollo: the server isn't connected.") {
		t.Fatalf("log:\n%s", logText(m, homeKey))
	}
}

func TestPassAndProposalQueue(t *testing.T) {
	m, f, _ := projectFormModel(t)
	press(m, "s") // the add-server form holds the screen
	propose(m, Proposal{ID: "p1", Origin: "mini", Path: "/home/dev/code/blog"})
	ask(m, passReq(m))
	propose(m, Proposal{ID: "p2", Origin: "mini", Path: "/work/here"})
	m.form = nil
	wantScreen(t, m, "agents    ● 3 requests waiting · p to review")
	settle(m, press(m, "p"))
	wantScreen(t, m, "Proposed by an agent on mini", "path      /home/dev/code/blog")
	for i := 0; i < 2; i++ {
		if _, ok := m.form.(*projectForm); ok {
			settle(m, press(m, "esc"))
		}
	}
	// The next opens by itself once the first has gone.
	if _, ok := m.form.(*passForm); !ok {
		t.Fatalf("second was %T", m.form)
	}
	settle(m, press(m, "n"))
	settle(m, press(m, "p"))
	wantScreen(t, m, "path      /work/here")
	if got := f.proposalAnswers(); !slices.Equal(got, []string{"decline p1"}) {
		t.Fatalf("answers %v", got)
	}
}

func TestPassExpiredSkipped(t *testing.T) {
	m, f, _ := projectFormModel(t)
	press(m, "s")
	ask(m, passReq(m))
	m.now = func() time.Time { return time.Now().Add(time.Hour) }
	m.form = nil
	settle(m, press(m, "p"))
	if m.form != nil || len(m.waiting) != 0 || len(f.passthroughCalls()) != 0 {
		t.Fatalf("form %T, waiting %d, calls %v", m.form, len(m.waiting), f.passthroughCalls())
	}
	if !strings.Contains(logText(m, homeKey), "expired before it was reviewed") {
		t.Fatalf("log:\n%s", logText(m, homeKey))
	}
}

func TestPassNotification(t *testing.T) {
	m, _, _ := projectFormModel(t)
	m.getenv = envOf(map[string]string{"TERM_PROGRAM": "ghostty"})
	_, cmd := m.Update(passMsg{passReq(m)})
	if got := raws(cmd); len(got) != 1 || got[0] != "\x1b]9;Claude Code on apollo wants to open port 41873\x07" {
		t.Fatalf("%q", got)
	}
	m, _, _ = projectFormModel(t)
	off := false
	m.cfg.Notifications = &off
	_, cmd = m.Update(passMsg{passReq(m)})
	if got := raws(cmd); len(got) != 0 {
		t.Fatalf("%q", got)
	}
}

func TestOpenedToast(t *testing.T) {
	m, _, _ := projectFormModel(t)
	m.getenv = envOf(map[string]string{"TERM_PROGRAM": "ghostty"})
	_, cmd := m.Update(passOpenedMsg{"apollo", "claude-code", 41873})
	if got := raws(cmd); len(got) != 0 {
		t.Fatalf("notification %q", got)
	}
	want := "Claude Code on apollo opened port 41873"
	if !strings.Contains(logText(m, homeKey), want) {
		t.Fatalf("log:\n%s", logText(m, homeKey))
	}
	wantScreen(t, m, want)
}

func TestRequesterName(t *testing.T) {
	for in, want := range map[string]string{
		"claude-code": "Claude Code", "codex": "Codex", "agent": "An agent",
		"cursor": "cursor", "cl\x1b[31maude": "claude",
	} {
		if got := requesterName(in); got != want {
			t.Errorf("requesterName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPassAnsweredWithNoFormStillLogs(t *testing.T) {
	m, _, _ := projectFormModel(t)
	m.Update(passAnsweredMsg{seq: 1, r: passReq(m), answer: AnswerYes})
	if !strings.Contains(logText(m, homeKey), "Opened port 41873 for Claude Code on apollo.") {
		t.Fatalf("log:\n%s", logText(m, homeKey))
	}
}

func TestPassAnsweredWithAnotherFormOpenKeepsIt(t *testing.T) {
	m, _, _ := projectFormModel(t)
	press(m, "s")
	other := m.form
	m.Update(passAnsweredMsg{seq: 1, r: passReq(m), answer: AnswerNo})
	if m.form != other {
		t.Fatalf("form %T replaced", m.form)
	}
	if !strings.Contains(logText(m, homeKey), "Declined to open port 41873 for Claude Code on apollo.") {
		t.Fatalf("log:\n%s", logText(m, homeKey))
	}
}

func TestAlwaysNotSavedStillLogsOpen(t *testing.T) {
	m, f, _ := projectFormModel(t)
	f.passErr = fmt.Errorf("%w: disk full", ErrAllowNotSaved)
	ask(m, passReq(m))
	settle(m, press(m, "a"))
	if _, ok := m.form.(*passForm); !ok {
		t.Fatalf("form %T", m.form)
	}
	wantText(t, m, "Always allow was not saved: disk full")
	if !strings.Contains(logText(m, homeKey), "Opened port 41873 for Claude Code on apollo, but Always allow was not saved.") {
		t.Fatalf("log:\n%s", logText(m, homeKey))
	}
	settle(m, press(m, "esc"))
	if m.form != nil {
		t.Fatalf("form %T still open", m.form)
	}
	if got := f.passthroughCalls(); !slices.Equal(got, []string{"answer r1 2"}) {
		t.Fatalf("calls %v, want no No after the port opened", got)
	}
}

// An Always allow whose request ended and whose grant failed logs that the
// request had ended, as its port was closed.
func TestAlwaysEndedAndNotSavedLogsEnded(t *testing.T) {
	m, f, _ := projectFormModel(t)
	f.passErr = fmt.Errorf("%w; %w", ErrRequestEnded, ErrAllowNotSaved)
	ask(m, passReq(m))
	settle(m, press(m, "a"))
	log := logText(m, homeKey)
	if !strings.Contains(log, "The request to open port 41873 for Claude Code on apollo had already ended.") ||
		strings.Contains(log, "Opened port") {
		t.Fatalf("log:\n%s", log)
	}
}

func TestNoOnEndedLogsEnded(t *testing.T) {
	m, f, _ := projectFormModel(t)
	ask(m, passReq(m))
	f.mu.Lock()
	f.endPass("r1", errors.New("the request for port 41873 has ended: expired"))
	f.mu.Unlock()
	settle(m, press(m, "n"))
	if m.form != nil {
		t.Fatalf("form %T still open", m.form)
	}
	log := logText(m, homeKey)
	if !strings.Contains(log, "The request to open port 41873 for Claude Code on apollo had already ended.") || strings.Contains(log, "Declined") {
		t.Fatalf("log:\n%s", log)
	}
}

func TestCloseAfterNonEndingErrorSendsNo(t *testing.T) {
	m, f, _ := projectFormModel(t)
	f.passErr, f.passErrKeeps = errors.New("the server isn't connected"), true
	ask(m, passReq(m))
	settle(m, press(m, "y"))
	wantText(t, m, "The server isn't connected")
	settle(m, press(m, "esc"))
	if m.form != nil {
		t.Fatalf("form %T still open", m.form)
	}
	if got := f.passthroughCalls(); !slices.Equal(got, []string{"answer r1 1", "answer r1 0"}) {
		t.Fatalf("calls %v, want one No after the failed Yes", got)
	}
}
