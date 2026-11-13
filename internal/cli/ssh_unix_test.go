//go:build unix

package cli

// The ssh tests that only make sense on the ControlMaster transport, which
// Windows does not have.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/testutil"
	"github.com/nearhost/nearhost/internal/transport"
)

// A Unix socket path holds 104 bytes on macOS (108 on Linux), and OpenSSH
// binds a master at its control path plus a 17-byte temporary suffix. The
// run directory of `add` must leave room whatever the server is called and
// wherever the system keeps temporary files.
//
// Windows binds no control socket, so there is nothing whose length to
// check; that a private run directory is made and removed with the session
// is TestPrivateSessionRunDirIsUnderTempDirOnWindows there.
func TestPrivateSessionSocketPathIsShort(t *testing.T) {
	const sunPath, sshSuffix, margin = 104, 17, 20
	for _, name := range []string{"mini", strings.Repeat("build-server-", 6), "deploy@build host"} {
		sess, _, end, err := sshClient{}.privateSession(config.Server{Name: name, SSHHost: "host.example"})
		if err != nil {
			t.Fatal(err)
		}
		sock := sess.(*transport.Session).SocketPath()
		dir := filepath.Dir(sock)
		if n := len(sock) + sshSuffix; n > sunPath-margin {
			t.Errorf("server %q: ssh would bind %d bytes (%s plus its suffix); the limit is %d", name, n, sock, sunPath)
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Errorf("server %q: run directory %s is not a directory: %v %v", name, dir, info, err)
		}
		testutil.WantPerm(t, dir, 0o700)
		if err := end(); err != nil {
			t.Errorf("server %q: %v", name, err)
		}
		if _, err := os.Stat(dir); err == nil {
			t.Errorf("server %q: run directory %s was left behind", name, dir)
		}
	}
}
