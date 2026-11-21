package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/testutil"
)

// These journeys go through the Windows prompt that asks to approve a change
// to the hosts file. The binary under test is built with the e2e tag, in
// which a stand-in takes the prompt's place and the hosts file is the
// sandbox's own: nothing asks Windows for consent and the machine's hosts
// file is never touched. The elevator around the prompt is the release's
// own, and so is the helper that writes the file, but for what a helper
// checks only as administrator: it runs as this user. The real prompt, and
// a helper that runs as administrator, are checked by hand.

// shopAsking starts nearhost with the project "shop" and the hosts file
// managed, and waits for the prompt that asks to add its name.
func shopAsking(t *testing.T) (*sandbox, *terminal) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("only Windows asks through its own prompt")
	}
	sb := newSandbox(t, fmt.Sprintf(localShop, t.TempDir(), testutil.Upstream(t, "shop")))
	sb.manageHosts = true
	term := sb.start()
	// Nothing covers the first run's dialog here: the prompt is Windows'.
	skipWelcome(term)
	sb.waitAsked(1)
	if got := sb.hostsFile(); got != untouched {
		t.Fatalf("the hosts file changed before any answer:\n%s", got)
	}
	return sb, term
}

func (s *sandbox) consentPath() string { return filepath.Join(s.dir, "consent") }

// asked is the commands the stand-in for the prompt asked to approve, a
// line each.
func (s *sandbox) asked() string {
	s.t.Helper()
	b, err := os.ReadFile(s.consentPath() + ".asked")
	if err != nil && !os.IsNotExist(err) {
		s.t.Fatal(err)
	}
	return string(b)
}

// waitAsked waits until the prompt has come up n times in all, each for
// the helper that writes the sandbox's hosts file.
func (s *sandbox) waitAsked(n int) {
	s.t.Helper()
	testutil.WaitUntil(s.t, stepWait, func() bool { return strings.Count(s.asked(), "\n") >= n },
		fmt.Sprintf("prompt %d from Windows", n))
	got := s.asked()
	if strings.Count(got, "\n") != n || strings.Count(got, " hosts apply --file "+s.hostsPath()+" --input ") != n {
		s.t.Fatalf("the prompts so far, want %d for the hosts file:\n%s", n, got)
	}
}

// answer answers the prompt that is up, or the next one: "yes" approves,
// and anything else declines.
func (s *sandbox) answer(said string) {
	s.t.Helper()
	// Put in place whole, so the stand-in never reads half an answer.
	tmp := s.consentPath() + ".tmp"
	if err := os.WriteFile(tmp, []byte(said+"\n"), 0o600); err != nil {
		s.t.Fatal(err)
	}
	if err := os.Rename(tmp, s.consentPath()); err != nil {
		s.t.Fatal(err)
	}
}

// Approving the prompt adds the project's name to the hosts file, and the
// project starts.
func TestApprovingTheWindowsPromptAddsTheNameToTheHostsFile(t *testing.T) {
	sb, term := shopAsking(t)
	sb.answer("yes")
	term.waitFor("the project running with its name in the hosts file", "● shop", "hosts ok", "hosts     ● 1 name")
	if got := sb.hostsFile(); !hasShop(got) {
		t.Errorf("the hosts file: %q", got)
	}
	sb.waitAsked(1)
	if body, err := sb.get("shop.test", "/"); err != nil || !strings.HasPrefix(body, "shop:shop.test") {
		t.Errorf("the project answered %q, %v", body, err)
	}
	quit(term)
}

// Declining the prompt leaves the project stopped, saying why, and the
// hosts file as it was.
func TestDecliningTheWindowsPromptLeavesTheHostsFileAlone(t *testing.T) {
	sb, term := shopAsking(t)
	sb.answer("no")
	term.waitFor("the project stopped, with the reason", "○ shop", "hosts are not resolvable", "you declined the Windows prompt", "missing shop.test")
	if got := sb.hostsFile(); got != untouched {
		t.Errorf("the hosts file changed:\n%s", got)
	}
	sb.waitAsked(1)
	quit(term)
}

// A project left stopped by a declined prompt is started again with enter,
// as its page says, and that start asks Windows again: declined once more,
// then approved.
func TestEnterAsksWindowsAgainAfterADeclinedPrompt(t *testing.T) {
	sb, term := shopAsking(t)
	sb.answer("no")
	term.waitFor("the project stopped", "○ shop", "you declined the Windows prompt")

	term.click("○ shop")
	term.waitFor("the project's page, with what to do", "▌  ○ shop", "Press enter and approve the Windows prompt", "enter start")
	term.press(keyEnter)
	// The first refusal leaves the screen before the second can be on it.
	term.waitGone("the project to start", "○ shop")
	sb.waitAsked(2)
	sb.answer("no")
	term.waitFor("the project stopped again, with the reason", "○ shop", "you declined the Windows prompt", "enter start")
	if got := sb.hostsFile(); got != untouched {
		t.Errorf("the hosts file changed:\n%s", got)
	}

	term.press(keyEnter)
	term.waitGone("the project to start", "○ shop")
	sb.waitAsked(3)
	sb.answer("yes")
	term.waitFor("the project running", "● shop", "hosts ok")
	if got := sb.hostsFile(); !hasShop(got) {
		t.Errorf("the hosts file: %q", got)
	}
	quit(term)
}

// Quitting while the prompt is up does not wait for an answer: nearhost
// exits, and the hosts file is as it was.
func TestQuittingWhileWindowsAsksLeavesTheHostsFileAlone(t *testing.T) {
	sb, term := shopAsking(t)
	quit(term)
	if got := sb.hostsFile(); got != untouched {
		t.Errorf("the hosts file changed:\n%s", got)
	}
}
