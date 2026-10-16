package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// notify shows text as a notification of the terminal the interface runs
// in, unless the config turns notifications off. Most terminals show it only
// while their window or tab is not the one in front.
func (m *Model) notify(text string) tea.Cmd {
	if m.cfg.NotificationsOff() {
		return nil
	}
	return tea.Raw(notification(m.getenv, text))
}

// notification is the escape sequence that shows text as a notification in
// the terminal getenv describes: OSC 9 for iTerm2, Ghostty and WezTerm, OSC
// 99 for kitty, OSC 777 for foot and urxvt, and the bell for the rest.
// Inside tmux, which names itself in TERM_PROGRAM, OSC 9 goes through tmux's
// passthrough, followed by the bell for when tmux drops it.
func notification(getenv func(string) string, text string) string {
	text = notificationText(text)
	if getenv("TMUX") != "" {
		osc := "\x1b]9;" + text + "\x07"
		return "\x1bPtmux;" + strings.ReplaceAll(osc, "\x1b", "\x1b\x1b") + "\x1b\\" + "\a"
	}
	term := getenv("TERM")
	switch program := getenv("TERM_PROGRAM"); {
	case program == "iTerm.app" || program == "ghostty" || program == "WezTerm":
		return "\x1b]9;" + text + "\x07"
	case term == "xterm-kitty" || getenv("KITTY_WINDOW_ID") != "":
		return "\x1b]99;;" + text + "\x1b\\"
	case strings.HasPrefix(term, "foot") || strings.HasPrefix(term, "rxvt"):
		return "\x1b]777;notify;Nearhost;" + text + "\x07"
	}
	return "\a"
}

// notificationText is text with nothing that could end the sequence early:
// control characters become spaces, and semicolons, which OSC 777 splits
// on, commas.
func notificationText(text string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0:
			return ' '
		case r == ';':
			return ','
		}
		return r
	}, text)
}
