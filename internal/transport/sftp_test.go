package transport

import (
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
	os.WriteFile(filepath.Join(root, ".env"), []byte("APP_PORT=8080\nVITE_PORT=5174\n"), 0o644)
	res, err := discovery.Discover(NewSFTPReader(client, root))
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
