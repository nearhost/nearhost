// Package safetext makes text Nearhost did not write safe to print in a
// terminal. Such text comes from a project's files, a server, ssh or a
// registry, and can hold escape sequences that write to the clipboard,
// change the window title or forge lines of output. The full-screen
// interface has its own cleaning; this package is for the plain commands
// and for values cleaned before they are saved.
package safetext

import (
	"strings"
	"unicode"
)

// Joiners that emoji sequences and some scripts (Persian, the Indic ones)
// need. They are format characters, which are otherwise dropped.
const (
	zwnj = '‌'
	zwj  = '‍'
)

// keep reports whether r prints as itself: a letter, mark, number,
// punctuation, symbol or space, or a joiner. Control characters, the other
// format characters (such as a right-to-left override), line and paragraph
// separators, private-use and unassigned code points are not kept.
func keep(r rune) bool {
	return unicode.IsGraphic(r) || r == zwnj || r == zwj
}

// Clean returns s with its control characters removed: a tab, line feed or
// carriage return becomes a space, so the words around it stay apart, and
// the rest that keep refuses goes. Text without such characters comes back
// as it was, which keeps the hash of a cleaned value stable.
func Clean(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case !keep(r):
			return -1
		}
		return r
	}, s)
}

// Line returns s cleaned as Clean does, on one line, without the spaces
// around it.
func Line(s string) string {
	return strings.TrimSpace(Clean(s))
}

// Lines returns s with its control characters removed, keeping the line
// breaks and tabs that separate and indent its lines. A carriage return
// goes, so a Windows line ending becomes a plain one and no line can be
// written over another.
func Lines(s string) string {
	return strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && !keep(r) {
			return -1
		}
		return r
	}, s)
}
