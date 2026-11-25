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

// Verbatim lines go under the explanation, indented once more, untrimmed,
// and travel through JSON.
func TestVerbatimInLogAndJSON(t *testing.T) {
	m := Errorf(CoreLost, "Nearhost in the background stopped.").Because("The end of the log:").
		Quote("  indented line", "plain\x1b[2Jline").Then("Start it again.")
	want := "error [core.lost] Nearhost in the background stopped.\n  The end of the log:\n      indented line\n    plain[2Jline\n  Start it again."
	if got := LogText(m); got != want {
		t.Errorf("LogText:\n%q\nwant\n%q", got, want)
	}
	b, err := m.MarshalJSON()
	if err != nil || !strings.Contains(string(b), `"verbatim":["  indented line","plain\u001b[2Jline"]`) {
		t.Fatalf("JSON %s, %v", b, err)
	}
	var back Message
	if err := back.UnmarshalJSON(b); err != nil || len(back.Verbatim) != 2 || back.Verbatim[0] != "  indented line" {
		t.Errorf("round trip %+v, %v", back, err)
	}
	if p := Problem(m, 503, DocsBase); len(p["verbatim"].([]string)) != 2 {
		t.Errorf("problem %v", p)
	}
	if c := From(m); &c.Verbatim[0] == &m.Verbatim[0] {
		t.Error("From shares the verbatim lines")
	}
}
