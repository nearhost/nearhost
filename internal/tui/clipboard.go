package tui

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os/exec"
	"unicode/utf16"

	tea "charm.land/bubbletea/v2"
)

// copyText puts text from the row key on the clipboard two ways: OSC 52, which the terminal
// handles, and the system's own tool, as Nearhost runs on the machine whose
// clipboard it is. Terminal.app has no OSC 52, and iTerm2 has it off unless
// it is allowed; the tool covers those, and OSC 52 covers a machine without
// one.
func copyText(key, text string) tea.Cmd {
	return tea.Batch(tea.SetClipboard(text), func() tea.Msg {
		if err := systemClipboard(text); err != nil {
			return copyFailedMsg{key, err}
		}
		return nil
	})
}

// copyFailedMsg says the system's clipboard tool failed on a copy from the
// row key. OSC 52 may still have copied, which nothing can tell.
type copyFailedMsg struct {
	key string
	err error
}

// copyWith copies text with the first of tools, each a command and its
// arguments, that is installed and works: wl-copy fails outside Wayland,
// where xclip may not. With none installed, it does nothing.
func copyWith(tools [][]string, text string) error {
	return copyWithInput(tools, []byte(text))
}

// copyWithInput is copyWith for a tool that wants input encoded as given.
func copyWithInput(tools [][]string, input []byte) error {
	var err error
	for _, t := range tools {
		if _, look := exec.LookPath(t[0]); look != nil {
			continue
		}
		cmd := exec.Command(t[0], t[1:]...)
		cmd.Stdin = bytes.NewReader(input)
		if err = cmd.Run(); err == nil {
			return nil
		}
		err = fmt.Errorf("%s: %w", t[0], err)
	}
	return err
}

// utf16LE is text as UTF-16, little-endian, after a byte order mark: what
// Windows' clip.exe reads as Unicode rather than the console's code page.
func utf16LE(text string) []byte {
	out := []byte{0xFF, 0xFE}
	for _, unit := range utf16.Encode([]rune(text)) {
		out = binary.LittleEndian.AppendUint16(out, unit)
	}
	return out
}
