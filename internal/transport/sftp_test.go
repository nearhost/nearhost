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

// writerCase runs one of three behaviours against a FileWriter, so the
// local and SFTP writers are held to one contract.
func writerCase(t *testing.T, name string, newWriter func(t *testing.T, root string) FileWriter) {
	newRoot := func(t *testing.T) string { return t.TempDir() }
	switch name {
	case "CreatesWithMode0644":
		root := newRoot(t)
		if err := newWriter(t, root).WriteFile(".nearhostrc", []byte("a\n")); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(filepath.Join(root, ".nearhostrc"))
		if err != nil || info.Mode().Perm() != 0o644 {
			t.Fatalf("stat: %v %v", info, err)
		}
		if b, _ := os.ReadFile(filepath.Join(root, ".nearhostrc")); string(b) != "a\n" {
			t.Fatalf("content %q", b)
		}
		assertNoStray(t, root, ".nearhostrc")
	case "ReplacesKeepingMode":
		root := newRoot(t)
		p := filepath.Join(root, ".nearhostrc")
		os.WriteFile(p, []byte("old\n"), 0o600)
		os.Chmod(p, 0o600)
		if err := newWriter(t, root).WriteFile(".nearhostrc", []byte("new\n")); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("stat: %v %v", info, err)
		}
		if b, _ := os.ReadFile(p); string(b) != "new\n" {
			t.Fatalf("content %q", b)
		}
		assertNoStray(t, root, ".nearhostrc")
	case "LeavesNoTempFileOnFailure":
		root := newRoot(t)
		missing := filepath.Join(root, "missing")
		if err := newWriter(t, missing).WriteFile(".nearhostrc", []byte("x")); err == nil {
			t.Fatal("expected an error for a root that does not exist")
		}
		if _, err := os.Stat(missing); err == nil {
			t.Fatal("the root must not be created")
		}
		assertNoStray(t, root, "")
	}
}

func assertNoStray(t *testing.T, root, want string) {
	t.Helper()
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if e.Name() != want {
			t.Fatalf("stray file %q in %s", e.Name(), root)
		}
	}
}

func sftpWriterFor(t *testing.T, root string) FileWriter {
	client, _ := inProcessSFTP(t)
	return NewSFTPWriter(client, root)
}

func dirWriterFor(_ *testing.T, root string) FileWriter { return DirWriter(root) }

func TestSFTPWriterCreatesWithMode0644(t *testing.T) {
	writerCase(t, "CreatesWithMode0644", sftpWriterFor)
}

func TestSFTPWriterReplacesKeepingMode(t *testing.T) {
	writerCase(t, "ReplacesKeepingMode", sftpWriterFor)
}

func TestSFTPWriterLeavesNoTempFileOnFailure(t *testing.T) {
	writerCase(t, "LeavesNoTempFileOnFailure", sftpWriterFor)
}

func TestDirWriterCreatesWithMode0644(t *testing.T) {
	writerCase(t, "CreatesWithMode0644", dirWriterFor)
}

func TestDirWriterReplacesKeepingMode(t *testing.T) {
	writerCase(t, "ReplacesKeepingMode", dirWriterFor)
}

func TestDirWriterLeavesNoTempFileOnFailure(t *testing.T) {
	writerCase(t, "LeavesNoTempFileOnFailure", dirWriterFor)
}
