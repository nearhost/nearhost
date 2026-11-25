package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/service"
	"github.com/nearhost/nearhost/internal/tui"
)

// wantSteps fails the test unless m's fix steps are want, in order.
func wantSteps(t *testing.T, m *report.Message, want ...report.Step) {
	t.Helper()
	if len(m.Fix) != len(want) {
		t.Fatalf("steps %+v, want %+v", m.Fix, want)
	}
	for i := range want {
		if m.Fix[i] != want[i] {
			t.Errorf("step %d = %+v, want %+v", i, m.Fix[i], want[i])
		}
	}
}

// messageIn is the message in err's chain, or a failed test.
func messageIn(t *testing.T, err error) *report.Message {
	t.Helper()
	m, ok := report.As(err)
	if !ok {
		t.Fatalf("%v (%T) holds no message", err, err)
	}
	return m
}

func TestNoTerminalMessage(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			setHostOS(t, goos)
			m := messageIn(t, noTerminal())
			if m.Code != report.InterfaceNoTerminal || m.Level != report.Error {
				t.Errorf("code %q, level %v", m.Code, m.Level)
			}
			if m.Title != "The interface needs a terminal, and there is none here." {
				t.Errorf("title %q", m.Title)
			}
			wantSteps(t, m,
				report.Step{Text: "Run it in the background:", Command: "nearhost -d"},
				report.Step{Text: "Or serve without the interface:", Command: "nearhost up"})
			if got := strings.Contains(m.Why, "winpty"); got != (goos == "windows") {
				t.Errorf("why %q mentions winpty: %v", m.Why, got)
			}
			noNearhostPrefix(t, rendered(m))
		})
	}
}

func TestKnownAPIMessage(t *testing.T) {
	m := messageIn(t, knownAPI(control.Info{API: control.APIVersion + 1}))
	if m.Code != report.CoreAPIMismatch || m.Title != "Nearhost in the background speaks a different version of its API." {
		t.Errorf("code %q, title %q", m.Code, m.Title)
	}
	wantSteps(t, m,
		report.Step{Text: "Stop it:", Command: "nearhost stop"},
		report.Step{Text: "Then open the interface again:", Command: "nearhost"})
	if knownAPI(control.Info{API: control.APIVersion}) != nil {
		t.Error("the same API is refused")
	}
}

func TestSameConfigMessage(t *testing.T) {
	configHome(t)
	root := newRootCmd()
	other := filepath.Join(t.TempDir(), "other.yaml")
	if err := root.ParseFlags([]string{"--config", other}); err != nil {
		t.Fatal(err)
	}
	served := filepath.Join(t.TempDir(), "config.yaml")
	m := messageIn(t, sameConfig(root, served))
	if m.Code != report.CoreOtherConfig || m.Title != "Nearhost in the background serves "+served+"." {
		t.Errorf("code %q, title %q", m.Code, m.Title)
	}
	wantSteps(t, m,
		report.Step{Text: "To use " + other + ", stop it first:", Command: "nearhost stop"},
		report.Step{Text: "Or open the interface on the file it serves:", Command: "nearhost"})
}

func TestIgnoredFlagsIsANotice(t *testing.T) {
	root := newRootCmd()
	var f serveFlags
	f.bind(root)
	if err := root.ParseFlags([]string{"--https-port", "8443"}); err != nil {
		t.Fatal(err)
	}
	m := ignoredFlags(root, control.State{HTTPSPort: 443})
	if m == nil || m.Code != report.InterfaceFlagsIgnored || m.Level != report.Info {
		t.Fatalf("notice %+v", m)
	}
	if m.Title != "--https-port applies only when Nearhost starts; the running Nearhost keeps its settings." {
		t.Errorf("title %q", m.Title)
	}
	if ignoredFlags(newRootCmd(), control.State{}) != nil {
		t.Error("a notice with no flags given")
	}
}

func TestCoreLostTakesNoWriter(t *testing.T) {
	m := messageIn(t, coreLost(detachBackend{}, 0, control.StartedForeground))
	if m.Code != report.CoreLost || m.Title != "Nearhost stopped." || m.Why != "It ran in a terminal, which shows why." {
		t.Errorf("%+v", m)
	}
}

func TestInterruptedStartSaysWhatBecameOfTheCore(t *testing.T) {
	m := messageIn(t, interruptedStart{err: errors.New("x"), fate: fateNoAnswer}.Report())
	if m.Title != "Interrupted while Nearhost started in the background." {
		t.Errorf("title %q", m.Title)
	}
	if !strings.Contains(m.Why, "may still be starting") {
		t.Errorf("why %q", m.Why)
	}
	path, _ := config.LogPath()
	wantSteps(t, m,
		report.Step{Text: "See how it is going in its log:", Command: path},
		report.Step{Text: "Stop it:", Command: "nearhost stop"})
}

func TestHostsThroughCancelledIsAMessage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ensure := hostsThrough((&fakeRoot{needed: true, askErr: tui.ErrPasswordCancelled}).root(), path, nil)
	err := ensure(t.Context(), []string{"a.test"}, []string{"a.test"})
	m := messageIn(t, err)
	if m.Code != report.InterfacePasswordCancelled || !errors.Is(err, tui.ErrPasswordCancelled) {
		t.Errorf("code %q, err %v", m.Code, err)
	}
	if m.Title != "You cancelled the password, so a.test was not added to "+path+"." {
		t.Errorf("title %q", m.Title)
	}
}

// The update notice and ignored flags reach the interface's log as events
// that carry their message.
func TestUpdateNoticeIsAMessage(t *testing.T) {
	m := updateNotice("0.15.0", "0.16.0")
	if m.Code != report.UpdateAvailable || m.Level != report.Info || m.Title != "A new nearhost is available: 0.15.0 → 0.16.0." {
		t.Fatalf("%+v", m)
	}
	wantSteps(t, m, report.Step{Text: "Update it:", Command: "nearhost update"})
	ev := reportEvent(m)
	if ev.Message != m.Title || ev.Report != m {
		t.Errorf("event %+v", ev)
	}
}

func TestUpdateCheckCodes(t *testing.T) {
	exe := "/opt/homebrew/Caskroom/nearhost/0.5.0/nearhost"
	t.Run("failure exits 2", func(t *testing.T) {
		_, err := runUpdate(t, &fakeUpdate{err: errors.New("offline")}, "0.5.0", "", "--check")
		m := messageIn(t, err)
		if m.Code != report.UpdateCheckFailed || exitCode(err, &strings.Builder{}) != 2 {
			t.Errorf("code %q, exit %d", m.Code, exitCode(err, &strings.Builder{}))
		}
		if m.Title != "Nearhost could not find out whether a newer release is out." {
			t.Errorf("title %q", m.Title)
		}
		out := rendered(err)
		if !strings.HasPrefix(out, "Error: Nearhost could not find out") || !strings.Contains(out, "offline") {
			t.Errorf("rendered %q", out)
		}
		noNearhostPrefix(t, out)
	})
	t.Run("newer release exits 1", func(t *testing.T) {
		_, err := runUpdate(t, &fakeUpdate{latest: "0.6.0", exe: exe}, "0.5.0", "", "--check")
		m := messageIn(t, err)
		if m.Code != report.UpdateAvailable || m.Level != report.Info || exitCode(err, &strings.Builder{}) != 1 {
			t.Errorf("code %q, level %v", m.Code, m.Level)
		}
		if m.Title != "nearhost 0.6.0 is available; this is 0.5.0, installed with Homebrew." {
			t.Errorf("title %q", m.Title)
		}
		wantSteps(t, m, report.Step{Text: "Update it:", Command: "nearhost update"})
		if out := rendered(err); !strings.HasPrefix(out, "Note: nearhost 0.6.0 is available") {
			t.Errorf("rendered %q, want a note", out)
		}
		noNearhostPrefix(t, rendered(err))
	})
	t.Run("up to date exits 0", func(t *testing.T) {
		_, err := runUpdate(t, &fakeUpdate{latest: "0.5.0", exe: exe}, "0.5.0", "", "--check")
		if exitCode(err, &strings.Builder{}) != 0 {
			t.Errorf("err %v", err)
		}
	})
}

// Discovery's warnings go to standard error as Warning: lines; standard
// output holds the result alone.
func TestDiscoveryWarningsGoToStderr(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	saveConfig(t, cfgPath, &config.Config{Presets: []config.PresetRef{{Name: "gone"}}})
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("DB_PORT=5432\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := newRootCmd()
	root.AddCommand(newAddCmd(noRemote(t)))
	stdout, stderr, code := runRootSplit(t, root, "--config", cfgPath, "add", dir, "--name", "myapp")
	if code != 0 {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	for _, want := range []string{`Warning: preset "gone" is enabled but`, "Warning: DB_PORT is the only port Nearhost found"} {
		if !strings.Contains(flat(stderr), want) {
			t.Errorf("stderr %q lacks %q", stderr, want)
		}
	}
	if strings.Contains(stdout, "Warning:") || !strings.Contains(stdout, "Added myapp on local") {
		t.Errorf("stdout %q", stdout)
	}
	noNearhostPrefix(t, stdout+stderr)
}

func TestAddRefusalsAreMessages(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("A_PORT=1\nB_PORT=2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	_, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", dir, "--name", "myapp")
	m := messageIn(t, err)
	if m.Code != report.AddNoWebPort || m.Title != "Nearhost could not tell which port serves myapp, so nothing was saved." {
		t.Errorf("code %q, title %q", m.Code, m.Title)
	}
	noNearhostPrefix(t, rendered(err))

	_, err = execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", dir, "--server", "nope")
	if m := messageIn(t, err); m.Code != report.AddUnknownServer {
		t.Errorf("unknown server: code %q", m.Code)
	}
}

func TestServiceNotesAreNotes(t *testing.T) {
	x := newServiceFixture(t)
	x.m.changed = false
	x.b.exe = func() string { return "/home/u/.nvm/versions/node/v26.13.0/bin/nearhost" }
	root := newRootCmd()
	root.AddCommand(newServiceCmd(x.b))
	stdout, stderr, code := runRootSplit(t, root, "service", "install")
	if code != 0 {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	if !strings.HasPrefix(stderr, "Note: The service runs nearhost from Node v26.13.0's folder.") {
		t.Errorf("stderr %q", stderr)
	}
	if strings.Contains(stdout, "Node") {
		t.Errorf("stdout %q", stdout)
	}
}

func TestServiceErrorsAreMessages(t *testing.T) {
	if m := messageIn(t, service.ErrNoUserManager); m.Code != report.ServiceNoUserManager {
		t.Errorf("no user manager: %q", m.Code)
	}
	if m := messageIn(t, service.ErrUnsupported); m.Code != report.ServiceUnsupported {
		t.Errorf("unsupported: %q", m.Code)
	}
	x := newServiceFixture(t)
	x.b.euid = func() int { return 0 }
	_, err := x.run(t, "", "service", "install")
	m := messageIn(t, err)
	if m.Code != report.ServiceAsRoot || m.Title != "Install the service as yourself, not as root." {
		t.Errorf("code %q, title %q", m.Code, m.Title)
	}
	wantSteps(t, m, report.Step{Text: "Run it again without sudo:", Command: "nearhost service install"})

	x = newServiceFixture(t)
	x.m.err = errors.New("systemctl failed")
	_, err = x.run(t, "", "service", "install")
	if m := messageIn(t, err); m.Code != report.ServiceManagerFailed {
		t.Errorf("manager failure: %q", m.Code)
	}
}

func TestUninstallUnreadableIsAWarning(t *testing.T) {
	x := newServiceFixture(t)
	x.m.installed = false
	x.m.instErr = service.Unreadable(errors.New("no ExecStart"))
	root := newRootCmd()
	root.AddCommand(newServiceCmd(x.b))
	stdout, stderr, code := runRootSplit(t, root, "service", "uninstall")
	if code != 0 || !strings.HasPrefix(stderr, "Warning: The Nearhost service cannot be read, so it is removed as it is.") {
		t.Errorf("code %d, stderr %q", code, stderr)
	}
	if strings.Contains(stdout, "cannot be read") {
		t.Errorf("stdout %q", stdout)
	}
}

func TestSSHHintIsAStep(t *testing.T) {
	err := sshFailure("box", "add the project again", errors.New("Permission denied"))
	m := messageIn(t, err)
	if m.Code != report.SSHUnreachable || m.Title != "ssh box failed." {
		t.Errorf("code %q, title %q", m.Code, m.Title)
	}
	wantSteps(t, m,
		report.Step{Text: "Fix access by running ssh once in a terminal:", Command: "ssh box"},
		report.Step{Text: "Then add the project again."})
	// The interface and the agent API show the text it had.
	if want := "Permission denied; run `ssh box` once in a terminal to fix access, then add the project again"; err.Error() != want {
		t.Errorf("text %q, want %q", err.Error(), want)
	}
	out := rendered(err)
	if !strings.HasPrefix(out, "Error: ssh box failed.\n\n  Permission denied\n") {
		t.Errorf("rendered %q", out)
	}
}

func TestEventsCarryTheirMessage(t *testing.T) {
	for name, ev := range map[string]engine.Event{
		"missed":  missedEvent(),
		"browser": browserFailed("https://x.test/", errors.New("no browser")),
	} {
		if ev.Report == nil || ev.Message != ev.Report.Title {
			t.Errorf("%s: %+v", name, ev)
		}
	}
}

// errText is err as Main renders it, on one line.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return flat(rendered(err))
}

// noticeText is m as the terminal shows it, on one line, or "" for none.
func noticeText(m *report.Message) string {
	if m == nil {
		return ""
	}
	return flat(rendered(m))
}

// titles is the title of each of ms, which must be add.host-taken notices.
func titles(ms []*report.Message) []string {
	var out []string
	for _, m := range ms {
		if m.Code == report.AddHostTaken && m.Level == report.Info {
			out = append(out, m.Title)
		}
	}
	return out
}

// setcapWarning is how the warning after an update that the new binary may
// not bind ports 80 and 443 starts, up to its command.
const setcapWarning = "Warning: The new binary has no permission to bind ports 80 and 443.\n\n" +
	"An update replaces the binary, and the permission belongs to the file it\nwas given to.\n\n" +
	"To give it permission, run:\n\n  "

// The interface's hosts fixer words a refusal as the engine does: an inner
// message keeps its code and fix, the retry is the last step, and the title
// stays one line with no advice in it.
func TestHostsNotFixedKeepsTheInnerMessage(t *testing.T) {
	inner := report.Errorf(report.HostsReadOnly, "/etc/hosts is read-only.").
		Because("Something marked it so.").Step("Make it writable:", "chattr -i /etc/hosts")
	m := hostsNotFixed("web", fmt.Errorf("apply: %w", inner))
	if m.Code != report.HostsReadOnly {
		t.Errorf("code %q", m.Code)
	}
	if want := `project "web"'s hosts are not resolvable: the hosts file was not updated (/etc/hosts is read-only.)`; m.Title != want {
		t.Errorf("title %q, want %q", m.Title, want)
	}
	wantSteps(t, m,
		report.Step{Text: "Make it writable:", Command: "chattr -i /etc/hosts"},
		report.Step{Text: "Then start the project again to retry."})
	if e := engine.HostsProblem("web", inner); e.Title != m.Title || e.Code != m.Code {
		t.Errorf("the engine words it otherwise: %+v", e)
	}
	plain := hostsNotFixed("web", errors.New("sudo: no"))
	if plain.Code != report.HostsNeedsUpdate || strings.Contains(plain.Title, "retry") || len(plain.Fix) != 2 || plain.Fix[1].Command != "nearhost --no-hosts" {
		t.Errorf("%+v", plain)
	}
}

func TestRegistryNotTrustedIsAMessage(t *testing.T) {
	err := unreadableRegistry(config.Registry{Alias: "acme", Repo: "Acme/web-presets"}, errors.New("404 Not Found"))
	m := messageIn(t, err)
	if m.Code != report.PresetRegistryNotTrusted || m.Level != report.Error ||
		m.Title != "Registry acme (Acme/web-presets) was not trusted, so nothing was saved." ||
		m.Why != "Registry acme (Acme/web-presets) cannot be read: 404 Not Found." {
		t.Errorf("%+v", m)
	}
}

// add's steps give the command line to run again, with the flag to change.
func TestAddStepsRunItAgain(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("A_PORT=1\nB_PORT=2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	_, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", dir, "--name", "my app")
	m := messageIn(t, err)
	want := "nearhost add " + shellQuote(dir) + " --config " + shellQuote(cfgPath) + " --name 'my app' --web <port>"
	wantSteps(t, m, report.Step{Text: "Run it again, naming the port your app listens on:", Command: want})

	_, err = execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", dir, "--server", "nas")
	m = messageIn(t, err)
	if len(m.Fix) != 2 || m.Fix[0].Command != "- {name: nas, ssh_host: <ssh host>}" || !strings.HasPrefix(m.Fix[1].Command, "nearhost add ") {
		t.Errorf("steps %+v", m.Fix)
	}
}

func TestPresetUpdatesFailedIsAnError(t *testing.T) {
	if e, ok := report.Lookup(report.PresetUpdatesFailed); !ok || e.Level != report.Error {
		t.Errorf("%+v", e)
	}
}
