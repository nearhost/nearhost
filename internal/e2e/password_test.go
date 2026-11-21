package e2e

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/elevate"
	"github.com/nearhost/nearhost/internal/testutil"
)

// These journeys go through the password box. The binary under test is built
// with the e2e tag, in which a stand-in takes sudo's place and the hosts
// file is the sandbox's own: the machine's sudo and hosts file are never
// touched. The CA is never trusted here, which would run the real sudo.

// untouched is the sandbox's hosts file before nearhost has written to it.
const untouched = "127.0.0.1 localhost\n"

// hasShop reports whether the hosts file is as it was with nearhost's block
// after it, and shop.test in the block. The block also has a line for the
// IPv6 loopback on a machine that has one, so it is not compared whole.
func hasShop(hostsFile string) bool {
	return strings.HasPrefix(hostsFile, untouched+"# nearhost:start\n") &&
		strings.HasSuffix(hostsFile, "# nearhost:end\n") &&
		strings.Contains(hostsFile, "\n127.0.0.1 shop.test\n")
}

// shopWithHosts starts nearhost with the project "shop" and the hosts file
// managed, and waits for the password box that asks to add its name.
func shopWithHosts(t *testing.T) (*sandbox, *terminal) {
	t.Helper()
	// Windows asks for consent in a dialog of its own, not in the password
	// box. And a run with administrator rights, as GitHub's Windows
	// runners and root are, writes the machine's own hosts file alone,
	// which the stand-in's command would then be refused.
	if runtime.GOOS == "windows" {
		t.Skip("Windows asks through its own prompt, not the password box")
	}
	if elevate.Elevated() {
		t.Skip("an elevated run writes the machine's hosts file alone")
	}
	sb := newSandbox(t, fmt.Sprintf(localShop, t.TempDir(), testutil.Upstream(t, "shop")))
	sb.manageHosts = true
	term := sb.start()
	term.waitFor("the password box", "Password needed", "Nearhost needs your password to add shop.test to", "enter continue · esc cancel")
	if got := sb.hostsFile(); strings.Contains(got, "shop.test") {
		t.Fatalf("the hosts file holds the name before any password:\n%s", got)
	}
	return sb, term
}

// elevated is the commands the stand-in for sudo was given, a line each.
func (s *sandbox) elevated() string {
	s.t.Helper()
	b, err := os.ReadFile(filepath.Join(s.dir, "elevated.log"))
	if err != nil && !os.IsNotExist(err) {
		s.t.Fatal(err)
	}
	return string(b)
}

// A wrong password is refused and asked for again, with nothing run. The
// right one adds the project's name to the hosts file, and the project
// starts. What is typed never shows.
func TestThePasswordBoxAddsTheNameToTheHostsFile(t *testing.T) {
	sb, term := shopWithHosts(t)

	term.typeText("not-the-password")
	term.waitFor("the password hidden as it is typed", "password  ••••••••••••••••")
	if s := term.screen(); strings.Contains(s, "not-the-password") {
		t.Fatalf("the password shows on the screen:\n%s", s)
	}
	term.press(keyEnter)
	term.waitFor("the refusal, and the box still open", "Sorry, try again.", "Password needed")
	if ran := sb.elevated(); ran != "" {
		t.Errorf("a refused password ran %q", ran)
	}

	term.typeText(password)
	term.press(keyEnter)
	term.waitGone("the password box to close", "Password needed")
	// The first run's dialog comes once the box has gone.
	skipWelcome(term)
	term.waitFor("the project running with its name in the hosts file", "● shop", "hosts ok", "hosts     ● 1 name")
	if got := sb.hostsFile(); !hasShop(got) {
		t.Errorf("the hosts file: %q", got)
	}
	if ran := sb.elevated(); strings.Count(ran, "\n") != 1 || !strings.Contains(ran, "hosts apply --file "+sb.hostsPath()) {
		t.Errorf("the commands run with the password: %q", ran)
	}
	if body, err := sb.get("shop.test", "/"); err != nil || !strings.HasPrefix(body, "shop:shop.test") {
		t.Errorf("the project answered %q, %v", body, err)
	}
	quit(term)
}

// esc at the password box cancels: the project stays stopped, saying why,
// and the hosts file is as it was.
func TestEscAtThePasswordBoxLeavesTheHostsFileAlone(t *testing.T) {
	sb, term := shopWithHosts(t)
	term.press(keyEscape)
	term.waitGone("the password box to close", "Password needed")
	skipWelcome(term)
	term.waitFor("the project stopped, with the reason", "○ shop", "hosts are not resolvable", "you cancelled", "missing shop.test")
	if got := sb.hostsFile(); got != untouched {
		t.Errorf("the hosts file changed:\n%s", got)
	}
	if ran := sb.elevated(); ran != "" {
		t.Errorf("a cancelled box ran %q", ran)
	}
	quit(term)
}

// Three wrong passwords close the box: the project stays stopped, saying
// why, and the hosts file is as it was.
func TestThreeWrongPasswordsCloseTheBox(t *testing.T) {
	sb, term := shopWithHosts(t)
	for i, wrong := range []string{"one", "two", "three"} {
		term.typeText(wrong)
		term.waitFor("the password typed", "password  "+strings.Repeat("•", len(wrong)))
		term.press(keyEnter)
		if i < 2 {
			term.waitUntil("the refusal and an empty box", func(s string) bool {
				return strings.Contains(s, "Sorry, try again.") && !strings.Contains(s, "password  •")
			})
		}
	}
	term.waitGone("the password box to close", "Password needed")
	skipWelcome(term)
	term.waitFor("the project stopped, with the reason", "○ shop", "three wrong passwords")
	if got := sb.hostsFile(); got != untouched {
		t.Errorf("the hosts file changed:\n%s", got)
	}
	if ran := sb.elevated(); ran != "" {
		t.Errorf("refused passwords ran %q", ran)
	}
	quit(term)
}

// The stand-ins for sudo and the hosts file are in builds made with the e2e
// tag alone. A build without it, as a release is, holds none of them.
func TestAReleaseBuildHasNoTestSeam(t *testing.T) {
	plain := filepath.Join(t.TempDir(), "nearhost-plain")
	if out, err := exec.Command("go", "build", "-o", plain, "github.com/nearhost/nearhost/cmd/nearhost").CombinedOutput(); err != nil {
		t.Fatalf("build without the tag: %v\n%s", err, out)
	}
	const seam = "NEARHOST_E2E_"
	for path, want := range map[string]bool{plain: false, nearhostBin: true} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := bytes.Contains(b, []byte(seam)); got != want {
			t.Errorf("%s holds %q: %v, want %v", filepath.Base(path), seam, got, want)
		}
	}
}
