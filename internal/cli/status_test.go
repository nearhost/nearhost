package cli

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
)

// statusCore is a control backend that answers with a fixed status.
type statusCore struct {
	// The actions are the core's, which these tests never take: nil.
	*coreOps
	st control.Status
}

func (c statusCore) Info() control.Info               { return c.st.Info }
func (c statusCore) Status() control.Status           { return c.st }
func (c statusCore) State() control.State             { return control.State{Info: c.st.Info} }
func (c statusCore) Stop()                            {}
func (c statusCore) HostsChanged(ctx context.Context) {}

// PageKeyURL is the page's address with the core's key, which status
// prints on a terminal.
func (c statusCore) PageKeyURL() (string, error) {
	if c.st.Page == "" {
		return "", errors.New("Nearhost's page isn't served")
	}
	return c.st.Page + "/?k=the-core-s-key", nil
}

// serveStatus answers st on the control socket of the test's own config
// folder, where status looks.
func serveStatus(t *testing.T, st control.Status) {
	t.Helper()
	configHome(t)
	runDir, err := config.RunDir()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := control.Listen(control.SocketPath(runDir), statusCore{st: st}, control.NewHub())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
}

var testStatus = control.Status{
	Info: control.Info{
		API: control.APIVersion, Version: "1.2.3", PID: 4242,
		Started:   time.Date(2026, 11, 20, 9, 30, 0, 0, time.UTC),
		StartedBy: control.StartedDetached, Reloads: true,
	},
	Servers: []control.ServerInfo{
		{Name: "local", State: "connected"},
		{Name: "mini", State: "connecting", Message: "ssh: connection refused"},
	},
	Projects: []control.ProjectInfo{
		{Name: "web", State: "running", URL: "https://web.test"},
		{Name: "api", State: "starting", Message: "waiting for mini"},
	},
}

func TestStatusPrints(t *testing.T) {
	serveStatus(t, testStatus)
	out, err := execute(t, newStatusCmd(), "status")
	if err != nil {
		t.Fatal(err)
	}
	started := testStatus.Info.Started.Local().Format("2006-01-02 15:04:05")
	want := "Nearhost 1.2.3 is running (pid 4242, started by detached at " + started + ").\n" +
		"\n" +
		"SERVER  STATE       MESSAGE\n" +
		"local   connected\n" +
		"mini    connecting  ssh: connection refused\n" +
		"\n" +
		"PROJECT  STATE     URL               MESSAGE\n" +
		"web      running   https://web.test\n" +
		"api      starting                    waiting for mini\n"
	if out != want {
		t.Errorf("output:\n%s\nwant:\n%s", out, want)
	}
}

func TestStatusSaysWhenItDoesNotReload(t *testing.T) {
	st := testStatus
	st.Info.Reloads = false
	st.Info.StartedBy = control.StartedForeground
	st.Projects = nil
	serveStatus(t, st)
	out, err := execute(t, newStatusCmd(), "status")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, "started by foreground",
		"\nIt serves the projects it was started with and does not reload the config.\n",
		"\nNo projects are being served.")
}

func TestStatusJSON(t *testing.T) {
	serveStatus(t, testStatus)
	out, err := execute(t, newStatusCmd(), "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got control.Status
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if !got.Info.Started.Equal(testStatus.Info.Started) {
		t.Errorf("Started = %v", got.Info.Started)
	}
	got.Info.Started = testStatus.Info.Started
	if !reflect.DeepEqual(got, testStatus) {
		t.Errorf("got %+v\nwant %+v", got, testStatus)
	}
}

func TestStatusNotRunning(t *testing.T) {
	configHome(t)
	for _, args := range [][]string{{"status"}, {"status", "--json"}} {
		stdout, stderr, code := runRootSplit(t, statusRoot(), args...)
		want := "Nearhost is not running. Start it with nearhost -d, or open nearhost.\n"
		// JSON output is JSON or nothing; the message goes to stderr.
		gotMsg, gotOther := stdout, stderr
		if len(args) > 1 {
			gotMsg, gotOther = stderr, stdout
		}
		if code != 3 || gotMsg != want || gotOther != "" {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", args, code, stdout, stderr)
		}
	}
}

func statusRoot() *cobra.Command {
	root := newRootCmd()
	root.AddCommand(newStatusCmd())
	return root
}

// waitingStatus is testStatus with two requests waiting, and the page served.
func waitingStatus() control.Status {
	st := testStatus
	st.Page = "https://nearhost.localhost:6327"
	st.Approvals = []control.ApprovalInfo{
		{ID: "a1", Summary: "Claude Code on mini asks to open port 5173", URL: st.Page + "/approve/a1"},
		{ID: "web", Summary: "web's .nearhostrc asks for stripe.com", URL: st.Page + "/change/web"},
	}
	return st
}

// setStdoutTerminal makes status see stdout as a terminal, or not.
func setStdoutTerminal(t *testing.T, tty bool) {
	t.Helper()
	before := stdoutIsTerminal
	t.Cleanup(func() { stdoutIsTerminal = before })
	stdoutIsTerminal = func() bool { return tty }
}

func TestStatusListsApprovals(t *testing.T) {
	setStdoutTerminal(t, false)
	serveStatus(t, waitingStatus())
	out, err := execute(t, newStatusCmd(), "status")
	if err != nil {
		t.Fatal(err)
	}
	want := "\nWaiting for your answer:\n" +
		"  Claude Code on mini asks to open port 5173: https://nearhost.localhost:6327/approve/a1\n" +
		"  web's .nearhostrc asks for stripe.com: https://nearhost.localhost:6327/change/web\n"
	if !strings.Contains(out, want) {
		t.Errorf("output:\n%s\nwant it to contain:\n%s", out, want)
	}
	if strings.Contains(out, "?k=") {
		t.Errorf("a link carries the key:\n%s", out)
	}
}

func TestStatusApprovalWithoutPage(t *testing.T) {
	setStdoutTerminal(t, true)
	st := waitingStatus()
	st.Page = ""
	st.Approvals = []control.ApprovalInfo{{ID: "a1", Summary: "Claude Code on mini asks to open port 5173"}}
	serveStatus(t, st)
	out, err := execute(t, newStatusCmd(), "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Waiting for your answer:\n  Claude Code on mini asks to open port 5173\n") || strings.Contains(out, "Page:") {
		t.Errorf("output:\n%s", out)
	}
}

func TestStatusNoApprovalsNoHeading(t *testing.T) {
	setStdoutTerminal(t, false)
	serveStatus(t, testStatus)
	out, err := execute(t, newStatusCmd(), "status")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Waiting") || strings.Contains(out, "Page:") {
		t.Errorf("output:\n%s", out)
	}
}

func TestStatusPageKeyedOnTerminal(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	setStdoutTerminal(t, true)
	serveStatus(t, waitingStatus())
	out, err := execute(t, newStatusCmd(), "status")
	if err != nil {
		t.Fatal(err)
	}
	// The key is the core's, not one this process would find.
	if !strings.Contains(out, "\nPage: https://nearhost.localhost:6327/?k=the-core-s-key\n") {
		t.Errorf("output:\n%s", out)
	}
	// The approval links stay without the key.
	if strings.Count(out, "?k=") != 1 {
		t.Errorf("the key appears more than once:\n%s", out)
	}
}

func TestStatusPageUnkeyedInAPipe(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	setStdoutTerminal(t, false)
	serveStatus(t, waitingStatus())
	out, err := execute(t, newStatusCmd(), "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "\nPage: https://nearhost.localhost:6327\n") || strings.Contains(out, "?k=") {
		t.Errorf("output:\n%s", out)
	}
}

// JSON carries the new fields and never the key.
func TestStatusJSONHasApprovalsAndPage(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	setStdoutTerminal(t, true)
	serveStatus(t, waitingStatus())
	out, err := execute(t, newStatusCmd(), "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got control.Status
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Page != "https://nearhost.localhost:6327" || len(got.Approvals) != 2 || got.Approvals[0].ID != "a1" {
		t.Errorf("status: %+v", got)
	}
	if strings.Contains(out, "?k=") {
		t.Errorf("JSON carries the key:\n%s", out)
	}
}

// failingWriter fails each write that contains bad.
type failingWriter struct{ bad string }

var errClosedPipe = errors.New("closed pipe")

func (f failingWriter) Write(b []byte) (int, error) {
	if strings.Contains(string(b), f.bad) {
		return 0, errClosedPipe
	}
	return len(b), nil
}

// A write that fails, as to a closed pipe, is printStatus's error wherever
// it falls.
func TestPrintStatusReturnsAFailedWrite(t *testing.T) {
	noProjects := testStatus
	noProjects.Projects = nil
	for _, c := range []struct {
		name string
		st   control.Status
		bad  string
	}{
		{"no projects", noProjects, "No projects"},
		{"approvals", waitingStatus(), "Waiting"},
		{"page", waitingStatus(), "Page:"},
	} {
		if err := printStatus(failingWriter{c.bad}, c.st, ""); !errors.Is(err, errClosedPipe) {
			t.Errorf("%s: printStatus = %v", c.name, err)
		}
	}
}
