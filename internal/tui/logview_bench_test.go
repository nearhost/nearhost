package tui

import (
	"testing"

	"charm.land/bubbles/v2/viewport"
)

// benchLog is a 120×40 model with a 2000-entry log scrolled to its middle
// with a request open, as it is while someone reads back through a busy log.
func benchLog(b *testing.B) *Model {
	b.Helper()
	m := goldenModel(b, 120, 40, homeKey, 2000)
	press(m, "g")
	for i := 0; i < 30; i++ {
		press(m, "pgdown")
	}
	shown := m.shown()
	m.logView.open = shown[m.logView.at(shown)+1].seq
	return m
}

// BenchmarkLogView2000 draws one frame of a 2000-entry log.
func BenchmarkLogView2000(b *testing.B) {
	m := benchLog(b)
	m.logScreen()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.logScreen()
	}
}

// BenchmarkLogView2000Old draws the same frame as the log was drawn before
// it rendered only the rows on screen.
func BenchmarkLogView2000Old(b *testing.B) {
	m := benchLog(b)
	vp := viewport.New()
	refLogScreen(m, &vp)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		refLogScreen(m, &vp)
	}
}

// BenchmarkLogView2000Append draws a frame after a new line arrived, as a
// busy dev server makes it happen.
func BenchmarkLogView2000Append(b *testing.B) {
	m := benchLog(b)
	m.logScreen()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		addGolden(m, 2000+i, 1)
		m.logScreen()
	}
}

// BenchmarkLogView2000AppendOld is BenchmarkLogView2000Append for the log as
// it was drawn before.
func BenchmarkLogView2000AppendOld(b *testing.B) {
	m := benchLog(b)
	vp := viewport.New()
	refLogScreen(m, &vp)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		addGolden(m, 2000+i, 1)
		refLogScreen(m, &vp)
	}
}
