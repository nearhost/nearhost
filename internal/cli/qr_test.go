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
	rows := code.Size + 4
	var out bytes.Buffer
	if err := renderQR(&out, text); err != nil {
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
