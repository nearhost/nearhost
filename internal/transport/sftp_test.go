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
