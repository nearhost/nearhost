package transport

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/pkg/sftp"

	"github.com/nearhost/nearhost/internal/discovery"
)

type pipeRWC struct {
	io.Reader
	io.WriteCloser
}

// inProcessSFTP serves a temp dir over an in-memory SFTP connection.
func inProcessSFTP(t *testing.T) (*sftp.Client, string) {
	t.Helper()
	root := t.TempDir()
	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	server, err := sftp.NewServer(pipeRWC{serverReader, serverWriter})
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
	return client, root
}

func TestSFTPReaderReadsAndReportsNotExist(t *testing.T) {
	client, root := inProcessSFTP(t)
	os.WriteFile(filepath.Join(root, ".env"), []byte("APP_PORT=8080\n"), 0o644)
	r := NewSFTPReader(client, root)
	data, err := r.ReadFile(".env")
	if err != nil || string(data) != "APP_PORT=8080\n" {
		t.Fatalf("ReadFile: %q %v", data, err)
	}
	_, err = r.ReadFile("docker-compose.yml")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected fs.ErrNotExist, got %v", err)
	}
}

func TestDiscoveryWorksOverSFTP(t *testing.T) {
	client, root := inProcessSFTP(t)
	os.WriteFile(filepath.Join(root, ".env"), []byte("APP_PORT=8080\nASSET_PORT=5174\n"), 0o644)
	res, err := discovery.Discover(NewSFTPReader(client, root), discovery.Options{})
	if err != nil || len(res.Routes) != 2 {
		t.Fatalf("Discover over sftp: %+v %v", res, err)
	}
}

func TestOpenSFTPFailsCleanlyWhenSubsystemUnavailable(t *testing.T) {
	s, _ := newFakeSession(t)
	connected(t, s)
	f, err := s.OpenSFTP(t.Context())
	if err == nil {
		f.Close()
		t.Fatal("fake ssh does not provide sftp; expected an error")
	}
}

func TestKillOnCancelNeverKillsAfterHandshake(t *testing.T) {
	for i := 0; i < 500; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		close(done)
		cancel() // both channels are ready, as when ctx ends right after a good handshake
		killOnCancel(ctx, done, func() { t.Fatalf("iteration %d: killed a session whose handshake had finished", i) })
	}
}

func TestKillOnCancelKillsStuckHandshake(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	killed := false
	killOnCancel(ctx, make(chan struct{}), func() { killed = true })
	if !killed {
		t.Fatal("a handshake still in flight should be killed when ctx ends")
	}
}

// Close kills ssh before it closes the client. When the client has noticed
// ssh going first, it has already closed its end of the pipe, and closing it
// again is refused. That is not a failure of the close.
func TestSFTPCloseAfterTheClientSawSSHGoReportsNoError(t *testing.T) {
	var server string
	for _, p := range []string{"/usr/libexec/sftp-server", "/usr/lib/openssh/sftp-server", "/usr/libexec/openssh/sftp-server", "/usr/lib/ssh/sftp-server"} {
		if _, err := os.Stat(p); err == nil {
			server = p
			break
		}
	}
	if server == "" {
		t.Skip("no sftp-server binary on this machine")
	}
	wrapper := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec '"+server+"' -R\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := NewSession("mini", "mini", t.TempDir())
	s.SSHPath = wrapper
	f, err := s.OpenSFTP(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Client.Getwd(); err != nil {
		t.Fatal(err)
	}
	// What Close does first, and then the client's reader running to its
	// end before Close gets to the client.
	f.cmd.Process.Kill()
	f.Client.Wait()
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

type newWriterFunc func(t *testing.T, root string) FileWriter

func sftpWriterFor(t *testing.T, root string) FileWriter {
	client, _ := inProcessSFTP(t)
	return NewSFTPWriter(client, root)
}

// sftpNoPosixWriterFor is a writer for a server without posix-rename, so the
// fallback path runs.
func sftpNoPosixWriterFor(t *testing.T, root string) FileWriter {
	w := sftpWriterFor(t, root).(sftpWriter)
	w.posixRename = false
	return w
}

func dirWriterFor(_ *testing.T, root string) FileWriter { return DirWriter(root) }

// assertOnly fails when root holds anything but the named entries.
func assertOnly(t *testing.T, root string, want ...string) {
	t.Helper()
	entries, _ := os.ReadDir(root)
	got := map[string]bool{}
	for _, e := range entries {
		got[e.Name()] = true
	}
	if len(got) != len(want) {
		t.Fatalf("entries in %s: %v, want %v", root, got, want)
	}
	for _, n := range want {
		if !got[n] {
			t.Fatalf("entries in %s: %v, want %v", root, got, want)
		}
	}
}

func checkFile(t *testing.T, p, content string, mode os.FileMode) {
	t.Helper()
	info, err := os.Stat(p)
	if err != nil || info.Mode().Perm() != mode {
		t.Fatalf("stat %s: %v %v, want mode %v", p, info, err, mode)
	}
	if b, _ := os.ReadFile(p); string(b) != content {
		t.Fatalf("%s content %q, want %q", p, b, content)
	}
}

func checkCreatesWithMode0644(t *testing.T, nw newWriterFunc) {
	root := t.TempDir()
	if err := nw(t, root).WriteFile(".nearhostrc", []byte("a\n")); err != nil {
		t.Fatal(err)
	}
	checkFile(t, filepath.Join(root, ".nearhostrc"), "a\n", 0o644)
	assertOnly(t, root, ".nearhostrc")
}

func checkReplacesKeepingMode(t *testing.T, nw newWriterFunc) {
	root := t.TempDir()
	p := filepath.Join(root, ".nearhostrc")
	os.WriteFile(p, []byte("old\n"), 0o600)
	os.Chmod(p, 0o600)
	if err := nw(t, root).WriteFile(".nearhostrc", []byte("new\n")); err != nil {
		t.Fatal(err)
	}
	checkFile(t, p, "new\n", 0o600)
	assertOnly(t, root, ".nearhostrc")
}

func checkMissingRoot(t *testing.T, nw newWriterFunc) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	if err := nw(t, missing).WriteFile(".nearhostrc", []byte("x")); err == nil {
		t.Fatal("expected an error for a root that does not exist")
	}
	assertOnly(t, root)
}

// checkDirectoryTarget makes the rename fail after the temporary file exists,
// so the deferred cleanup is what has to remove it.
func checkDirectoryTarget(t *testing.T, nw newWriterFunc) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, ".nearhostrc"), 0o755)
	if err := nw(t, root).WriteFile(".nearhostrc", []byte("x")); err == nil {
		t.Fatal("expected an error when the target is a directory")
	}
	assertOnly(t, root, ".nearhostrc")
}

func checkSymlinkTarget(t *testing.T, nw newWriterFunc) {
	root := t.TempDir()
	dest := filepath.Join(root, "real.rc")
	os.WriteFile(dest, []byte("old\n"), 0o600)
	os.Chmod(dest, 0o600)
	if err := os.Symlink("real.rc", filepath.Join(root, ".nearhostrc")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if err := nw(t, root).WriteFile(".nearhostrc", []byte("new\n")); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(filepath.Join(root, ".nearhostrc")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was replaced: %v %v", fi, err)
	}
	checkFile(t, dest, "new\n", 0o600)
	assertOnly(t, root, ".nearhostrc", "real.rc")
}

func TestSFTPWriterCreatesWithMode0644(t *testing.T) { checkCreatesWithMode0644(t, sftpWriterFor) }
func TestSFTPWriterReplacesKeepingMode(t *testing.T) { checkReplacesKeepingMode(t, sftpWriterFor) }
func TestSFTPWriterLeavesNoTempFileOnFailure(t *testing.T) {
	checkMissingRoot(t, sftpWriterFor)
	checkDirectoryTarget(t, sftpWriterFor)
}
func TestSFTPWriterWritesThroughSymlink(t *testing.T) { checkSymlinkTarget(t, sftpWriterFor) }

func TestSFTPWriterWithoutPosixRename(t *testing.T) {
	checkCreatesWithMode0644(t, sftpNoPosixWriterFor)
	checkReplacesKeepingMode(t, sftpNoPosixWriterFor)
	checkMissingRoot(t, sftpNoPosixWriterFor)
	checkDirectoryTarget(t, sftpNoPosixWriterFor)
	checkSymlinkTarget(t, sftpNoPosixWriterFor)
}

func TestSFTPWriterRestoresTheOriginalWhenTheSecondRenameFails(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, ".nearhostrc")
	os.WriteFile(p, []byte("old\n"), 0o600)
	os.Chmod(p, 0o600)
	w := sftpNoPosixWriterFor(t, root).(sftpWriter)
	real := w.c.Rename
	toTarget := 0
	w.rename = func(oldname, newname string) error {
		if newname == p {
			toTarget++
			if toTarget == 1 { // a strict server refuses to replace the file
				return errors.New("file exists")
			}
			if toTarget == 2 { // the tmp -> target rename after the original moved aside
				return errors.New("injected rename failure")
			}
		}
		return real(oldname, newname)
	}
	if err := w.WriteFile(".nearhostrc", []byte("new\n")); err == nil {
		t.Fatal("expected the injected failure")
	}
	checkFile(t, p, "old\n", 0o600)
	assertOnly(t, root, ".nearhostrc")
}

func TestDirWriterCreatesWithMode0644(t *testing.T) { checkCreatesWithMode0644(t, dirWriterFor) }
func TestDirWriterReplacesKeepingMode(t *testing.T) { checkReplacesKeepingMode(t, dirWriterFor) }
func TestDirWriterLeavesNoTempFileOnFailure(t *testing.T) {
	checkMissingRoot(t, dirWriterFor)
	checkDirectoryTarget(t, dirWriterFor)
}
func TestDirWriterWritesThroughSymlink(t *testing.T) { checkSymlinkTarget(t, dirWriterFor) }
