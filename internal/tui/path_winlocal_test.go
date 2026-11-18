package tui

import (
	"context"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
)

// dirLister is ServerFiles whose ListDirs answers from a map of directory to
// folder names and records what it was asked.
type dirLister struct {
	ServerFiles
	dirs  map[string][]string
	asked []string
	// resolved is what Resolve was asked for.
	resolved []string
}

func (d *dirLister) Resolve(_ context.Context, p string) (string, error) {
	d.resolved = append(d.resolved, p)
	return p, nil
}

func (d *dirLister) ListDirs(_ context.Context, dir string) ([]string, error) {
	d.asked = append(d.asked, dir)
	return d.dirs[dir], nil
}

// winForm is an add form on a Windows laptop with the path typed, on the
// local machine or on the server named srv.
func winForm(srv, path string, l *dirLister) *projectForm {
	f := &projectForm{
		laptopWindows: true, srv: config.Server{Name: srv}, files: l,
		w: newWorker(), listings: map[string]listing{}, step: projectStepPath,
	}
	f.path.value = path
	return f
}

// listNow runs the listing ahead the form starts and files its answer.
func listNow(t *testing.T, f *projectForm) {
	t.Helper()
	cmd := f.listAhead()
	if cmd == nil {
		return
	}
	lm := cmd().(listedMsg)
	f.listings[lm.parent] = listing{true, lm.dirs, lm.err}
}

func TestSplitPathWindows(t *testing.T) {
	for _, c := range []struct{ in, parent, part string }{
		{`C:\Users\me\ap`, `C:\Users\me\`, "ap"},
		{`C:\`, `C:\`, ""},
		{`C:`, "", "C:"},
		{`C:ap`, "", "C:ap"},
		{`\\server\share\dir\ap`, `\\server\share\dir\`, "ap"},
		{`C:/Users/me/ap`, `C:/Users/me/`, "ap"},
		{`C:\Users/me\ap`, `C:\Users/me\`, "ap"},
		{`C:\Users\me\`, `C:\Users\me\`, ""},
		{`~`, "", "~"},
		{`~\code`, `~\`, "code"},
		{`~/code`, `~/`, "code"},
		{`C:\My Projects\my ap`, `C:\My Projects\`, "my ap"},
	} {
		if p, n := splitPath(c.in, true); p != c.parent || n != c.part {
			t.Errorf("splitPath(%q) = %q, %q; want %q, %q", c.in, p, n, c.parent, c.part)
		}
	}
	if p, n := splitPath(`/a\b/c`, false); p != `/a\b/` || n != "c" {
		t.Errorf("POSIX split changed: %q, %q", p, n)
	}
}

func TestListPath(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", "."},
		{`C:\Users\me\`, `C:\Users\me\`},
		{`C:/Users/me/`, `C:\Users\me\`},
		{`C:\`, `C:\`},
		{`\\server\share\`, `\\server\share\`},
		{`~\`, "~/"},
		{`~\code\`, "~/code/"},
		{`~/code/`, "~/code/"},
		{`~/My Code\x\`, "~/My Code/x/"},
	} {
		if got := listPath(c.in, true); got != c.want {
			t.Errorf("listPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := listPath(`/a\b/`, false); got != `/a\b/` {
		t.Errorf("POSIX listPath changed: %q", got)
	}
}

func TestWindowsPathCompletion(t *testing.T) {
	l := &dirLister{dirs: map[string][]string{
		`C:\Users\me\`:      {".cache", "app", "apple", "My Apps", "zed"},
		`C:\`:               {"Users", "Windows"},
		`\\server\share\d\`: {"app"},
		"~/":                {"code"},
		"~/code/":           {"blog"},
	}}
	for _, c := range []struct {
		name, typed, want string
		asked             string
	}{
		{"backslash path", `C:\Users\me\ap`, `C:\Users\me\app\`, `C:\Users\me\`},
		{"drive root", `C:\`, `C:\Users\`, `C:\`},
		{"forward slashes keep their style", `C:/Users/me/ap`, `C:/Users/me/app/`, `C:\Users\me\`},
		{"mixed separators follow the last one", `C:\Users/me\ap`, `C:\Users/me\app\`, `C:\Users\me\`},
		{"unc", `\\server\share\d\a`, `\\server\share\d\app\`, `\\server\share\d\`},
		{"spaces", `C:\Users\me\my a`, `C:\Users\me\My Apps\`, `C:\Users\me\`},
		{"tilde backslash", `~\co`, `~\code\`, "~/"},
		{"tilde slash", `~/code/b`, `~/code/blog/`, "~/code/"},
	} {
		t.Run(c.name, func(t *testing.T) {
			l.asked = nil
			f := winForm(config.LocalServer, c.typed, l)
			listNow(t, f)
			f.accept()
			if f.path.value != c.want {
				t.Errorf("accepted %q, want %q", f.path.value, c.want)
			}
			if len(l.asked) != 1 || l.asked[0] != c.asked {
				t.Errorf("listed %q, want %q", l.asked, c.asked)
			}
		})
	}
}

func TestWindowsPathCaseInsensitive(t *testing.T) {
	l := &dirLister{dirs: map[string][]string{`c:\users\me\`: {"App", "other"}}}
	f := winForm(config.LocalServer, `c:\users\me\AP`, l)
	listNow(t, f)
	if got := f.suggestion(); got != "App" {
		t.Fatalf("suggestion = %q", got)
	}
	f.accept()
	if f.path.value != `c:\users\me\App\` {
		t.Errorf("accepted %q", f.path.value)
	}
}

func TestWindowsDriveRelativeIsNotCompleted(t *testing.T) {
	for _, typed := range []string{"C:", "C:ap", "c:"} {
		l := &dirLister{dirs: map[string][]string{".": {"C:app", "ap"}}}
		f := winForm(config.LocalServer, typed, l)
		listNow(t, f)
		if len(l.asked) != 0 || f.suggestion() != "" || len(f.candidates()) != 0 {
			t.Errorf("%q: listed %v, suggested %q", typed, l.asked, f.suggestion())
		}
		if cmd := f.tab(); cmd != nil || f.path.value != typed || f.listNote == "" {
			t.Errorf("%q: tab changed things: %q, note %q", typed, f.path.value, f.listNote)
		}
	}
}

func TestWindowsTabGivesTildeASeparator(t *testing.T) {
	f := winForm(config.LocalServer, "~", &dirLister{})
	f.tab()
	if f.path.value != `~\` {
		t.Errorf("tab on ~ gave %q", f.path.value)
	}
	f = winForm(config.LocalServer, "~/", &dirLister{})
	if p, part := splitPath(f.path.value, true); p != "~/" || part != "" {
		t.Errorf("split %q, %q", p, part)
	}
}

// A remote server on a Windows laptop has POSIX paths: slashes only, and
// case matters.
func TestWindowsLaptopRemoteServerKeepsSlashes(t *testing.T) {
	l := &dirLister{dirs: map[string][]string{"/srv/": {"App", "app", `a\b`}, "~/": {"code"}}}
	f := winForm("mini", "/srv/ap", l)
	if f.winLocal() {
		t.Fatal("a remote server is not a Windows local path")
	}
	listNow(t, f)
	f.accept()
	if f.path.value != "/srv/app/" || l.asked[0] != "/srv/" {
		t.Errorf("accepted %q, listed %v", f.path.value, l.asked)
	}
	f = winForm("mini", `~/a\b`, l)
	if p, n := splitPath(f.path.value, f.winLocal()); p != "~/" || n != `a\b` {
		t.Errorf("split %q, %q", p, n)
	}
	f = winForm("mini", "~", l)
	f.tab()
	if f.path.value != "~/" {
		t.Errorf("tab on ~ gave %q", f.path.value)
	}
}

func TestShortHome(t *testing.T) {
	for _, c := range []struct {
		root, home, want string
		win              bool
	}{
		{`C:\Users\me\code`, `C:\Users\me`, `~\code`, true},
		{`C:/Users/me/code`, `C:\Users\me`, `~/code`, true},
		{`c:\users\ME\code`, `C:\Users\me`, `~\code`, true},
		{`C:\Users\me`, `C:\Users\me`, `~`, true},
		{`C:\Users\meow`, `C:\Users\me`, `C:\Users\meow`, true},
		{`C:\Users\me\code`, `C:\Users\me\`, `~\code`, true},
		{`D:\code`, `C:\Users\me`, `D:\code`, true},
		{"/Users/dev/code", "/Users/dev", "~/code", false},
		{"/Users/devious", "/Users/dev", "/Users/devious", false},
		{`C:\Users\me\code`, `C:\Users\me`, `C:\Users\me\code`, false},
	} {
		if got := shortHome(c.root, c.home, c.win); got != c.want {
			t.Errorf("shortHome(%q, %q, %v) = %q, want %q", c.root, c.home, c.win, got, c.want)
		}
	}
}

// Submitting the path hands the local files a ~ they expand: ~/ and slashes,
// on a Windows laptop's own files only.
func TestResolveTranslatesTildeOnWindowsLocal(t *testing.T) {
	for _, c := range []struct {
		srv, typed, want string
	}{
		{config.LocalServer, `~\code\`, "~/code/"},
		{config.LocalServer, "~/code", "~/code"},
		{config.LocalServer, "~", "~"},
		{config.LocalServer, `C:\Users\me\app`, `C:\Users\me\app`},
		{"mini", `~\code`, `~\code`},
	} {
		l := &dirLister{}
		f := winForm(c.srv, c.typed, l)
		f.b = &fakeBackend{}
		f.resolve()()
		if len(l.resolved) != 1 || l.resolved[0] != c.want {
			t.Errorf("%s %q: Resolve got %q, want %q", c.srv, c.typed, l.resolved, c.want)
		}
	}
	f := winForm(config.LocalServer, "C:\\", &dirLister{})
	f.listings[`C:\`] = listing{true, []string{"$Recycle.Bin", "System Volume Information", "Users"}, nil}
	if c := f.candidates(); len(c) != 1 || c[0] != "Users" {
		t.Errorf("drive root candidates = %q", c)
	}
}
