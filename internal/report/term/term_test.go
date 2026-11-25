package term

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/nearhost/nearhost/internal/report"
)

func render(m *report.Message, o Options) string {
	var b bytes.Buffer
	Render(&b, m, o)
	return b.String()
}

func cannotBind() *report.Message {
	return report.Errorf(report.ServiceCannotBind, "The service cannot run Nearhost from this copy yet.").
		Because("On Linux, a program may use ports below 1024, such as 443 for HTTPS and 80 for HTTP, "+
			"only when it has been given permission. The install script gives it; a nearhost you build "+
			"or copy yourself does not have it.").
		Step("To give it permission, run:", "sudo setcap cap_net_bind_service=+ep '/path/to/nearhost'").
		Then("Then run nearhost service install again.").
		SuggestDoctor()
}

func TestErrorLayout(t *testing.T) {
	want := `Error: The service cannot run Nearhost from this copy yet.

On Linux, a program may use ports below 1024, such as 443 for HTTPS and
80 for HTTP, only when it has been given permission. The install script
gives it; a nearhost you build or copy yourself does not have it.

To give it permission, run:

  sudo setcap cap_net_bind_service=+ep '/path/to/nearhost'

Then run nearhost service install again.

Run ` + "`nearhost doctor`" + ` to check this and the rest of what Nearhost needs.
`
	if got := render(cannotBind(), Options{Width: 80}); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestWarningAndNoteLabels(t *testing.T) {
	if got := render(report.Warnf(report.HostsPublic, "Careful."), Options{Width: 80}); got != "Warning: Careful.\n" {
		t.Fatalf("warning = %q", got)
	}
	if got := render(report.Notef(report.PresetsNote, "Worth knowing."), Options{Width: 80}); got != "Note: Worth knowing.\n" {
		t.Fatalf("note = %q", got)
	}
	if got := render(nil, Options{Width: 80}); got != "" {
		t.Fatalf("nil = %q", got)
	}
}

func TestNarrowWidthNeverWrapsCommands(t *testing.T) {
	cmd := "sudo setcap cap_net_bind_service=+ep '/home/someone/go/bin/nearhost'"
	cmd += strings.Repeat("x", 70-len(cmd))
	m := cannotBind()
	m.Fix[0].Command = cmd
	got := render(m, Options{Width: 40})
	found := false
	for _, l := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
		if l == "  "+cmd {
			found = true
			continue
		}
		if w := ansi.StringWidth(l); w > 40 {
			t.Errorf("line wider than 40 (%d): %q", w, l)
		}
	}
	if !found {
		t.Fatalf("the command is not on one line:\n%s", got)
	}
}

func TestColorKeepsText(t *testing.T) {
	m := cannotBind()
	plain := render(m, Options{Width: 80})
	colour := render(m, Options{Width: 80, Color: true})
	if !strings.Contains(colour, "\x1b[") {
		t.Fatal("Color: true printed no colour")
	}
	if ansi.Strip(colour) != plain {
		t.Fatalf("colour text differs from plain:\n%s\n---\n%s", ansi.Strip(colour), plain)
	}
}

func TestNoColorWithoutTerminalOrWithNO_COLOR(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if o := For(w, false); o.Color || o.Width != 80 || o.Verbose {
		t.Fatalf("For(pipe) = %+v", o)
	}
	if o := For(w, true); !o.Verbose {
		t.Fatal("For did not keep verbose")
	}

	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	if o := options(true, 120, env(nil), false); !o.Color || o.Width != 120 {
		t.Fatalf("terminal = %+v", o)
	}
	if o := options(true, 120, env(map[string]string{"NO_COLOR": "1"}), false); o.Color {
		t.Fatal("NO_COLOR=1 still has colour")
	}
	if o := options(true, 120, env(map[string]string{"TERM": "dumb"}), false); o.Color {
		t.Fatal("TERM=dumb still has colour")
	}
	if o := options(false, 0, env(nil), false); o.Color || o.Width != 80 {
		t.Fatalf("no terminal = %+v", o)
	}
}

func TestVerboseShowsCodeAndCauses(t *testing.T) {
	m := report.Errorf(report.PortNeedsPermission, "HTTPS port 443 needs permission to bind.").
		Wrap(fmt.Errorf("listen tcp :443: %w", fs.ErrPermission))
	quiet := render(m, Options{Width: 80})
	if strings.Contains(quiet, "port.needs-permission") || strings.Contains(quiet, "Cause:") {
		t.Fatalf("not verbose, but shows the code or cause:\n%s", quiet)
	}
	want := "Error: HTTPS port 443 needs permission to bind.\n" +
		"(port.needs-permission)\n" +
		"\n" +
		"Cause: listen tcp :443: permission denied\n" +
		"Cause: permission denied\n"
	if got := render(m, Options{Width: 80, Verbose: true}); got != want {
		t.Fatalf("verbose =\n%s\nwant\n%s", got, want)
	}
	plain := report.From(fmt.Errorf("boom"))
	if got := render(plain, Options{Width: 80, Verbose: true}); got != "Error: boom\n(unexpected)\n" {
		t.Fatalf("a cause that repeats the title is shown: %q", got)
	}
}

func TestContextShownAboveTitle(t *testing.T) {
	m := report.Errorf(report.PortNeedsPermission, "HTTPS port 443 needs permission to bind.")
	got := render(report.From(fmt.Errorf("start web: %w", m)), Options{Width: 80})
	if got != "Error: start web: HTTPS port 443 needs permission to bind.\n" {
		t.Fatalf("got %q", got)
	}
}

func TestScrubsANSIInText(t *testing.T) {
	m := report.Errorf(report.SSHUnreachable, "Server \x1b]52;c;AAAA\a<b>down</b>.").
		Because("ssh said: \x1b[31mPermission denied\x1b[0m\r\nsecond line").
		Step("Check \x1b[2Jit:", "ssh \x1b[1mhost").
		With("x", 1)
	got := render(m, Options{Width: 80, Verbose: true})
	if strings.ContainsAny(got, "\x1b\a\r") {
		t.Fatalf("output kept a control character: %q", got)
	}
	for _, want := range []string{"<b>down</b>", "Permission denied", "\nsecond line\n", "  ssh [1mhost\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
}
