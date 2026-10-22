package tui

import (
	"bytes"
	"testing"
)

func TestUTF16LE(t *testing.T) {
	for _, tc := range []struct {
		text string
		want []byte
	}{
		{"café", []byte{0xFF, 0xFE, 0x63, 0x00, 0x61, 0x00, 0x66, 0x00, 0xE9, 0x00}},
		// Outside the Basic Multilingual Plane: the surrogate pair D83D DE00.
		{"😀", []byte{0xFF, 0xFE, 0x3D, 0xD8, 0x00, 0xDE}},
	} {
		if got := utf16LE(tc.text); !bytes.Equal(got, tc.want) {
			t.Errorf("utf16LE(%q) = % X, want % X", tc.text, got, tc.want)
		}
	}
}
