package cli

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
)

// statusCore is a control backend that answers with a fixed status.
type statusCore struct{ st control.Status }

func (c statusCore) Info() control.Info               { return c.st.Info }
func (c statusCore) Status() control.Status           { return c.st }
func (c statusCore) State() control.State             { return control.State{Info: c.st.Info} }
func (c statusCore) Stop()                            {}
func (c statusCore) HostsChanged(ctx context.Context) {}

// serveStatus answers st on the control socket of the test's own config
// folder, where status looks.
func serveStatus(t *testing.T, st control.Status) {
	t.Helper()
	configHome(t)
	runDir, err := config.RunDir()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := control.Listen(control.SocketPath(runDir), statusCore{st}, control.NewHub())
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
