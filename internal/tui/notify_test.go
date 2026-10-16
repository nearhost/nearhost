package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func envOf(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestNotificationSuitsTheTerminal(t *testing.T) {
	for name, tc := range map[string]struct {
		env  map[string]string
		want string
	}{
		"iTerm2":   {map[string]string{"TERM_PROGRAM": "iTerm.app"}, "\x1b]9;Agent on mini wants to add /srv/app\x07"},
		"Ghostty":  {map[string]string{"TERM_PROGRAM": "ghostty"}, "\x1b]9;Agent on mini wants to add /srv/app\x07"},
		"WezTerm":  {map[string]string{"TERM_PROGRAM": "WezTerm"}, "\x1b]9;Agent on mini wants to add /srv/app\x07"},
		"kitty":    {map[string]string{"TERM": "xterm-kitty"}, "\x1b]99;;Agent on mini wants to add /srv/app\x1b\\"},
		"foot":     {map[string]string{"TERM": "foot"}, "\x1b]777;notify;Nearhost;Agent on mini wants to add /srv/app\x07"},
		"Terminal": {map[string]string{"TERM_PROGRAM": "Apple_Terminal"}, "\a"},
		"tmux": {map[string]string{"TMUX": "/tmp/tmux-501/default,1,0", "TERM_PROGRAM": "tmux"},
			"\x1bPtmux;\x1b\x1b]9;Agent on mini wants to add /srv/app\x07\x1b\\\a"},
	} {
		if got := notification(envOf(tc.env), "Agent on mini wants to add /srv/app"); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

func TestNotificationTextCannotEndTheSequence(t *testing.T) {
	got := notification(envOf(map[string]string{"TERM": "foot"}), "a\x07b\x1b\\c;d")
	if want := "\x1b]777;notify;Nearhost;a b \\c,d\x07"; got != want {
		t.Fatalf("%q, want %q", got, want)
	}
}

// raws are the escape sequences the commands that follow from cmd write.
func raws(cmd tea.Cmd) []string {
	if cmd == nil {
		return nil
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		var out []string
		for _, c := range msg {
			out = append(out, raws(c)...)
		}
		return out
	case tea.RawMsg:
		return []string{msg.Msg.(string)}
	}
	return nil
}

func TestAProposalNotifiesTheTerminal(t *testing.T) {
	m, _, _ := projectFormModel(t)
	m.getenv = envOf(map[string]string{"TERM_PROGRAM": "ghostty"})
	p := Proposal{ID: "p1", Origin: "mini", Path: "/srv/app", Expires: time.Now().Add(time.Minute)}
	_, cmd := m.Update(proposalMsg{p})
	if got := raws(cmd); len(got) != 1 || got[0] != "\x1b]9;Agent on mini wants to add /srv/app\x07" {
		t.Fatalf("%q", got)
	}
}

func TestNotificationsFalseKeepsTheTerminalQuiet(t *testing.T) {
	m, _, _ := projectFormModel(t)
	off := false
	m.cfg.Notifications = &off
	_, cmd := m.Update(proposalMsg{Proposal{ID: "p1", Origin: "mini", Path: "/srv/app", Expires: time.Now().Add(time.Minute)}})
	if got := raws(cmd); len(got) != 0 {
		t.Fatalf("%q", got)
	}
}
