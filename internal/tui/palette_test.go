package tui

import (
	"fmt"
	"image/color"
	"math"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/nearhost/nearhost/internal/engine"
)

// lightTerminal is the answer of a terminal with a white background.
var lightTerminal = tea.BackgroundColorMsg{Color: color.RGBA{0xff, 0xff, 0xff, 0xff}}

// darkTerminal is the answer of a terminal with a black background.
var darkTerminal = tea.BackgroundColorMsg{Color: color.RGBA{0, 0, 0, 0xff}}

// fg is the escape sequence that sets the foreground to hex.
func fg(hex string) string {
	r, g, b, _ := lipgloss.Color(hex).RGBA()
	return fmt.Sprintf("38;2;%d;%d;%d", r>>8, g>>8, b>>8)
}

// restoreDark puts the dark palette back once the test ends, so the tests
// after it draw as they always have. Tests of this package do not run in
// parallel, so changing the palette races with nothing.
func restoreDark(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { usePalette(darkPalette) })
}

// The model asks the terminal for its background when it starts.
func TestInitAsksForTheBackground(t *testing.T) {
	m := sized(newFake(t, sampleConfig()))
	batch, ok := m.Init()().(tea.BatchMsg)
	if !ok {
		t.Fatal("Init does not return a batch")
	}
	want := reflect.ValueOf(tea.RequestBackgroundColor).Pointer()
	for _, cmd := range batch {
		if cmd != nil && reflect.ValueOf(cmd).Pointer() == want {
			return
		}
	}
	t.Error("Init does not request the background colour")
}

// A light background selects the light palette, and the view draws its
// body text in the light body colour.
func TestLightBackgroundSelectsLightPalette(t *testing.T) {
	restoreDark(t)
	m := sized(newFake(t, sampleConfig()))
	m.Update(lightTerminal)
	if colourText != lightPalette.text || colourDim != lightPalette.dim || colourBeacon != lightPalette.beacon {
		t.Fatalf("the colours are %v %v %v, want the light palette", colourText, colourDim, colourBeacon)
	}
	view := m.View().Content
	if !strings.Contains(view, fg("#1F2420")) {
		t.Error("the view does not draw the light body colour")
	}
	if strings.Contains(view, fg("#ECEFE8")) {
		t.Error("the view still draws the dark body colour")
	}
}

// A dark background keeps the dark palette.
func TestDarkBackgroundKeepsDarkPalette(t *testing.T) {
	restoreDark(t)
	m := sized(newFake(t, sampleConfig()))
	m.Update(darkTerminal)
	if colourText != darkPalette.text {
		t.Fatalf("the body colour is %v, want the dark one", colourText)
	}
	if view := m.View().Content; !strings.Contains(view, fg("#ECEFE8")) || strings.Contains(view, fg("#1F2420")) {
		t.Error("the view does not draw in the dark palette")
	}
}

// A terminal that never answers keeps the dark palette, and an answer that
// comes after the wait is over changes nothing.
func TestNoAnswerKeepsDarkPalette(t *testing.T) {
	restoreDark(t)
	m := sized(newFake(t, sampleConfig()))
	if colourText != darkPalette.text {
		t.Fatalf("the body colour is %v before any answer, want the dark one", colourText)
	}
	m.Update(backgroundWaitedMsg{})
	m.Update(lightTerminal)
	if colourText != darkPalette.text {
		t.Errorf("the body colour is %v after a late answer, want the dark one", colourText)
	}
}

// Only the first answer counts.
func TestOnlyTheFirstAnswerCounts(t *testing.T) {
	restoreDark(t)
	m := sized(newFake(t, sampleConfig()))
	m.Update(lightTerminal)
	m.Update(darkTerminal)
	if colourText != lightPalette.text {
		t.Errorf("the body colour is %v, want the light one", colourText)
	}
}

// What is drawn once and kept follows the palette too: the mark, the
// footer's help, the filter and the status dots.
func TestLightPaletteReachesKeptStyles(t *testing.T) {
	restoreDark(t)
	m := sized(newFake(t, sampleConfig()))
	m.Update(lightTerminal)
	if !strings.Contains(markLines[0], fg("#4D6B00")) || !strings.Contains(shortMarkLines[0], fg("#4D6B00")) {
		t.Error("the mark is not drawn in the light accent")
	}
	if m.help.Styles.ShortKey.GetForeground() != lightPalette.beacon {
		t.Error("the footer's keys are not in the light accent")
	}
	if m.filter.Styles().Focused.Text.GetForeground() != lightPalette.text {
		t.Error("the filter's text is not in the light body colour")
	}
	if got := stateMark(engine.StateRunning).style.GetForeground(); got != lightPalette.running {
		t.Errorf("a running project's dot is %v, want the light running colour", got)
	}
	if got := styleBox.GetForeground(); got != lightPalette.boxEdge {
		t.Errorf("the scrollbar is %v, want the light box edge", got)
	}
}

// The light palette is the one the brief asks for, and the dark palette is
// unchanged.
func TestPaletteValues(t *testing.T) {
	for _, c := range []struct {
		name      string
		got, want color.Color
	}{
		{"light text", lightPalette.text, lipgloss.Color("#1F2420")},
		{"light muted", lightPalette.dim, lipgloss.Color("#5B635A")},
		{"light accent", lightPalette.beacon, lipgloss.Color("#4D6B00")},
		{"light error", lightPalette.red, lipgloss.Color("#B3261E")},
		{"light warning", lightPalette.amber, lipgloss.Color("#8A5A00")},
		{"light running", lightPalette.running, lipgloss.Color("#1B7F3B")},
		{"dark ground", darkPalette.ground, lipgloss.Color("#0F1210")},
		{"dark accent", darkPalette.beacon, lipgloss.Color("#D6F26B")},
		{"dark text", darkPalette.text, lipgloss.Color("#ECEFE8")},
		{"dark muted", darkPalette.dim, lipgloss.Color("#7C8479")},
		{"dark warning", darkPalette.amber, lipgloss.Color("#F2B84B")},
		{"dark error", darkPalette.red, lipgloss.Color("#F26B6B")},
		{"dark running", darkPalette.running, lipgloss.Color("#D6F26B")},
		{"dark line", darkPalette.line, lipgloss.Color("#2A312B")},
		{"dark selected", darkPalette.selected, lipgloss.Color("#1F261F")},
		{"dark faint", darkPalette.faint, lipgloss.Color("#3A413B")},
		{"dark box edge", darkPalette.boxEdge, lipgloss.Color("#4A5449")},
		{"dark warning edge", darkPalette.warnEdge, lipgloss.Color("#6B5524")},
	} {
		if !sameColour(c.got, c.want) {
			t.Errorf("%s is %v, want %v", c.name, c.got, c.want)
		}
	}
}

// Text in the light palette reads at 4.5:1 or better on white and on the
// selected row, and the selected tab's text reads on the accent. Box edges
// differ from muted text, so a click can tell an edge from a "│" in a log.
func TestLightPaletteContrast(t *testing.T) {
	p := lightPalette
	white := lipgloss.Color("#FFFFFF")
	for name, c := range map[string]color.Color{
		"text": p.text, "muted": p.dim, "accent": p.beacon, "error": p.red, "warning": p.amber, "running": p.running,
	} {
		for bgName, bg := range map[string]color.Color{"white": white, "the selected row": p.selected} {
			if r := contrast(c, bg); r < 4.5 {
				t.Errorf("%s on %s is %.2f:1, want at least 4.5:1", name, bgName, r)
			}
		}
	}
	if r := contrast(p.ground, p.beacon); r < 4.5 {
		t.Errorf("the selected tab's text is %.2f:1 on the accent, want at least 4.5:1", r)
	}
	for _, edge := range []color.Color{p.line, p.boxEdge, p.warnEdge} {
		for _, text := range []color.Color{p.text, p.dim, p.beacon, p.red, p.amber, p.running} {
			if sameColour(edge, text) {
				t.Errorf("the edge colour %v is also a text colour", edge)
			}
		}
	}
}

// contrast is the WCAG 2 contrast ratio of a and b.
func contrast(a, b color.Color) float64 {
	la, lb := luminance(a), luminance(b)
	return (math.Max(la, lb) + 0.05) / (math.Min(la, lb) + 0.05)
}

// luminance is the WCAG 2 relative luminance of c.
func luminance(c color.Color) float64 {
	r, g, b, _ := c.RGBA()
	lin := func(v uint32) float64 {
		s := float64(v>>8) / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}
