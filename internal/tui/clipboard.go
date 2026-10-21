package tui

import (
	"os/exec"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// copyText puts text on the clipboard two ways: OSC 52, which the terminal
// handles, and the system's own tool, as Nearhost runs on the machine whose
// clipboard it is. Terminal.app has no OSC 52, and iTerm2 has it off unless
// it is allowed; the tool covers those, and OSC 52 covers a machine without
// one.
func copyText(text string) tea.Cmd {
	return tea.Batch(tea.SetClipboard(text), func() tea.Msg {
		_ = systemClipboard(text) // OSC 52 may well have done it
		return nil
	})
}

// systemClipboard copies text with pbcopy on macOS, or with the first of
// wl-copy, xclip and xsel found elsewhere. Tests replace it, so they leave
// the clipboard alone.
var systemClipboard = func(text string) error {
	tools := [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}}
	if runtime.GOOS == "darwin" {
		tools = [][]string{{"pbcopy"}}
	}
	for _, t := range tools {
		if _, err := exec.LookPath(t[0]); err != nil {
			continue
		}
		cmd := exec.Command(t[0], t[1:]...)
		cmd.Stdin = strings.NewReader(text)
		return cmd.Run()
	}
	return nil
}
