package cli

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"

	"rsc.io/qr"
)

func TestRenderQRIsSquare(t *testing.T) {
	const text = "https://nearhost.x/pair/ABCD2345"
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		t.Fatal(err)
	}
	rows := code.Size + 8
	var out bytes.Buffer
	if err := renderQR(&out, text, false); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if want := (rows + 1) / 2; len(lines) != want {
		t.Fatalf("%d lines, want %d", len(lines), want)
	}
	for i, l := range lines {
		if n := utf8.RuneCountInString(l); n != rows {
			t.Errorf("line %d has %d runes, want %d", i, n, rows)
		}
	}
	if strings.TrimLeft(lines[0], " ") != "" {
		t.Errorf("the quiet zone's first line is %q, want blank", lines[0])
	}
}

func TestRenderQRColour(t *testing.T) {
	const text = "https://nearhost.x/pair/ABCD2345"
	var plain, tty bytes.Buffer
	if err := renderQR(&plain, text, false); err != nil {
		t.Fatal(err)
	}
	if err := renderQR(&tty, text, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain.String(), "\x1b") {
		t.Errorf("plain output has an escape")
	}
	if !strings.HasPrefix(tty.String(), "\x1b[30;47m") {
		t.Errorf("terminal output starts %q, want the black-on-white escape", tty.String()[:12])
	}
	for _, l := range strings.Split(strings.TrimSuffix(tty.String(), "\n"), "\n") {
		if !strings.HasPrefix(l, "\x1b[30;47m") || !strings.HasSuffix(l, "\x1b[0m") {
			t.Errorf("line %q is not wrapped in the colour", l)
		}
	}
}
