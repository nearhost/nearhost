package cli

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/discovery"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/hosts"
	"github.com/nearhost/nearhost/internal/nearhostrc"
	"github.com/nearhost/nearhost/internal/tui"
)

// uiRoot is a root command whose bare form opens the interface through b.
func uiRoot(b uiBackend) *cobra.Command {
	root := newRootCmd()
	root.AddCommand(newVersionCmd())
	bindUI(root, b)
	return root
}

func runRoot(t *testing.T, root *cobra.Command, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return out.String(), err
}

// fakeUI is a backend whose interface run is recorded instead of drawn.
func fakeUI(t *testing.T, terminal bool, ran *bool) uiBackend {
	t.Helper()
	return uiBackend{
		interactive: func() bool { return terminal },
		run: func(m *tui.Model, attach func(*tea.Program)) error {
			*ran = true
			return nil
		},
		applier:   func(context.Context, bool) hosts.Applier { return func([]string) error { return nil } },
		readHosts: func() (string, error) { return "", nil },
		open:      func(string) error { return nil },
		workDir:   func() (string, error) { return t.TempDir(), nil },
	}
}

func TestBareNearhostWithoutATerminalPrintsHelp(t *testing.T) {
	ran := false
	out, err := runRoot(t, uiRoot(fakeUI(t, false, &ran)))
	if err != nil || ran || !strings.Contains(out, "Usage:") {
		t.Fatalf("err %v, ran %v, out:\n%s", err, ran, out)
	}
}

func TestBareNearhostOpensTheInterfaceWithNoConfigFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // the engine's run and CA directories
	ran := false
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if _, err := runRoot(t, uiRoot(fakeUI(t, true, &ran)), "--config", cfgPath, "--https-port", "8443", "--http-port", "8080"); err != nil || !ran {
		t.Fatalf("err %v, ran %v", err, ran)
	}
}

func TestBareNearhostRefusesWhatUpRefuses(t *testing.T) {
	ran := false
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("projects: [{name: a}, {name: a}]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runRoot(t, uiRoot(fakeUI(t, true, &ran)), "--config", cfgPath); err == nil || ran {
		t.Fatalf("invalid config: err %v, ran %v", err, ran)
	}
	if _, err := runRoot(t, uiRoot(fakeUI(t, true, &ran)), "--https-port", "80", "--http-port", "80"); err == nil || ran {
		t.Fatalf("same ports: err %v, ran %v", err, ran)
	}
	if _, err := runRoot(t, uiRoot(fakeUI(t, true, &ran)), "nosuchcommand"); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("unknown command: %v", err)
	}
}

func TestHostsThroughAsksOnlyWhenTheBlockChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(path, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeRoot{needed: true, sticks: true}
	var wrote [][]string
	var prompted []bool
	applier := func(_ context.Context, prompt bool) hosts.Applier {
		return func(aliases []string) error {
			wrote = append(wrote, aliases)
			prompted = append(prompted, prompt)
			return hosts.Apply(path, aliases)
		}
	}
	ensure := hostsThrough(f.root(), path, applier)
	if err := ensure(context.Background(), []string{"a.test", "b.test"}); err != nil {
		t.Fatal(err)
	}
	if err := ensure(context.Background(), []string{"a.test", "b.test"}); err != nil {
		t.Fatal(err)
	}
	if want := "Nearhost needs your password to add a.test, b.test to " + path + "."; strings.Join(f.asked, "|") != want {
		t.Errorf("asked %q, want %q", f.asked, want)
	}
	if len(wrote) != 1 || prompted[0] || f.suspended != 0 {
		t.Fatalf("wrote %v, prompted %v, suspended %d", wrote, prompted, f.suspended)
	}
	// A start that has ended by the time the block needs changing asks
	// nothing.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ensure(ctx, []string{"d.test"}); err == nil {
		t.Error("an ended start updated the hosts block")
	}
	if len(f.asked) != 1 {
		t.Error("an ended start asked for the password")
	}
	// A password cancelled says what was not done.
	cancelled := hostsThrough((&fakeRoot{needed: true, askErr: tui.ErrPasswordCancelled}).root(), path, applier)
	if err := cancelled(context.Background(), []string{"c.test"}); err == nil || err.Error() != "you cancelled the password, so c.test was not added to "+path {
		t.Errorf("cancelled = %v", err)
	}
}

func TestHostsState(t *testing.T) {
	u := &uiEngine{flags: serveFlags{noHosts: true}}
	if got := u.HostsState(); got != "not managed (--no-hosts)" {
		t.Errorf("--no-hosts: %q", got)
	}
}

// The first HostsState call happens while the interface is built, and must
// not wait on DNS; later calls apply the lookup.
func TestHostsStateFirstCallSkipsTheLookup(t *testing.T) {
	cfg := &config.Config{Projects: []config.Project{localProject("web", 8000, true)}}
	var asked []string
	u := &uiEngine{
		Engine: engine.New(cfg, engine.Options{}),
		b: uiBackend{
			readHosts: func() (string, error) { return "127.0.0.1 localhost\n", nil },
			lookup: func(_ context.Context, name string) ([]netip.Addr, error) {
				asked = append(asked, name)
				return nil, nil
			},
		},
	}
	if got := u.HostsState(); got != "checking" || len(asked) != 0 {
		t.Fatalf("first call = %q, lookups %v", got, asked)
	}
	if got := u.HostsState(); got != "missing web.test" {
		t.Errorf("second call = %q", got)
	}
}

// pendingUI is an interface backend whose config file at path has enabled
// "one" serving x.local.example.io and "two" serving two.local.example.io,
// and whose engine has a change pending for "two" that serves next's hosts.
func pendingUI(t *testing.T, next []string) (*uiEngine, string) {
	t.Helper()
	one := localProject("one", 8001, true)
	one.Routes[0].Hosts = []string{"x.local.example.io"}
	two := localProject("two", 8002, true)
	two.Routes[0].Hosts = []string{"two.local.example.io"}
	cfg := &config.Config{Projects: []config.Project{one, two}}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	changed := two
	changed.Routes = []config.Route{{Port: 8002, Role: config.RoleWeb, Hosts: next}}
	changed.Nearhostrc = &config.RcSnapshot{SHA256: "new", Routes: changed.Routes}
	var items []nearhostrc.Item
	for _, h := range next {
		items = append(items, nearhostrc.Item{Port: 8002, Host: h, Why: "a real domain"})
	}
	review := func(context.Context, config.Project, discovery.FileReader) engine.Review {
		return engine.Review{Pending: &engine.Change{SHA256: "new", Project: changed, Items: items}}
	}
	u := &uiEngine{Engine: engine.New(cfg, engine.Options{Review: review}), path: path}
	if err := u.Recheck(context.Background(), "two"); err != nil {
		t.Fatal(err)
	}
	if u.Status("two").Pending == nil {
		t.Fatal("nothing pending after the recheck")
	}
	return u, path
}

func TestAcceptRefusesAHostAnotherProjectServes(t *testing.T) {
	u, path := pendingUI(t, []string{"x.local.example.io"})
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	err = u.AcceptChange("two", "new")
	if want := `host x.local.example.io is already served by project "one"`; err == nil || err.Error() != want {
		t.Fatalf("AcceptChange = %v, want %s", err, want)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Errorf("the config changed:\n%s", after)
	}
}

// Accepting saves the change's routes and snapshot, and keeps the rest of
// the project as the file has it now.
func TestAcceptSavesOnlyTheRoutesAndSnapshot(t *testing.T) {
	u, path := pendingUI(t, []string{"app.local.example.io"})
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Projects[1].Path = "/tmp/moved"
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if err := u.AcceptChange("two", "new"); err != nil {
		t.Fatal(err)
	}
	cfg, err = config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := cfg.Project("two")
	if p.Path != "/tmp/moved" || p.Routes[0].Hosts[0] != "app.local.example.io" || p.Nearhostrc == nil || p.Nearhostrc.SHA256 != "new" {
		t.Errorf("saved %+v", p)
	}
}

// Declining drops the pending change.
func TestDeclineDropsTheChange(t *testing.T) {
	u, _ := pendingUI(t, []string{"app.local.example.io"})
	u.DeclineChange("two", "new")
	if st := u.Status("two"); st.Pending != nil {
		t.Errorf("still pending: %+v", st.Pending)
	}
}

// A change other than the one pending now is refused, and nothing is saved.
func TestAcceptRefusesAStaleChange(t *testing.T) {
	u, path := pendingUI(t, []string{"app.local.example.io"})
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	err = u.AcceptChange("two", "older")
	if want := "the .nearhostrc changed again; review it again"; err == nil || err.Error() != want {
		t.Fatalf("AcceptChange = %v, want %s", err, want)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Errorf("the config changed:\n%s", after)
	}
}

// A newer .nearhostrc that a review saved while the accept waited for the
// config's lock is not overwritten with the change the user approved.
func TestAcceptRefusesAChangeANewerReviewSaved(t *testing.T) {
	u, path := pendingUI(t, []string{"app.local.example.io"})
	var before []byte
	// The review saves after the accept checked the change and before it
	// holds the lock.
	u.acceptLocking = func() {
		if err := config.Update(path, func(cfg *config.Config) error {
			p, _ := cfg.Project("two")
			p.Nearhostrc = &config.RcSnapshot{SHA256: "newer", Routes: p.Routes}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		var err error
		if before, err = os.ReadFile(path); err != nil {
			t.Fatal(err)
		}
	}
	err := u.AcceptChange("two", "new")
	if want := "the .nearhostrc changed again; review it again"; err == nil || err.Error() != want {
		t.Fatalf("AcceptChange = %v, want %s", err, want)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Errorf("the config changed:\n%s", after)
	}
}

// failCAMove leaves a CA at the old path that cannot move, as the data
// directory's parent can't be written.
func failCAMove(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root writes to read-only directories")
	}
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(cfgHome, "nearhost", "caddy"), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := config.DataDir()
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(data)
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o700) })
	if note := migrateCANote(); !strings.Contains(note, "Warning") {
		t.Fatalf("the move did not fail: %q", note)
	}
}

// The interface hides stderr while it runs, so a CA that could not move is
// warned of in the interface's log, not on stderr.
func TestBareNearhostPutsTheMoveWarningInTheLog(t *testing.T) {
	failCAMove(t)
	ran := false
	b := fakeUI(t, true, &ran)
	var events <-chan engine.Event
	b.engineMade = func(eng *engine.Engine) {
		ch, unsubscribe := eng.Events().Subscribe(16)
		t.Cleanup(unsubscribe)
		events = ch
	}
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	out, err := runRoot(t, uiRoot(b), "--config", cfgPath, "--https-port", "8443", "--http-port", "8080")
	if err != nil || !ran {
		t.Fatalf("err %v, ran %v", err, ran)
	}
	if strings.Contains(out, "Warning") {
		t.Errorf("warned on stderr, hidden behind the interface:\n%s", out)
	}
	for logged := false; !logged; {
		select {
		case ev := <-events:
			logged = strings.Contains(ev.Message, "could not move its local CA")
		default:
			t.Fatal("the warning was not published to the interface's log")
		}
	}
}

// A run that fails before the interface exists prints the warning, which
// would otherwise be lost.
func TestBareNearhostPrintsTheMoveWarningWhenItFailsEarly(t *testing.T) {
	failCAMove(t)
	ran := false
	b := fakeUI(t, true, &ran)
	b.workDir = func() (string, error) { return "", errors.New("no working directory") }
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	out, err := runRoot(t, uiRoot(b), "--config", cfgPath, "--https-port", "8443", "--http-port", "8080")
	if err == nil || ran {
		t.Fatalf("err %v, ran %v", err, ran)
	}
	if !strings.Contains(out, "could not move its local CA") {
		t.Errorf("no warning on stderr:\n%s", out)
	}
}
