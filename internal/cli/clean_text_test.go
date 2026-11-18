package cli

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
)

// osc52 writes AAAA to the clipboard of a terminal that prints it.
const osc52 = "\x1b]52;c;AAAA\a"

// noControl fails t when out holds an ESC, a BEL or a carriage return.
func noControl(t *testing.T, out string) {
	t.Helper()
	if strings.ContainsAny(out, "\x1b\a\r") {
		t.Errorf("output carries control characters: %q", out)
	}
}

// A fatal error can quote a value from a project file or a server. Main
// prints it without its control characters, and keeps its lines.
func TestExitCodePrintsErrorsWithoutControlCharacters(t *testing.T) {
	var stderr strings.Builder
	err := fmt.Errorf(".nearhostrc: line 3: did not find expected key near \"x%s\r\"\nfix the file", osc52)
	if code := exitCode(err, &stderr); code != 1 {
		t.Fatalf("exit code = %d", code)
	}
	noControl(t, stderr.String())
	if !strings.Contains(stderr.String(), "\nfix the file\n") {
		t.Errorf("the second line is lost: %q", stderr.String())
	}
}

// An event message can carry text from ssh or the server; up prints it, and
// the project's name, without control characters, on one line.
func TestUpPrintsEventsWithoutControlCharacters(t *testing.T) {
	cfg := &config.Config{Servers: mini, Projects: []config.Project{remoteProject("api")}}
	var out bytes.Buffer
	p := &upPrinter{out: &out, eng: engine.New(cfg, engine.Options{}), cfg: cfg, advise: true}
	at := time.Date(2026, 10, 13, 9, 30, 0, 0, time.Local)
	p.event(engine.Event{Time: at, Project: "api", Message: "ssh: " + osc52 + "\n09:30:00 [api] forged"})
	p.event(engine.Event{Time: at, Project: "api", Server: "mini", State: engine.StateError, Message: "state: error: " + osc52})
	p.event(engine.Event{Time: at, Server: "mini", Message: "mini: Welcome \x1b]0;pwned\a日本語 ❤️"})
	noControl(t, out.String())
	if n := strings.Count(out.String(), "\n09:30:00 "); n != 2 {
		t.Errorf("a message's newline started a line of its own:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "日本語 ❤️") {
		t.Errorf("text in other scripts was lost: %q", out.String())
	}
}

// The summary names projects and their reasons, which can come from a
// remote directory name and a server.
func TestUpSummaryWithoutControlCharacters(t *testing.T) {
	proj := remoteProject("api" + osc52)
	cfg := &config.Config{Servers: mini, Projects: []config.Project{proj}}
	var out bytes.Buffer
	p := &upPrinter{out: &out, eng: engine.New(cfg, engine.Options{}), cfg: cfg}
	p.summary([]string{proj.Name}, []error{errors.New("ssh said " + osc52)})
	noControl(t, out.String())
}

// A label saved by an earlier version can hold an escape sequence; add's
// tables print it, and the project's name and path, without it.
func TestAddTablesPrintWithoutControlCharacters(t *testing.T) {
	p := remoteProject("api" + osc52)
	p.Path = "/srv/api" + osc52
	p.Routes = []config.Route{
		{Port: 80, Role: config.RoleWeb, Label: "web" + osc52 + "\n  9999  web  forged"},
		{Port: 81, Role: config.RoleLoopback, Hosts: nil, Label: "vite"},
	}
	var out bytes.Buffer
	if err := printRoutes(&out, p, false); err != nil {
		t.Fatal(err)
	}
	chooseWeb(bufio.NewReader(strings.NewReader("0\n")), &out, p.Routes)
	noControl(t, out.String())
	if strings.Contains(out.String(), "\n  9999") {
		t.Errorf("a label forged a row:\n%s", out.String())
	}
}
