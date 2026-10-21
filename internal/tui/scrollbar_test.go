package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nearhost/nearhost/internal/config"
)

// column is the cells of the body at x, top to bottom.
func column(m *Model, x int) string {
	var sb strings.Builder
	lines := strings.Split(screen(m), "\n")
	for y := bodyTop; y < bodyTop+m.bodyHeight(); y++ {
		sb.WriteString(ansi.Cut(lines[y], x, x+1))
	}
	return sb.String()
}

// A sidebar longer than the window draws its scrollbar's thumb on its
// border, where the rows shown are among all of them; one that fits has
// the plain border.
func TestSidebarScrollbar(t *testing.T) {
	m := sized(newFake(t, sampleConfig()))
	if got := column(m, m.sideWidth()); strings.Contains(got, thumb) {
		t.Errorf("a sidebar that fits has a thumb: %q", got)
	}
	m = sized(newFake(t, manyProjects(40)))
	border := []rune(column(m, m.sideWidth()))
	if border[0] != '┃' || border[len(border)-1] != '│' {
		t.Errorf("at the top, the border is %q, want the thumb at its top", string(border))
	}
	for i := 0; i < 41; i++ {
		press(m, "down")
	}
	border = []rune(column(m, m.sideWidth()))
	if border[0] != '│' || border[len(border)-1] != '┃' {
		t.Errorf("at the bottom, the border is %q, want the thumb at its bottom", string(border))
	}
}

// A panel log longer than its room draws a thumb in the cell at the right
// of its rows, which moves up as the wheel scrolls back; one that fits has
// none.
func TestPanelLogScrollbar(t *testing.T) {
	m := sized(newFake(t, sampleConfig()))
	if got := column(m, m.width-1); strings.Contains(got, thumb) {
		t.Errorf("a log that fits has a thumb: %q", got)
	}
	for i := 0; i < 200; i++ {
		m.logs.noteOn(m.sel, fmt.Sprintf("line %d", i))
	}
	right := []rune(column(m, m.width-1))
	if right[len(right)-1] != '┃' || right[0] != ' ' {
		t.Fatalf("following, the right edge is %q, want the thumb at the bottom", string(right))
	}
	for i := 0; i < 30; i++ {
		m.Update(tea.MouseWheelMsg{X: m.mainX() + 5, Y: bodyTop + 15, Button: tea.MouseWheelUp})
	}
	moved := []rune(column(m, m.width-1))
	if moved[len(moved)-1] == '┃' || !strings.ContainsRune(string(moved), '┃') {
		t.Errorf("scrolled back, the right edge is %q, want the thumb above the bottom", string(moved))
	}
}

// A drag in the main panel on a row where the sidebar's thumb is drawn
// still keeps to the panel.
func TestDragKeepsToThePanelBesideTheSidebarThumb(t *testing.T) {
	m := sized(newFake(t, manyProjects(40)))
	x, y := find(t, m, "proxy     ")
	if !strings.HasPrefix(column(m, m.sideWidth()), thumb) {
		t.Fatal("no thumb on the rows the drag crosses")
	}
	if got := clipboard(drag(m, x, y, m.width-1, y+2)); strings.ContainsAny(got, "│┃▾") || !strings.HasPrefix(got, "proxy") {
		t.Errorf("copied %q", got)
	}
}

// A form whose rows do not fit its box scrolls to keep the selected row in
// view, with the lines below it that say what it does, and draws a thumb
// on the box's right edge.
func TestFormScrollsToTheSelectedRow(t *testing.T) {
	m, _, files := projectFormModel(t)
	routes := []config.Route{{Port: 80, Role: config.RoleWeb, Label: "app"}}
	for i := 1; i < 25; i++ {
		routes = append(routes, config.Route{Port: 9000 + i, Role: config.RoleIgnore, Label: fmt.Sprintf("svc%d", i)})
	}
	files.drafts["/work/here"] = Draft{Routes: routes}
	toPath(t, m, 0)
	settle(m, press(m, "enter"))
	if !strings.Contains(screen(m), "[x] 80") || strings.Contains(screen(m), "9024") {
		t.Fatalf("at the first row:\n%s", screen(m))
	}
	edge := boxEdge(t, m)
	if !strings.HasPrefix(strings.TrimLeft(edge, "│"), thumb) || strings.Index(edge, thumb) > len("│")*8 {
		t.Errorf("at the first row, the box's edge is %q, want the thumb near its top", edge)
	}
	for i := 0; i < 24; i++ {
		press(m, "down")
	}
	wantScreen(t, m, "▌ [ ] 9024", "ignore: Nearhost opens nothing for port 9024.")
	if strings.Contains(screen(m), "[x] 80 ") {
		t.Errorf("the first row is still shown:\n%s", screen(m))
	}
	if edge := boxEdge(t, m); !strings.HasSuffix(strings.TrimRight(edge, "│"), thumb) || strings.Count(edge, thumb) == 0 {
		t.Errorf("at the last row, the box's edge is %q, want the thumb at its bottom", edge)
	}
}

// boxEdge is the form box's right edge from its title's row down to the
// row above its keys.
func boxEdge(t *testing.T, m *Model) string {
	t.Helper()
	x, y := find(t, m, "Add project")
	lines := strings.Split(screen(m), "\n")
	var right int
	for i, r := range []rune(lines[y]) {
		if (r == '│' || r == '┃') && i > x {
			right = ansi.StringWidth(string([]rune(lines[y])[:i]))
		}
	}
	var sb strings.Builder
	for yy := y; yy < len(lines) && !strings.Contains(lines[yy], "esc back"); yy++ {
		sb.WriteString(ansi.Cut(lines[yy], right, right+1))
	}
	return sb.String()
}
