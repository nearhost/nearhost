// Package documents decides which folder a document from a coding agent is
// served from, and keeps the table of folders ("trees") Nearhost is serving.
package documents

import (
	"crypto/rand"
	"encoding/base64"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// IsDocument reports whether name's base name has one of the document
// extensions, .md, .markdown, .html or .htm in any case, and more before it:
// ".md" alone, in any folder, is not a document.
func IsDocument(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".markdown", ".html", ".htm":
		return path.Base(name) != path.Ext(name)
	}
	return false
}

// Below returns p relative to root, with "/" separators. ok is false when p
// is outside root, is root itself, or has a segment starting with ".". Paths
// are cleaned but never resolved, so callers pass resolved paths.
func Below(root, p string) (rel string, ok bool) {
	root, p = path.Clean(root), path.Clean(p)
	if p == root {
		return "", false
	}
	prefix := root
	if prefix != "/" {
		prefix += "/"
	}
	if !strings.HasPrefix(p, prefix) {
		return "", false
	}
	rel = p[len(prefix):]
	for _, seg := range strings.Split(rel, "/") {
		if strings.HasPrefix(seg, ".") {
			return "", false
		}
	}
	return rel, true
}

// aboveOrAt reports whether dir is p or one of its ancestors.
func aboveOrAt(dir, p string) bool {
	if dir == "/" {
		return true
	}
	return p == dir || strings.HasPrefix(p, dir+"/")
}

// ChooseRoot picks the folder to serve doc from. The root the agent sent is
// accepted only when it is doc's folder or holds a .git entry (hasGit), is not
// "/", home or above home, and contains doc. If the way from the root down to
// doc has a segment starting with ".", or the sent root is not accepted, doc's
// folder is used. ok is false when that folder is "/", home or above home, so
// there is no acceptable root. All paths are absolute and resolved.
func ChooseRoot(sent, doc, home string, hasGit func(dir string) bool) (root string, ok bool) {
	doc, home = path.Clean(doc), path.Clean(home)
	dir := path.Dir(doc)
	barred := func(p string) bool { return aboveOrAt(p, home) }
	if sent != "" {
		sent = path.Clean(sent)
		if (sent == dir || hasGit(sent)) && !barred(sent) {
			if _, below := Below(sent, doc); below {
				return sent, true
			}
		}
	}
	if barred(dir) {
		return "", false
	}
	return dir, true
}

// Doc is one document in a tree. Path is relative to the tree's root.
type Doc struct {
	Path   string
	Opened time.Time
}

// Tree is a folder Nearhost serves on a server. Docs is newest first.
// Requester is the agent the tree was approved for.
type Tree struct {
	ID, Server, Root, Requester string
	Docs                        []Doc
	Opened                      time.Time
}

// clone returns a copy of tr that shares no memory with it.
func (tr *Tree) clone() Tree {
	c := *tr
	c.Docs = append([]Doc(nil), tr.Docs...)
	return c
}

// Table holds the trees being served. It is safe for concurrent use, and the
// zero value is not usable: make one with NewTable.
type Table struct {
	now   func() time.Time
	mu    sync.Mutex
	trees map[string]*Tree
}

// NewTable returns an empty table that takes the time from now.
func NewTable(now func() time.Time) *Table {
	return &Table{now: now, trees: map[string]*Tree{}}
}

// newID returns 16 random bytes as 22 characters of unpadded base64url.
func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("documents: no randomness: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Add finds the tree for server and root, or makes one with a fresh ID, moves
// doc to the front of its Docs without duplicating it, and returns a copy.
// requester is recorded only when the tree is made.
func (t *Table) Add(server, root, requester, doc string) Tree {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	tr := t.find(server, root)
	if tr == nil {
		tr = &Tree{ID: newID(), Server: server, Root: root, Requester: requester}
		t.trees[tr.ID] = tr
	}
	docs := []Doc{{Path: doc, Opened: now}}
	for _, d := range tr.Docs {
		if d.Path != doc {
			docs = append(docs, d)
		}
	}
	tr.Docs, tr.Opened = docs, now
	return tr.clone()
}

// find returns the tree for server and root, or nil. The caller holds mu.
func (t *Table) find(server, root string) *Tree {
	for _, tr := range t.trees {
		if tr.Server == server && tr.Root == root {
			return tr
		}
	}
	return nil
}

// Find returns a copy of the tree for server and root.
func (t *Table) Find(server, root string) (Tree, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if tr := t.find(server, root); tr != nil {
		return tr.clone(), true
	}
	return Tree{}, false
}

// Get returns a copy of the tree with the given ID.
func (t *Table) Get(id string) (Tree, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if tr, ok := t.trees[id]; ok {
		return tr.clone(), true
	}
	return Tree{}, false
}

// List returns copies of the trees on server, newest Opened first. An empty
// server lists every server's trees.
func (t *Table) List(server string) []Tree {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []Tree
	for _, tr := range t.trees {
		if server == "" || tr.Server == server {
			out = append(out, tr.clone())
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Opened.Equal(out[j].Opened) {
			return out[i].Opened.After(out[j].Opened)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Remove deletes the tree with the given ID and reports whether it existed.
func (t *Table) Remove(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.trees[id]
	delete(t.trees, id)
	return ok
}

// Prune removes the trees whose server is not in keep and returns how many it
// removed.
func (t *Table) Prune(keep []string) int {
	k := map[string]bool{}
	for _, s := range keep {
		k[s] = true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for id, tr := range t.trees {
		if !k[tr.Server] {
			delete(t.trees, id)
			n++
		}
	}
	return n
}

// URLFor returns the address of doc, a path below the tree's root:
// base + "/d/" + the tree's ID + "/" + the path with each segment escaped.
func URLFor(base string, tr Tree, doc string) string {
	segs := strings.Split(doc, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return base + "/d/" + tr.ID + "/" + strings.Join(segs, "/")
}
