package e2e

import (
	"crypto/x509"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/testutil"
)

// sandbox is a home folder of its own for one nearhost, with a config file,
// and the environment and flags that keep nearhost inside it: its own
// config and data folders, the fake ssh first on PATH, ports of its own, no
// hosts file and no page.
type sandbox struct {
	t           *testing.T
	dir, config string
	https, http int
	env         []string
	args        []string
}

// newSandbox makes a sandbox whose config file holds config. env adds
// variables to nearhost's environment, such as those that steer the fake
// ssh.
func newSandbox(t *testing.T, config string, env ...string) *sandbox {
	t.Helper()
	dir := testutil.TempDir(t, "nh")
	s := &sandbox{t: t, dir: dir, config: filepath.Join(dir, "config.yaml")}
	if err := os.WriteFile(s.config, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Dir(testutil.FakeSSH(t))
	s.env = append(os.Environ(),
		"HOME="+dir, "USERPROFILE="+dir,
		"XDG_CONFIG_HOME="+filepath.Join(dir, "c"),
		"XDG_DATA_HOME="+filepath.Join(dir, "d"),
		"PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_SSH_LOG="+s.sshLogPath(),
		"TERM=xterm-256color",
		"NEARHOST_NO_UPDATE_CHECK=1",
	)
	s.env = append(s.env, env...)
	s.https, s.http = testutil.ProxyPorts(t)
	s.args = []string{"--config", s.config, "--no-hosts",
		"--https-port", fmt.Sprint(s.https), "--http-port", fmt.Sprint(s.http), "--web-port", "0"}
	return s
}

// start runs nearhost in the sandbox, in a terminal of its own.
func (s *sandbox) start() *terminal {
	s.t.Helper()
	return start(s.t, s.env, nearhostBin, s.args...)
}

func (s *sandbox) sshLogPath() string { return filepath.Join(s.dir, "ssh.log") }

// sshLog is what the fake ssh was asked to do, a line a run.
func (s *sandbox) sshLog() string {
	s.t.Helper()
	b, err := os.ReadFile(s.sshLogPath())
	if err != nil && !os.IsNotExist(err) {
		s.t.Fatal(err)
	}
	return string(b)
}

// rootCert is the sandbox's local CA, which nearhost makes as it starts.
func (s *sandbox) rootCert() *x509.Certificate {
	s.t.Helper()
	var found string
	_ = filepath.WalkDir(s.dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Name() == "root.crt" {
			found = p
		}
		return nil
	})
	if found == "" {
		s.t.Fatal("no root.crt in the sandbox")
	}
	// root.crt is at <storage>/pki/authorities/local/root.crt.
	root, err := proxy.RootCert(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(found)))))
	if err != nil {
		s.t.Fatal(err)
	}
	return root
}

// client is an HTTPS client for alias that trusts the sandbox's CA alone
// and reaches the sandbox's proxy without a hosts entry.
func (s *sandbox) client(alias string) *http.Client {
	s.t.Helper()
	return testutil.HTTPSClient(s.t, s.rootCert(), alias, s.https)
}

// get asks the sandbox's proxy for path at alias, once, and returns the
// body or the error.
func (s *sandbox) get(alias, path string) (string, error) {
	s.t.Helper()
	res, err := s.client(alias).Get(fmt.Sprintf("https://%s:%d%s", alias, s.https, path))
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	return string(body), err
}

// skipWelcome answers the first run's dialog, which asks to trust the CA,
// with a skip: trusting asks for a password.
func skipWelcome(term *terminal) {
	term.t.Helper()
	term.waitFor("the welcome dialog", "Welcome to Nearhost", "s/esc skip")
	term.press('s')
	term.waitGone("the welcome dialog to close", "Welcome to Nearhost")
}

// quit presses q and checks nearhost exits cleanly and leaves the terminal
// as it found it: off the alternate screen, with the cursor showing.
func quit(term *terminal) {
	term.t.Helper()
	term.press('q')
	restored(term)
}

// restored waits for the exit and checks the terminal is as it was.
func restored(term *terminal) {
	term.t.Helper()
	if code := term.waitExit(); code != 0 {
		term.t.Errorf("exit code %d; the screen:\n%s", code, term.screen())
	}
	term.waitUntil("the terminal to be restored", func(string) bool {
		return !term.emu.IsAltScreen() && !term.emu.CursorHidden()
	})
}
