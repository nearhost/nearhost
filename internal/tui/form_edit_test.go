package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// fieldKey presses a key by name or types text on the field.
func fieldKey(f *textField, k string) {
	switch k {
	case "left", "right", "home", "end", "delete", "backspace", "ctrl+a", "ctrl+e", "ctrl+u":
		code := map[string]rune{"left": tea.KeyLeft, "right": tea.KeyRight, "home": tea.KeyHome, "end": tea.KeyEnd,
			"delete": tea.KeyDelete, "backspace": tea.KeyBackspace}[k]
		mod := tea.KeyMod(0)
		if strings.HasPrefix(k, "ctrl+") {
			code, mod = rune(k[5]), tea.ModCtrl
		}
		f.key(tea.KeyPressMsg{Code: code, Mod: mod})
	default:
		f.key(tea.KeyPressMsg{Code: []rune(k)[0], Text: k})
	}
}

func fieldWith(s string) *textField {
	f := &textField{}
	f.set(s)
	return f
}

func TestFieldInsertsInTheMiddle(t *testing.T) {
	f := fieldWith("helo")
	fieldKey(f, "left")
	fieldKey(f, "left")
	fieldKey(f, "l")
	if f.value != "hello" || f.cur != 3 {
		t.Errorf("got %q at %d", f.value, f.cur)
	}
	fieldKey(f, "right")
	fieldKey(f, "right")
	fieldKey(f, "right") // at the end: stays
	if f.cur != 5 {
		t.Errorf("cursor %d, want 5", f.cur)
	}
}

func TestFieldDeletesForwardAndBack(t *testing.T) {
	f := fieldWith("abcd")
	fieldKey(f, "home")
	fieldKey(f, "delete")
	if f.value != "bcd" || f.cur != 0 {
		t.Errorf("delete: got %q at %d", f.value, f.cur)
	}
	fieldKey(f, "backspace") // at the start: nothing
	if f.value != "bcd" || f.cur != 0 {
		t.Errorf("backspace at 0: got %q at %d", f.value, f.cur)
	}
	fieldKey(f, "end")
	fieldKey(f, "delete") // at the end: nothing
	if f.value != "bcd" || f.cur != 3 {
		t.Errorf("delete at end: got %q at %d", f.value, f.cur)
	}
	fieldKey(f, "left")
	fieldKey(f, "backspace")
	if f.value != "bd" || f.cur != 1 {
		t.Errorf("backspace mid: got %q at %d", f.value, f.cur)
	}
}

func TestFieldHomeThenType(t *testing.T) {
	for _, home := range []string{"home", "ctrl+a"} {
		f := fieldWith("world")
		fieldKey(f, home)
		f.paste("hello ")
		if f.value != "hello world" || f.cur != 6 {
			t.Errorf("%s: got %q at %d", home, f.value, f.cur)
		}
		for _, end := range []string{"end", "ctrl+e"} {
			fieldKey(f, "home")
			fieldKey(f, end)
			fieldKey(f, "!")
			if f.value != "hello world!" {
				t.Errorf("%s: got %q", end, f.value)
			}
			f.set("hello world")
		}
	}
}

func TestFieldKeepsCharactersWhole(t *testing.T) {
	f := fieldWith("aé世😀z")
	fieldKey(f, "left")
	fieldKey(f, "backspace") // the emoji
	if f.value != "aé世z" || f.cur != 3 {
		t.Fatalf("got %q at %d", f.value, f.cur)
	}
	fieldKey(f, "backspace") // the CJK character
	fieldKey(f, "backspace") // é
	if f.value != "az" || f.cur != 1 {
		t.Fatalf("got %q at %d", f.value, f.cur)
	}
	f.set("é世😀")
	fieldKey(f, "home")
	fieldKey(f, "delete")
	fieldKey(f, "delete")
	if f.value != "😀" {
		t.Errorf("got %q", f.value)
	}
	fieldKey(f, "é")
	fieldKey(f, "right")
	if f.value != "é😀" || f.cur != 2 {
		t.Errorf("got %q at %d", f.value, f.cur)
	}
}

// The cursor is drawn on the character under it, by cell: the text keeps
// its width, and a wide character is not cut.
func TestFieldViewPlacesTheCursorByCell(t *testing.T) {
	f := fieldWith("a世😀")
	if got := ansi.Strip(f.view(true)); got != "a世😀▏" {
		t.Errorf("at the end: %q", got)
	}
	fieldKey(f, "left")
	fieldKey(f, "left")
	got := f.view(true)
	if ansi.Strip(got) != "a世😀" {
		t.Errorf("on a character: %q", ansi.Strip(got))
	}
	if !strings.Contains(got, "\x1b[") || ansi.StringWidth(got) != 5 {
		t.Errorf("want the wide character marked and width 5, got %q (%d)", got, ansi.StringWidth(got))
	}
	if f.view(false) != "a世😀" {
		t.Errorf("unfocused: %q", f.view(false))
	}
}

// A long value wraps in the panel, so the cursor line is always shown,
// wherever the cursor is; at the end the tail shows as before.
func TestFieldLongValueKeepsTheCursorInView(t *testing.T) {
	long := strings.Repeat("abcdefghij", 12)
	f := fieldWith(long)
	for _, at := range []int{len(long), 0, 57} {
		f.cur = at
		lines := field("path", f.view(true), 30)
		var in []string
		for _, l := range lines {
			in = append(in, ansi.Strip(l)[labelWidth:])
		}
		joined := strings.Join(in, "")
		want := long
		if at == len(long) {
			want += "▏"
		}
		if joined != want {
			t.Errorf("cursor %d: got %q", at, joined)
		}
	}
}

func TestFieldSetClampsTheCursor(t *testing.T) {
	f := fieldWith("a long value")
	f.set("ab")
	if f.cur != 2 {
		t.Errorf("set: cursor %d, want 2", f.cur)
	}
	f.value = "" // a direct write must not make the next key panic
	fieldKey(f, "backspace")
	fieldKey(f, "delete")
	fieldKey(f, "left")
	fieldKey(f, "x")
	if f.value != "x" || f.cur != 1 {
		t.Errorf("got %q at %d", f.value, f.cur)
	}
	f.cur = -5
	fieldKey(f, "y")
	if f.value != "yx" {
		t.Errorf("negative cursor: %q", f.value)
	}
}

func TestFieldPasteInsertsAtTheCursor(t *testing.T) {
	f := fieldWith("ac")
	fieldKey(f, "left")
	f.paste("b\n\x1b")
	if f.value != "abc" || f.cur != 2 {
		t.Errorf("got %q at %d", f.value, f.cur)
	}
}

// Completion works with the cursor at the end of the path and is off
// elsewhere: no ghost text, and tab does nothing. Accepting puts the cursor
// at the end.
func TestPathCompletionNeedsTheCursorAtTheEnd(t *testing.T) {
	m, _, _ := projectFormModel(t)
	toPath(t, m, 1)
	pf := m.form.(*projectForm)
	settle(m, press(m, "tab"))
	typeText(m, "b")
	if !strings.Contains(pathLine(m), "b▏lob/") {
		t.Fatalf("no ghost text at the end: %q", pathLine(m))
	}
	press(m, "left")
	if got := pathLine(m); strings.Contains(got, "lob/") || pf.suggestion() != "" {
		t.Errorf("ghost text mid-string: %q", got)
	}
	before := pf.path.value
	settle(m, press(m, "tab"))
	if pf.path.value != before {
		t.Errorf("completion ran mid-string: %q", pf.path.value)
	}
	typeText(m, "X") // in the middle of the path
	if pf.path.value != "~/code/Xb" {
		t.Errorf("typed in the middle: %q", pf.path.value)
	}
	press(m, "end")
	press(m, "backspace")
	press(m, "backspace")
	typeText(m, "b")
	settle(m, press(m, "tab"))
	if pf.path.value != "~/code/blob/" {
		t.Errorf("tab at the end: %q", pf.path.value)
	}
	if !pf.path.atEnd() {
		t.Errorf("accepting left the cursor at %d", pf.path.cur)
	}
}

// Pasted text is text, even when it spells a key's name.
func TestFieldPasteOfAKeyNameInserts(t *testing.T) {
	for _, name := range []string{"home", "end", "left", "right", "delete", "backspace", "ctrl+a"} {
		f := fieldWith("ab")
		fieldKey(f, "left")
		f.paste(name)
		if want := "a" + name + "b"; f.value != want || f.cur != 1+len(name) {
			t.Errorf("%s: got %q at %d", name, f.value, f.cur)
		}
	}
}

// A key reports a change of the text only; a move is not one.
func TestFieldKeyReportsChangesOnly(t *testing.T) {
	f := fieldWith("ab")
	for _, k := range []string{"left", "right", "home", "end", "ctrl+a", "ctrl+e", "delete"} {
		if f.key(tea.KeyPressMsg{Code: tea.KeyLeft}) && k == "left" {
			t.Errorf("a move reported a change")
		}
	}
	fieldKey(f, "end")
	if !f.key(tea.KeyPressMsg{Code: tea.KeyBackspace}) || f.value != "a" {
		t.Errorf("backspace: %q", f.value)
	}
}

// A format character under the cursor still shows a cursor.
func TestFieldViewShowsACursorOnAFormatCharacter(t *testing.T) {
	f := &textField{value: "a​b", cur: 1}
	if got := f.view(true); !strings.Contains(got, "\x1b[") || ansi.StringWidth(got) != 3 {
		t.Errorf("got %q (%d)", got, ansi.StringWidth(got))
	}
}

// On the path step left and right move the cursor, right accepts a
// suggestion only at the end, and a move does not reset the completion.
func TestPathStepMovesTheCursor(t *testing.T) {
	m, _, _ := projectFormModel(t)
	toPath(t, m, 1)
	pf := m.form.(*projectForm)
	settle(m, press(m, "tab"))
	typeText(m, "b")
	press(m, "down") // choose blog
	press(m, "left")
	if pf.path.cur != len(pf.path.value)-1 {
		t.Fatalf("left: cursor %d of %q", pf.path.cur, pf.path.value)
	}
	press(m, "right")
	if !pf.path.atEnd() || pf.chosen != 1 {
		t.Errorf("right: cursor %d, chosen %d", pf.path.cur, pf.chosen)
	}
	settle(m, press(m, "right")) // at the end with a suggestion: accepts
	if pf.path.value != "~/code/blog/" {
		t.Errorf("right at the end: %q", pf.path.value)
	}
	press(m, "left")
	settle(m, press(m, "tab"))
	wantScreen(t, m, "Move to the end of the path to complete.")
}
