package report

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestLogBlock(t *testing.T) {
	m := Errorf(PortNeedsPermission, "HTTPS port %d needs permission to bind.", 443).
		Because("On Linux, …").
		Step("To give it permission, run:", "sudo setcap cap_net_bind_service=+ep '/p/nearhost'").
		SuggestDoctor()
	want := "error [port.needs-permission] HTTPS port 443 needs permission to bind.\n" +
		"  On Linux, …\n" +
		"  Fix: sudo setcap cap_net_bind_service=+ep '/p/nearhost'\n" +
		"  Run `nearhost doctor` to check this and the rest of what Nearhost needs."
	if got := LogText(m); got != want {
		t.Fatalf("LogText =\n%s\nwant\n%s", got, want)
	}
}

func TestLogBlockStepsContextAndCause(t *testing.T) {
	m := From(fmt.Errorf("start web: %w", Warnf(HostsPublic, "Title.").
		Because("One.\n\nTwo.").
		Then("Then do this.").
		Wrap(errors.New("lookup failed"))))
	want := "warning [hosts.public] start web: Title.\n" +
		"  One.\n" +
		"  Two.\n" +
		"  Then do this.\n" +
		"  Cause: lookup failed"
	if got := LogText(m); got != want {
		t.Fatalf("LogText =\n%s\nwant\n%s", got, want)
	}
	if LogText(nil) != "" {
		t.Fatal("LogText(nil) is not empty")
	}
}

func TestLogBlockScrubsControlCharacters(t *testing.T) {
	m := Errorf(SSHUnreachable, "Server \x1b]0;pwned\a down.").Because("\x1b[31mx\ny").
		Step("run:", "ssh \x1b[2Jhost")
	got := LogText(m)
	if strings.ContainsAny(got, "\x1b\a") {
		t.Fatalf("LogText kept a control character: %q", got)
	}
	want := "error [ssh.unreachable] Server ]0;pwned down.\n" +
		"  [31mx\n" +
		"  y\n" +
		"  Fix: ssh [2Jhost"
	if got != want {
		t.Fatalf("LogText =\n%q\nwant\n%q", got, want)
	}
}
