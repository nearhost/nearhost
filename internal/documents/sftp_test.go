package documents

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/pkg/sftp"
)

// pipeRWC joins the two pipe ends the in-process SFTP server talks over.
type pipeRWC struct {
	io.Reader
	io.WriteCloser
}

// inProcessSFTP starts pkg/sftp's server over a pipe, with its working
// folder set to dir, and returns a client of it.
func inProcessSFTP(t *testing.T, dir string) *sftp.Client {
	t.Helper()
	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	server, err := sftp.NewServer(pipeRWC{serverReader, serverWriter}, sftp.WithServerWorkingDirectory(dir))
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve()
	client, err := sftp.NewClientPipe(clientReader, clientWriter)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Server first: client.Close waits on a reader only the server close unblocks.
		server.Close()
		client.Close()
	})
	return client
}

func TestSFTPFS(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "p.md"), []byte("# P"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	fsys := SFTPFS(inProcessSFTP(t, dir))
	ctx := context.Background()

	got, err := fsys.RealPath(ctx, filepath.Join(dir, "link"))
	if err != nil || got != real {
		t.Errorf("RealPath(link) = %q, %v; want %q", got, err, real)
	}
	got, err = fsys.RealPath(ctx, filepath.Join(dir, "link", "p.md"))
	if err != nil || got != filepath.Join(real, "p.md") {
		t.Errorf("RealPath(link/p.md) = %q, %v", got, err)
	}
	if _, err := fsys.RealPath(ctx, filepath.Join(dir, "link", "nope.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("RealPath(missing) err = %v, want fs.ErrNotExist", err)
	}
	if _, err := fsys.RealPath(ctx, filepath.Join(dir, "link", "p.md", "x")); !errors.Is(err, syscall.ENOTDIR) {
		t.Errorf("RealPath(through a file) err = %v, want ENOTDIR", err)
	}
	if _, err := fsys.Stat(ctx, filepath.Join(dir, "nope.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Stat(missing) err = %v, want fs.ErrNotExist", err)
	}
	if info, err := fsys.Stat(ctx, filepath.Join(dir, "link")); err != nil || !info.IsDir() {
		t.Errorf("Stat(link) = %v, %v; want the folder it points to", info, err)
	}
	if _, err := fsys.Open(ctx, filepath.Join(dir, "nope.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open(missing) err = %v, want fs.ErrNotExist", err)
	}
	f, err := fsys.Open(ctx, filepath.Join(real, "p.md"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(f)
	f.Close()
	if err != nil || string(b) != "# P" {
		t.Errorf("Open read %q, %v", b, err)
	}
	if home, err := fsys.Home(ctx); err != nil || home != dir {
		t.Errorf("Home = %q, %v; want %q", home, err, dir)
	}
}

func TestSFTPFSHonoursCancel(t *testing.T) {
	dir := t.TempDir()
	fsys := SFTPFS(inProcessSFTP(t, dir))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fsys.Stat(ctx, dir); !errors.Is(err, context.Canceled) {
		t.Errorf("Stat err = %v, want context.Canceled", err)
	}
}

// TestHandlerOverSFTPMatchesLocal serves the same tree through LocalFS and
// through SFTPFS and expects the same answer to every request, so a tree on
// a server behaves like one on the laptop: symlinked roots, escapes, missing
// files and paths through files alike.
func TestHandlerOverSFTPMatchesLocal(t *testing.T) {
	f := newFixture(t)
	f.write(t, "plan.md", "# Plan")
	f.write(t, "site/index.html", "<p>hi</p>")
	f.write(t, "bare/a.md", "a")
	f.write(t, ".env", "SECRET")
	if err := os.Mkdir(filepath.Join(f.dir, "outside"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "outside", "x.md"), []byte("SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../outside", filepath.Join(f.root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("root", filepath.Join(f.dir, "rootlink")); err != nil {
		t.Fatal(err)
	}
	client := inProcessSFTP(t, f.dir)
	viaLink := f.table.Add("apollo", filepath.Join(f.dir, "rootlink"), "claude", "plan.md")
	local := NewHandler(f.table, func(context.Context, string) (FS, error) { return LocalFS{}, nil })
	remote := NewHandler(f.table, func(context.Context, string) (FS, error) { return SFTPFS(client), nil })

	for _, id := range []string{f.tree.ID, viaLink.ID} {
		for _, p := range []string{
			"plan.md", "plan.md?raw", "nope.md", "plan.md/x", "nowhere/deeper/x.md",
			"site", "site/", "bare/", "link/x.md", "link/nope.md", "link/x.md/y", ".env",
		} {
			do := func(h http.Handler) *httptest.ResponseRecorder {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest("GET", "/d/"+id+"/"+p, nil))
				return rec
			}
			want, got := do(local), do(remote)
			if got.Code != want.Code || got.Body.String() != want.Body.String() {
				t.Errorf("%s: over SFTP %d %q, locally %d %q", p, got.Code, got.Body.String(), want.Code, want.Body.String())
			}
		}
	}
}
