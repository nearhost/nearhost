package safetext

import "testing"

func TestClean(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Test stack", "Test stack"},
		// Text in other scripts, emoji and the joiners and spaces they use
		// are kept as they are.
		{"Café Ünïcode 日本語", "Café Ünïcode 日本語"},
		{"管理\u3000画面", "管理\u3000画面"},
		{"a\u00a0b", "a\u00a0b"},
		{"family 👨\u200d👩\u200d👧 ❤\ufe0f", "family 👨\u200d👩\u200d👧 ❤\ufe0f"},
		{"می\u200cخواهم", "می\u200cخواهم"},
		{"  padded  ", "  padded  "},
		// Control characters go: an OSC 52 clipboard write, a title
		// change, a colour, a right-to-left override, a line separator.
		{"a\x1b]52;c;AAAA\ab", "a]52;c;AAAAb"},
		{"\x1b]0;title\x07x", "]0;titlex"},
		{"\x1b[31mred\x1b[0m", "[31mred[0m"},
		{"ab\u202edc", "abdc"},
		{"a\u2028b", "ab"},
		{"a\x00b\x7fc\u0085d", "abcd"},
		// C1 controls a terminal reads as CSI and OSC, without an ESC.
		{"a\u009b31mb", "a31mb"},
		{"a\u009d52;c;AAAA\u009cb", "a52;c;AAAAb"},
		// Invisible format characters: a zero-width space, a byte order
		// mark, a left-to-right isolate.
		{"a\u200bb\ufeffc\u2066d", "abcd"},
		// Bytes that are not UTF-8 go, as strings.Map reads each as U+FFFD
		// and that is kept: it shows something was there.
		{"a\xffb\xc3", "a\ufffdb\ufffd"},
		// Line breaks and tabs become spaces, so words stay apart.
		{"a\nb", "a b"},
		{"a\tb", "a b"},
		{"a\rb", "a b"},
	}
	for _, tc := range cases {
		if got := Clean(tc.in); got != tc.want {
			t.Errorf("Clean(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Test stack", "Test stack"},
		{"a\nb", "a b"},
		{"  padded \n", "padded"},
		{"\x1b]52;c;AAAA\a", "]52;c;AAAA"},
	}
	for _, tc := range cases {
		if got := Line(tc.in); got != tc.want {
			t.Errorf("Line(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLines(t *testing.T) {
	cases := []struct{ in, want string }{
		{"first\n\tsecond\n", "first\n\tsecond\n"},
		{"crlf\r\nline", "crlf\nline"},
		{"日本語 ❤\ufe0f\n\x1b]52;c;AAAA\a", "日本語 ❤\ufe0f\n]52;c;AAAA"},
		{"ab\u202edc", "abdc"},
	}
	for _, tc := range cases {
		if got := Lines(tc.in); got != tc.want {
			t.Errorf("Lines(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
