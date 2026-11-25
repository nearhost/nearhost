package tui

// agentsPanel draws the main panel of the passthroughs or documents row: its
// title, then what agents have opened across every server, which for now is
// only the empty state.
func (m *Model) agentsPanel(kind rowKind, width, height int) []string {
	title, empty := "passthroughs", "No ports are open for agents. When an agent asks to open one, Nearhost asks you first."
	if kind == rowDocs {
		title, empty = "documents", "No documents are being served. When an agent writes a plan or report for you, it can ask to open it here."
	}
	lines := []string{styleTitle.Render(title), ""}
	return append(lines, wrap(empty, width)...)
}
