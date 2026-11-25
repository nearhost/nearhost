package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// With nothing open, each agents row's panel shows its title and what
// would put something there.
func TestAgentsPanelEmpty(t *testing.T) {
	for _, c := range []struct{ key, title, empty string }{
		{"passthroughs", "passthroughs", "No ports are open for agents. When an agent asks to open one, Nearhost asks you first."},
		{"documents", "documents", "No documents are being served. When an agent writes a plan or report for you, it can ask to open it here."},
	} {
		m := sized(newFake(t, sampleConfig()))
		m.selectRow(c.key)
		lines := m.agentsPanel(map[string]rowKind{"passthroughs": rowPasses, "documents": rowDocs}[c.key], 60, 10)
		if len(lines) == 0 || !strings.Contains(lines[0], c.title) {
			t.Errorf("%s: title line %q", c.key, lines)
		}
		if got := strings.Join(strings.Fields(ansi.Strip(strings.Join(lines, " "))), " "); !strings.Contains(got, c.empty) {
			t.Errorf("%s: panel lacks the empty state: %q", c.key, got)
		}
		if got := screen(m); !strings.Contains(got, c.title) || !strings.Contains(got, "ask") {
			t.Errorf("%s: screen lacks the panel:\n%s", c.key, got)
		}
	}
}
