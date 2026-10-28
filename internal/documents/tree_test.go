package documents

import (
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestIsDocument(t *testing.T) {
	for _, n := range []string{"a.md", "A.MD", "x.markdown", "x.html", "x.HTM"} {
		if !IsDocument(n) {
			t.Errorf("IsDocument(%q) = false, want true", n)
		}
	}
	for _, n := range []string{"x.txt", "md", ".md", "x.md.bak", "/dir/.md", "dir/.HTML"} {
		if IsDocument(n) {
			t.Errorf("IsDocument(%q) = true, want false", n)
		}
	}
}

func TestBelow(t *testing.T) {
	rel, ok := Below("/r", "/r/docs/a.md")
	if !ok || rel != "docs/a.md" {
		t.Fatalf("Below = %q, %v", rel, ok)
	}
	for _, p := range []string{"/r/../etc/x.md", "/rx/a.md", "/r", "/r/.git/x", "/r/a/.env", "/r/.superpowers/b/x.html"} {
		if rel, ok := Below("/r", p); ok {
			t.Errorf("Below(/r, %q) = %q, true; want false", p, rel)
		}
	}
}

func TestChooseRoot(t *testing.T) {
	hasGit := func(dir string) bool { return dir == "/home/ed/app" }
	tests := []struct {
		sent, doc, want string
		ok              bool
	}{
		{"/home/ed/app", "/home/ed/app/docs/p.md", "/home/ed/app", true},
		{"/home/ed/app", "/home/ed/app/.superpowers/b/x.html", "/home/ed/app/.superpowers/b", true},
		{"/etc", "/etc/x/p.md", "/etc/x", true},
		{"/home/ed", "/home/ed/n/p.md", "/home/ed/n", true},
		{"/home/ed", "/home/ed/p.md", "", false},
		{"/", "/p.md", "", false},
		{"/", "/tmp/p.md", "/tmp", true},
		{"/home", "/home/ed/app/p.md", "/home/ed/app", true},
		{"/home/ed/.claude/plans", "/home/ed/.claude/plans/x.md", "/home/ed/.claude/plans", true},
	}
	for _, tt := range tests {
		got, ok := ChooseRoot(tt.sent, tt.doc, "/home/ed", hasGit)
		if ok != tt.ok || got != tt.want {
			t.Errorf("ChooseRoot(%q, %q) = %q, %v; want %q, %v", tt.sent, tt.doc, got, ok, tt.want, tt.ok)
		}
	}
}

func TestTableAddReusesTree(t *testing.T) {
	now := time.Unix(1000, 0)
	tb := NewTable(func() time.Time { now = now.Add(time.Second); return now })
	a := tb.Add("mini", "/r", "claude", "a.md")
	b := tb.Add("mini", "/r", "claude", "b.md")
	if a.ID != b.ID {
		t.Fatalf("IDs differ: %q %q", a.ID, b.ID)
	}
	if got := strings.Join(docPaths(b), ","); got != "b.md,a.md" {
		t.Fatalf("Docs = %s", got)
	}
	c := tb.Add("mini", "/r", "claude", "a.md")
	if got := strings.Join(docPaths(c), ","); got != "a.md,b.md" {
		t.Fatalf("Docs = %s", got)
	}
	d := tb.Add("apollo", "/r", "claude", "a.md")
	if d.ID == a.ID {
		t.Fatal("different server reused the ID")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`).MatchString(a.ID) {
		t.Fatalf("bad ID %q", a.ID)
	}
	c.Docs[0].Path = "mutated"
	if g, _ := tb.Get(a.ID); g.Docs[0].Path != "a.md" {
		t.Fatal("Get returned shared state")
	}
	if g, ok := tb.Find("mini", "/r"); !ok || g.ID != a.ID || g.Requester != "claude" {
		t.Fatalf("Find = %+v, %v", g, ok)
	}
	if !tb.Remove(a.ID) || tb.Remove(a.ID) {
		t.Fatal("Remove results wrong")
	}
	if _, ok := tb.Get(a.ID); ok {
		t.Fatal("tree still present")
	}
}

func docPaths(tr Tree) []string {
	var out []string
	for _, d := range tr.Docs {
		out = append(out, d.Path)
	}
	return out
}

func TestTableListOrder(t *testing.T) {
	now := time.Unix(1000, 0)
	tb := NewTable(func() time.Time { now = now.Add(time.Second); return now })
	one := tb.Add("mini", "/one", "c", "a.md")
	two := tb.Add("mini", "/two", "c", "a.md")
	got := tb.List("")
	if len(got) != 2 || got[0].ID != two.ID || got[1].ID != one.ID {
		t.Fatalf("List = %+v", got)
	}
}

func TestTablePrune(t *testing.T) {
	tb := NewTable(time.Now)
	tb.Add("apollo", "/r", "c", "a.md")
	tb.Add("mini", "/r", "c", "a.md")
	tb.Add("local", "/r", "c", "a.md")
	if n := tb.Prune([]string{"local", "apollo"}); n != 1 {
		t.Fatalf("Prune = %d, want 1", n)
	}
	if got := tb.List("mini"); len(got) != 0 {
		t.Fatalf("List(mini) = %+v", got)
	}
	if got := tb.List("apollo"); len(got) != 1 {
		t.Fatalf("List(apollo) = %+v", got)
	}
}

func TestURLForEscapes(t *testing.T) {
	tr := Tree{ID: "abc"}
	doc := "plans/2026 plan (v2) #1?%é.md"
	got := URLFor("https://localhost:7000", tr, doc)
	const want = "https://localhost:7000/d/abc/plans/2026%20plan%20%28v2%29%20%231%3F%25%C3%A9.md"
	if got != want {
		t.Fatalf("URLFor = %q\nwant %q", got, want)
	}
	parts := strings.Split(doc, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	if got != "https://localhost:7000/d/abc/"+strings.Join(parts, "/") {
		t.Fatal("differs from PathEscape per segment")
	}
}
