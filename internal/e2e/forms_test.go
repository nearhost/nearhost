package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/testutil"
)

// configFile is the sandbox's config file as it is now.
func (s *sandbox) configFile() string {
	s.t.Helper()
	b, err := os.ReadFile(s.config)
	if err != nil {
		s.t.Fatal(err)
	}
	return string(b)
}

// n adds a project: the form offers the folder nearhost was started in,
// takes a web port, and names the project after the folder. The project is
// written to the config, started and served.
func TestNAddsAProjectThatIsThenServed(t *testing.T) {
	// The folder by its real path, which is what a program started in it
	// is told: a Mac's temporary folder is reached through a symlink.
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(parent, "blog")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	blog := testutil.Upstream(t, "blog")
	sb := newSandbox(t, fmt.Sprintf(localShop, t.TempDir(), testutil.Upstream(t, "shop")))
	sb.cwd = folder
	term := sb.start()
	skipWelcome(term)
	term.waitFor("the first project running", "● shop")

	term.press('n')
	term.waitFor("the server step", "Add project · server", "▸ local", "1 of 7")
	term.press(keyEnter)
	term.waitFor("the path step, with the folder offered", "Add project · path", "blog")
	term.press(keyEnter)
	// The presets question would fetch a registry from the network: no.
	term.waitFor("the presets question", "Add project · presets", "▸ Yes")
	term.press(keyDown)
	term.waitFor("No chosen", "▸ No")
	term.press(keyEnter)
	term.waitFor("the routes step", "Add project · routes", "No ports were found", "web port")
	term.typeText(fmt.Sprint(blog))
	term.waitFor("the port typed", fmt.Sprintf("web port  %d", blog))
	term.press(keyEnter)
	term.waitFor("the name step", "Add project · name", "name      blog", "alias     https://blog.test")
	term.press(keyEnter)
	term.waitFor("the confirm step", "Add project · confirm", "7 of 7", fmt.Sprintf("routes    %d  web", blog), "enter add")
	term.press(keyEnter)

	term.waitFor("the new project running", "● blog", "2 running")
	term.waitGone("the form to close", "Add project")
	if body, err := sb.get("blog.test", "/"); err != nil || !strings.HasPrefix(body, "blog:blog.test") {
		t.Errorf("the added project answered %q, %v", body, err)
	}
	// The path is the folder nearhost was started in, which the form
	// offered: on the screen a long one wraps, so it is read here.
	if cfg := sb.configFile(); !strings.Contains(cfg, "name: blog") || !strings.Contains(cfg, "alias: blog.test") ||
		!strings.Contains(cfg, "path: "+folder) {
		t.Errorf("the config file does not hold the project:\n%s", cfg)
	}
	// The first project is as it was.
	if body, err := sb.get("shop.test", "/"); err != nil || !strings.HasPrefix(body, "shop:shop.test") {
		t.Errorf("the first project answered %q, %v", body, err)
	}
	quit(term)
}

// esc backs out of the add-project form a step at a time, and nothing is
// added.
func TestEscLeavesTheAddProjectFormWithNothingAdded(t *testing.T) {
	sb, term := shop(t)
	before := sb.configFile()
	term.press('n')
	term.waitFor("the server step", "Add project · server")
	term.press(keyEnter)
	term.waitFor("the path step", "Add project · path", "esc back")
	term.press(keyEscape)
	term.waitFor("the server step again", "Add project · server", "esc cancel")
	term.press(keyEscape)
	term.waitGone("the form to close", "Add project")
	term.waitFor("Home as it was", "● shop", "1 running")
	if after := sb.configFile(); after != before {
		t.Errorf("the config file changed:\n%s", after)
	}
	quit(term)
}

// s adds a server: the form takes its ssh host, checks it can be reached
// and its files read, here through the fake ssh, and writes it to the
// config. c then connects it.
func TestSAddsAServerThatThenConnects(t *testing.T) {
	sftp := testutil.SFTPServer(t) // skips where the system has none
	port := testutil.Upstream(t, "shop")
	sb := newSandbox(t, fmt.Sprintf(localShop, t.TempDir(), port),
		"FAKE_SSH_SFTP_SERVER="+sftp, "FAKE_SSH_SFTP_HOME="+t.TempDir())
	term := sb.start()
	skipWelcome(term)
	term.waitFor("the project running", "● shop")

	term.press('s')
	term.waitFor("the host step", "Add server · host", "1 of 3")
	// A short name: the server's control socket is named after it.
	term.typeText("mini")
	term.waitFor("the host typed", "host      mini")
	term.press(keyEnter)
	term.waitFor("the name step, named after the host", "Add server · name", "name      mini", "enter check")
	term.press(keyEnter)

	term.waitFor("the server added", "Added server mini.", "ssh host  mini", "projects  none")
	term.waitGone("the form to close", "Add server")
	if cfg := sb.configFile(); !strings.Contains(cfg, "ssh_host: mini") {
		t.Errorf("the config file does not hold the server:\n%s", cfg)
	}
	// The check was made with ssh, to the host typed.
	if log := sb.sshLog(); !strings.Contains(log, "-- mini") {
		t.Errorf("ssh was not run for mini:\n%s", log)
	}

	term.waitFor("the server's page, not connected", "○ disconnected", "c connect")
	term.press('c')
	term.waitFor("the server connected", "● connected")
	quit(term)
}
