package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nearhost/nearhost/internal/testutil"
)

// localShop is a config with one project on this machine, "shop", whose web
// route is the port given.
const localShop = `servers: []
projects:
  - name: shop
    server: local
    path: %s
    alias: shop.test
    enabled: true
    routes:
      - port: %d
        role: web
`

// shop starts nearhost with the local project "shop" in front of an
// upstream that answers "shop:<host>", and waits for it to run.
func shop(t *testing.T) (*sandbox, *terminal) {
	t.Helper()
	port := testutil.Upstream(t, "shop")
	sb := newSandbox(t, fmt.Sprintf(localShop, t.TempDir(), port))
	term := sb.start()
	term.waitUntil("the alternate screen", func(string) bool { return term.emu.IsAltScreen() })
	skipWelcome(term)
	term.waitFor("the project running", "● shop", "running   1 of 1 projects", fmt.Sprintf("https :%d", sb.https))
	return sb, term
}

// nearhost starts with one local project, serves it over HTTPS the moment
// the screen says it runs, and q leaves the terminal as it found it.
func TestALocalProjectIsServedAndQuitRestoresTheTerminal(t *testing.T) {
	sb, term := shop(t)
	body, err := sb.get("shop.test", "/")
	if err != nil {
		t.Fatalf("a request made as the screen showed shop running: %v\nthe screen:\n%s", err, term.screen())
	}
	if !strings.HasPrefix(body, "shop:shop.test") {
		t.Errorf("the proxy answered %q", body)
	}
	quit(term)
}

// enter stops the selected project, and its address stops answering; enter
// again starts it. The help opens over it all and closes.
func TestEnterStopsAndStartsAProject(t *testing.T) {
	sb, term := shop(t)
	term.press(keyDown)
	term.press(keyDown)
	term.waitFor("the project's page", "shop  https://shop.test", "enter stop")

	term.press('?')
	term.waitFor("the help", "enter  start or stop", "?      close help")
	term.press('?')
	term.waitGone("the help to close", "close help")

	term.press(keyEnter)
	term.waitFor("the project stopped", "○ shop", "state: stopped", "enter start")
	if body, err := sb.get("shop.test", "/"); err == nil {
		t.Errorf("a stopped project answered %q", body)
	}

	term.press(keyEnter)
	term.waitFor("the project running again", "● shop", "enter stop")
	if body, err := sb.get("shop.test", "/"); err != nil || !strings.HasPrefix(body, "shop:shop.test") {
		t.Errorf("a project started again answered %q, %v", body, err)
	}
	quit(term)
}

// A request through the proxy shows in the project's full-screen log, and
// esc comes back.
func TestARequestShowsInTheLog(t *testing.T) {
	sb, term := shop(t)
	if _, err := sb.get("shop.test", "/cart?item=7"); err != nil {
		t.Fatal(err)
	}
	term.press(keyDown)
	term.press(keyDown)
	term.waitFor("the project's page", "shop  https://shop.test")
	term.press('l')
	term.waitFor("the log with the request", "nearhost › shop › log", "/cart?item=7", "esc back")
	term.press(keyEscape)
	term.waitFor("the project's page again", "shop  https://shop.test", "enter stop")
	quit(term)
}

// fits reports whether the screen is drawn for a terminal w wide and h high:
// its footer is on the last row and no row runs past the edge.
func fits(screen string, w, h int) bool {
	lines := strings.Split(strings.TrimRight(screen, "\n"), "\n")
	if len(lines) != h || !strings.Contains(lines[h-1], "q quit") {
		return false
	}
	for _, l := range lines {
		if utf8.RuneCountInString(l) > w {
			return false
		}
	}
	return true
}

// A terminal made smaller is redrawn to fit; one too small to draw in says
// so, and is drawn again when it grows.
func TestAResizeRedrawsToFit(t *testing.T) {
	_, term := shop(t)

	term.resize(90, 24)
	term.waitUntil("a redraw 90 by 24", func(s string) bool { return fits(s, 90, 24) && strings.Contains(s, "● shop") })

	term.resize(72, 20)
	term.waitFor("the too-small notice", "Make the window larger (80×20 at least). q quits.")
	term.waitGone("the interface to go", "● shop")

	term.resize(cols, rows)
	term.waitUntil("a redraw at full size", func(s string) bool { return fits(s, cols, rows) && strings.Contains(s, "● shop") })
	quit(term)
}

// ctrl+c quits as q does, and leaves the terminal as it found it.
func TestCtrlCQuitsAndRestoresTheTerminal(t *testing.T) {
	_, term := shop(t)
	term.press('c', ctrl)
	restored(term)
}

// onMini is a config with one project, "api", on the server "mini".
const onMini = `servers:
  - name: mini
    ssh_host: mini.lan
projects:
  - name: api
    server: mini
    path: /srv/api
    alias: api.test
    enabled: true
    routes:
      - port: 80
        role: web
`

// A project on a server: nearhost connects with ssh, here the fake one, and
// the screen follows the connection, the forward and the project.
func TestAServerConnectsAndItsProjectStarts(t *testing.T) {
	sb := newSandbox(t, onMini)
	term := sb.start()
	skipWelcome(term)
	term.waitFor("the server connected and its project running", "● connected", "● api", "mini: forward 127.0.0.1:")
	// The screen's forward is the one ssh was asked for. How it is asked
	// differs by transport, so only the forward is read.
	if log := sb.sshLog(); !strings.Contains(log, "-L 127.0.0.1:") || !strings.Contains(log, ":localhost:80") {
		t.Errorf("ssh was not asked to forward port 80:\n%s", log)
	}
	quit(term)
}

// A server that refuses the login is a problem on Home, with what to do
// about it. Once access is fixed, r connects and the project starts.
func TestAServerThatRefusesTheLoginRecoversWithR(t *testing.T) {
	refuse := filepath.Join(t.TempDir(), "refuse")
	if err := os.WriteFile(refuse, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	sb := newSandbox(t, onMini, "FAKE_SSH_AUTH_FAIL_FILE="+refuse)
	term := sb.start()
	skipWelcome(term)
	term.waitFor("the problem and its fix", "✕ mini  auth failed", "To fix access, run ssh once in a terminal:", "ssh mini.lan", "blocks    api", "r retry now")

	if err := os.Remove(refuse); err != nil {
		t.Fatal(err)
	}
	term.press('r')
	term.waitFor("the server connected and its project running", "● connected", "● api")
	term.waitGone("the problem to clear", "✕ mini  auth failed", "To fix access, run ssh once")
	quit(term)
}
