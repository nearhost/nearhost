package cli

import (
	"io"
	"strings"

	"rsc.io/qr"
)

// qrColour makes a line of the code black on white, so a scanner reads it
// on a dark terminal too.
const (
	qrColour = "\x1b[30;47m"
	qrReset  = "\x1b[0m"
)

// renderQR writes text as a QR code, two modules to a character with ▀▄█
// and spaces, inside a quiet zone of four modules. With colour set, each
// line is black on white, as the code must be dark on light to scan; without
// it the glyphs are plain.
func renderQR(w io.Writer, text string, colour bool) error {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return err
	}
	const quiet = 4
	size := code.Size + 2*quiet
	dark := func(x, y int) bool {
		x, y = x-quiet, y-quiet
		return x >= 0 && y >= 0 && x < code.Size && y < code.Size && code.Black(x, y)
	}
	var b strings.Builder
	for y := 0; y < size; y += 2 {
		if colour {
			b.WriteString(qrColour)
		}
		for x := 0; x < size; x++ {
			top, bottom := dark(x, y), dark(x, y+1)
			switch {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteByte(' ')
			}
		}
		if colour {
			b.WriteString(qrReset)
		}
		b.WriteByte('\n')
	}
	_, err = io.WriteString(w, b.String())
	return err
}
