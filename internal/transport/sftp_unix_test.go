//go:build unix

// Two kinds of test here run on Unix only.
//
// The first two open SFTP through a Session, over its ControlMaster's
// socket, which Windows does not have. The SFTP code under them is the
// per-process transport's too, and perprocess_test.go tests it on every OS.
//
// The rest are the SFTP writer's tests of links whose targets have more
// than one element. The in-process SFTP server runs on this machine, and on
// Windows it reads a link back as Windows wrote it, with backslashes
// (..\outside.txt, C:\x), which no POSIX server sends and the writer rightly
// does not take apart. The writer reaches POSIX servers only; the same checks
// run against the local writer on every OS (TestDirWriterRefusesLinksOutsideRoot
// and the rest in sftp_test.go), and linked_volume_test.go walks
// drive-shaped paths through a stand-in.

package transport

import (
	"context"
	"testing"

	"github.com/nearhost/nearhost/internal/testutil"
)

func TestOpenSFTPFailsCleanlyWhenSubsystemUnavailable(t *testing.T) {
	s, _ := newFakeSession(t)
	connected(t, s)
	f, err := s.OpenSFTP(t.Context())
	if err == nil {
		f.Close()
		t.Fatal("fake ssh does not provide sftp; expected an error")
	}
}

// Close kills ssh before it closes the client. When the client has noticed
// ssh going first, it has already closed its end of the pipe, and closing it
// again is refused. That is not a failure of the close.
func TestSFTPCloseAfterTheClientSawSSHGoReportsNoError(t *testing.T) {
	s := NewSession("mini", "mini", t.TempDir())
	s.SSHPath = testutil.FakeSSH(t)
	s.Env = []string{"FAKE_SSH_SFTP_SERVER=" + testutil.SFTPServer(t)}
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

func TestSFTPWriterRefusesLinksOutsideRoot(t *testing.T) {
	checkLinksOutsideRoot(t, sftpWriterFor)
}

func TestSFTPWriterRefusesLinkOutsideRoot(t *testing.T) {
	checkRefusesLinkOutsideRoot(t, sftpWriterFor)
}
func TestSFTPWriterWritesThroughLinkInsideRoot(t *testing.T) {
	checkWritesThroughLinkInsideRoot(t, sftpWriterFor)
}
