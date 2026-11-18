package safetext

import "testing"

func TestClean(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Test stack", "Test stack"},
		// Text in other scripts, emoji and the joiners and spaces they use
		// are kept as they are.
		{"Café Ünïcode 日本語", "Café Ünïcode 日本語"},
		{"管理　画面", "管理　画面"},
		{"a b", "a b"},
		{"family 👨‍👩‍👧 ❤️", "family 👨‍👩‍👧 ❤️"},
		{"می‌خواهم", "می‌خواهم"},
		{"  padded  ", "  padded  "},
		// Control characters go: an OSC 52 clipboard write, a title
		// change, a colour, a right-to-left override, a line separator.
		{"a\x1b]52;c;AAAA\ab", "a]52;c;AAAAb"},
		{"\x1b]0;title\x07x", "]0;titlex"},
		{"\x1b[31mred\x1b[0m", "[31mred[0m"},
		{"ab‮dc", "abdc"},
		{"a b", "ab"},
		{"a\x00b\x7fc\u0085d", "abcd"},
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
		{"日本語 ❤️\n\x1b]52;c;AAAA\a", "日本語 ❤️\n]52;c;AAAA"},
		{"ab‮dc", "abdc"},
	}
	for _, tc := range cases {
		if got := Lines(tc.in); got != tc.want {
			t.Errorf("Lines(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
