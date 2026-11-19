package tui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/viewport"
	"github.com/charmbracelet/x/ansi"
)

// This file keeps the full-screen log as it was drawn before it rendered
// only the rows on screen: it filters on every call and renders every entry
// to styled text before cutting out the window. The golden tests compare
// the live code against it, and the benchmarks time both.

// refEntryLines is entryLines as it was.
func refEntryLines(e logEntry, width int, sources bool) []string {
	lead := styleDim.Render(e.at.Format("15:04:05")) + "  "
	room := width - logTextAt
	if room < 8 {
		return []string{fit(lead+e.plain(), width)}
	}
	if e.req != nil {
		method := clean(e.req.Method)
		if sources {
			text := method + " " + requestText(*e.req, room-len(method)-1)
			return []string{fit(lead+fit(e.source, logSourceWidth)+"  "+text, width)}
		}
		return []string{fit(lead+fit(method, logSourceWidth)+"  "+requestText(*e.req, room), width)}
	}
	text := e.text
	if e.err {
		text = styleRed.Render(text)
	}
	var out []string
	for i, l := range wrap(text, room) {
		if i == 0 {
			out = append(out, fit(lead+fit(e.source, logSourceWidth)+"  "+l, width))
			continue
		}
		out = append(out, fit(strings.Repeat(" ", logTextAt)+l, width))
	}
	return out
}

// refShown is shown as it was: it filters the log on every call.
func refShown(m *Model) []logEntry {
	v := m.logView
	var out []logEntry
	for _, e := range m.logs.entries(v.key, strings.TrimSpace(v.search.Value())) {
		if v.kind.shows(e) {
			out = append(out, e)
		}
	}
	return out
}

// refLogTabs is logTabs as it was.
func refLogTabs(m *Model) string {
	v := m.logView
	var parts []string
	for i, name := range kindNames {
		text := " " + name + " "
		if logKind(i) == kindErrors {
			errs := 0
			for _, e := range m.logs.entries(v.key, "") {
				if e.err {
					errs++
				}
			}
			if errs > 0 {
				text = " " + name + " " + strconv.Itoa(errs) + " "
			}
		}
		if logKind(i) == v.kind {
			if m.monochrome {
				text = "[" + strings.TrimSuffix(strings.TrimPrefix(text, " "), " ") + "]"
			}
			parts = append(parts, styleSelected.Render(text))
		} else {
			parts = append(parts, styleDim.Render(text))
		}
	}
	tabs := strings.Join(parts, " ")
	if v.searching || v.search.Value() != "" {
		typed := v.search.View()
		if !v.searching {
			typed = v.search.Value()
		}
		tabs += "    " + styleBeacon.Render("/") + " " + typed
	}
	return tabs
}

// refLogCount is logCount as it was.
func refLogCount(m *Model, shown int) string {
	v := m.logView
	total := len(m.logs.entries(v.key, ""))
	count := fmt.Sprintf("%d lines", total)
	if v.search.Value() != "" || v.kind != kindAll {
		count = fmt.Sprintf("%d of %d lines", shown, total)
	}
	state := "following"
	if !v.follow {
		state = "paused"
	}
	return styleDim.Render(count + " · " + state)
}

// refLogScreen is logScreen as it was. It scrolls vp, which stands for the
// log view's own viewport, so it keeps its place from one frame to the next.
func refLogScreen(m *Model, vp *viewport.Model) string {
	v := m.logView
	width := m.width - 3
	shown := refShown(m)
	at := v.at(shown)
	sources := !strings.HasPrefix(v.key, "project:")
	var lines []string
	cursorLine := 0
	for i, e := range shown {
		if i == at {
			cursorLine = len(lines)
		}
		el := refEntryLines(e, width-1, sources)
		if !v.wrap {
			el = el[:1]
		}
		for j, l := range el {
			lead := " "
			if i == at && j == 0 {
				lead = styleBeacon.Render("▌")
			}
			lines = append(lines, lead+l)
		}
		if e.req != nil && e.seq == v.open {
			lines = append(lines, m.requestLines(e, width)...)
		}
	}
	foot := 1
	if m.showHelp {
		foot = logHelpRows(m.width)
	}
	height := max(m.height-5-foot, 1)
	newer := 0
	if !v.follow {
		for _, e := range shown {
			if e.seq > v.pausedAt {
				newer++
			}
		}
	}
	if newer > 0 || v.confirm != 0 || v.said != "" {
		height--
	}
	vp.SetWidth(width)
	vp.SetHeight(height)
	vp.SetContentLines(lines)
	if v.follow {
		vp.GotoBottom()
	} else {
		vp.EnsureVisible(cursorLine, 0, 0)
	}
	body := strings.Split(vp.View(), "\n")
	bar := scrollbar(vp.YOffset(), len(lines), height)
	out := []string{m.bar(m.logCrumbs(), m.logState()), rule(m.width), m.bar(refLogTabs(m), refLogCount(m, len(shown))), strings.Repeat(" ", m.width)}
	for i := 0; i < height; i++ {
		l := ""
		if i < len(body) {
			l = body[i]
		}
		out = append(out, " "+fit(l, width)+bar[i]+" ")
	}
	switch {
	case v.confirm != 0:
		out = append(out, m.bar(styleAmber.Render(m.confirmText()),
			styleBeacon.Render("y")+" "+styleDim.Render("send · any other key keeps it")))
	case v.said != "":
		out = append(out, m.bar(v.said, ""))
	case newer > 0:
		out = append(out, m.bar(styleAmber.Render(fmt.Sprintf("▼ %d new lines below", newer)),
			styleBeacon.Render("G")+" "+styleDim.Render("jump to the end and follow")))
	}
	out = append(out, rule(m.width))
	if m.showHelp {
		return strings.Join(append(out, m.logHelp()), "\n")
	}
	right := styleDim.Render(m.logName())
	m.help.SetWidth(m.width - 3 - ansi.StringWidth(m.logName()))
	return strings.Join(append(out, m.bar(m.help.ShortHelpView(bindings(logFooterItems())), right)), "\n")
}
