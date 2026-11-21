package e2e

import (
	"fmt"
	"testing"
)

// A click on a row of the sidebar opens it, as moving to it with the keys
// does.
func TestAClickOpensASidebarRow(t *testing.T) {
	_, term := shop(t)
	term.waitFor("Home", "▌home", "recent ─")

	term.click("● shop")
	term.waitFor("the project's page", "▌  ● shop", "shop  https://shop.test", "enter stop")

	term.click("home ")
	term.waitFor("Home again", "▌home", "recent ─")
	quit(term)
}

// The wheel scrolls the full-screen log: up leaves the newest line and
// pauses the follow, down to the end follows again.
func TestTheWheelScrollsTheLog(t *testing.T) {
	sb, term := shop(t)
	const requests = 150 // several screens of them
	for i := range requests {
		if _, err := sb.get("shop.test", fmt.Sprintf("/page/%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	term.click("● shop")
	term.waitFor("the project's page", "shop  https://shop.test")
	term.press('l')
	last := fmt.Sprintf("/page/%d ", requests-1)
	term.waitFor("the log at its newest line", "nearhost › shop › log", "· following", last)

	// Back some way, to the middle of the log: neither end shows.
	term.wheel(last, -12)
	term.waitFor("the log scrolled back and paused", "· paused", "/page/")
	term.waitGone("both ends of the log to be off the screen", last, "/page/0 ")

	term.wheel("/page/", 200)
	term.waitFor("the log at its newest line again", "· following", last)
	term.press(keyEscape)
	term.waitFor("the project's page again", "shop  https://shop.test", "enter stop")
	quit(term)
}
