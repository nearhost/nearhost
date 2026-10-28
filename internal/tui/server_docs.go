package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/documents"
)

// treeRoot is the tree's folder as the panel shows it: below this machine's
// home as ~ for a tree on this machine, and as it was sent for one on a
// server.
func (m *Model) treeRoot(tr documents.Tree) string { return m.shortRoot(tr.Server, tr.Root) }

// treeText is the line of a served tree: its folder, the agent it was
// approved for, its newest document and how long ago that was opened.
func (m *Model) treeText(tr documents.Tree) string {
	parts := []string{m.treeRoot(tr), requesterName(tr.Requester)}
	opened := tr.Opened
	if len(tr.Docs) > 0 {
		parts = append(parts, clean(tr.Docs[0].Path))
		opened = tr.Docs[0].Opened
	}
	return strings.Join(append(parts, age(m.now(), opened)), " · ")
}

// openTree opens the tree's newest document in the browser, on the worker,
// as opening the browser blocks.
func (m *Model) openTree(server string, tr documents.Tree) tea.Cmd {
	m.panelErr = ""
	b, key := m.b, rowKey(rowServer, server)
	return m.work.do(func(context.Context) tea.Msg {
		return treeOpenedMsg{key, tr, b.OpenDocument(tr.ID)}
	})
}

// treeOpenedMsg is the outcome of opening a tree's newest document from its
// server's panel, the row of key.
type treeOpenedMsg struct {
	key string
	tr  documents.Tree
	err error
}

// treeOpened logs the document opened, or shows why it could not be.
func (m *Model) treeOpened(msg treeOpenedMsg) {
	if msg.err != nil {
		m.refused(msg.key, msg.err)
		return
	}
	doc := "the newest document"
	if len(msg.tr.Docs) > 0 {
		doc = clean(msg.tr.Docs[0].Path)
	}
	m.logs.noteOn(msg.key, fmt.Sprintf("Opened %s from %s.", doc, m.treeRoot(msg.tr)))
}

// treeForm asks before no longer serving a tree, and stops serving it on
// yes.
type treeForm struct {
	b      Backend
	w      *worker
	server string
	id     string
	// root is the tree's folder, as the panel shows it.
	root string
	busy bool
	err  string
	seq  int
}

// treeRemovedMsg is the outcome of no longer serving a tree.
type treeRemovedMsg struct {
	seq          int
	server, root string
	err          error
}

// update answers y by removing the tree, on the worker, and n or esc by
// closing the question.
func (f *treeForm) update(msg tea.Msg) tea.Cmd {
	km, ok := msg.(tea.KeyPressMsg)
	if !ok || f.busy {
		return nil // a removal runs to its end
	}
	switch km.String() {
	case "y":
		f.seq = nextSeq()
		f.busy, f.err = true, ""
		seq, b, server, id, root := f.seq, f.b, f.server, f.id, f.root
		return f.w.do(func(context.Context) tea.Msg {
			return treeRemovedMsg{seq, server, root, b.RemoveDocuments(id)}
		})
	case "n", "esc":
		return closeForm
	}
	return nil
}

// lines draws what the question adds to its title.
func (f *treeForm) lines(width int) []string {
	lines := wrap("Its pages stop loading in the browser. The files stay where they are.", width)
	if f.busy {
		lines = append(lines, "", busyLine("Stopping…"))
	}
	if f.err != "" {
		lines = append(lines, "")
		lines = append(lines, errorLines(f.err, width)...)
	}
	return lines
}

// heading is the question.
func (f *treeForm) heading() heading {
	return heading{title: "Stop serving " + f.root + "?"}
}

// keys are y and n, and none while the removal runs.
func (f *treeForm) keys() []footerItem {
	if f.busy {
		return nil
	}
	return []footerItem{{"y", "stop serving"}, {"n", "keep"}}
}

// treeRemoved takes the outcome of a removal: the question closes when it
// was made, and shows why not otherwise.
func (m *Model) treeRemoved(msg treeRemovedMsg) {
	f, ok := m.form.(*treeForm)
	if !ok || f.seq != msg.seq {
		return
	}
	f.busy = false
	if msg.err != nil {
		f.err = msg.err.Error()
		return
	}
	m.form = nil
	m.logs.noteOn(rowKey(rowServer, msg.server), fmt.Sprintf("Stopped serving %s.", msg.root))
}
