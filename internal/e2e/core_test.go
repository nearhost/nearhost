package e2e

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/elevate"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/testutil"
)

// These journeys run Nearhost's core in the background, as `nearhost -d`
// starts it, and talk to it from other processes the way a person does from
// another terminal: `nearhost status`, `nearhost stop` and `nearhost hosts
// update`. The core is a process of its own, outside the terminal, so each
// journey stops it when it ends, whatever happened.

// coreWait is how long the core may take to start, serve or stop a project.
const coreWait = 30 * time.Second

// nearhost runs the nearhost binary in the sandbox with args, with no
// terminal, and returns what it printed and its exit code.
func (s *sandbox) nearhost(args ...string) (string, int) {
	s.t.Helper()
	cmd := exec.Command(nearhostBin, args...)
	cmd.Env = s.env
	cmd.Dir = s.dir
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return string(out), 0
	case errors.As(err, &ee):
		return string(out), ee.ExitCode()
	default:
		s.t.Fatalf("run nearhost %s: %v", strings.Join(args, " "), err)
		return "", -1
	}
}

// serveArgs are the flags that start nearhost on the sandbox's config and
// ports, with --no-hosts unless the sandbox manages its hosts file.
func (s *sandbox) serveArgs() []string {
	args := s.args[:len(s.args):len(s.args)]
	if !s.manageHosts {
		args = append(args, "--no-hosts")
	}
	return args
}

// detached matches what `nearhost -d` prints once the core answers.
var detached = regexp.MustCompile(`^Nearhost is running in the background \(pid (\d+)\)\. Stop it with nearhost stop\.\n$`)

// startCore starts the core in the background with `nearhost -d` and
// returns its pid. The core is stopped when the test ends.
func (s *sandbox) startCore() int {
	s.t.Helper()
	out, code := s.nearhost(append([]string{"-d"}, s.serveArgs()...)...)
	m := detached.FindStringSubmatch(out)
	if code != 0 || m == nil {
		s.t.Fatalf("nearhost -d: exit %d, printed:\n%s\nthe core log:\n%s", code, out, s.coreLog())
	}
	pid, _ := strconv.Atoi(m[1])
	s.t.Cleanup(func() { s.stopCore(pid) })
	return pid
}

// stopCore stops the core with `nearhost stop`, and kills it should it still
// answer: a failed journey must leave no core behind.
func (s *sandbox) stopCore(pid int) {
	if _, code := s.nearhost("status"); code == 3 {
		return
	}
	s.nearhost("stop")
	if _, code := s.nearhost("status"); code == 3 {
		return
	}
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}

// stopAnyCore stops the core running in the sandbox, if one is, as stopCore
// does: a failed journey must leave no core behind. A core still starting
// holds the instance lock before it answers, so it is asked again until the
// lock is free, for coreWait at most.
func (s *sandbox) stopAnyCore() {
	runDir := filepath.Join(s.dir, "nearhost", "run")
	deadline := time.Now().Add(coreWait)
	for {
		if out, code := s.nearhost("status", "--json"); code == 0 {
			var st control.Status
			if err := json.Unmarshal([]byte(out), &st); err != nil {
				s.t.Errorf("nearhost status --json printed %q: %v", out, err)
				return
			}
			s.stopCore(st.Info.PID)
		}
		if held, err := engine.InstanceRunning(runDir); err == nil && !held {
			return
		}
		if time.Now().After(deadline) {
			s.t.Errorf("a core still holds %s after %s; its log:\n%s", runDir, coreWait, s.coreLog())
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// coreLog is what the core has logged, in <config folder>/nearhost/core.log.
func (s *sandbox) coreLog() string {
	b, _ := os.ReadFile(filepath.Join(s.dir, "nearhost", "core.log"))
	return string(b)
}

// status is what `nearhost status --json` says of the running core.
func (s *sandbox) status() control.Status {
	s.t.Helper()
	out, code := s.nearhost("status", "--json")
	if code != 0 {
		s.t.Fatalf("nearhost status --json: exit %d, printed:\n%s", code, out)
	}
	var st control.Status
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		s.t.Fatalf("nearhost status --json printed %q: %v", out, err)
	}
	return st
}

// project is the project called name in st, and whether there is one.
func project(st control.Status, name string) (control.ProjectInfo, bool) {
	for _, p := range st.Projects {
		if p.Name == name {
			return p, true
		}
	}
	return control.ProjectInfo{}, false
}

// waitProject waits until the core says the project called name is in the
// given state, and returns what it says of it.
func (s *sandbox) waitProject(name, state string) control.ProjectInfo {
	s.t.Helper()
	var last control.Status
	deadline := time.Now().Add(coreWait)
	for {
		last = s.status()
		if p, ok := project(last, name); ok && p.State == state {
			return p
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("%s is not %s after %s; the core says %+v; its log:\n%s", name, state, coreWait, last.Projects, s.coreLog())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// `nearhost -d` starts the core and returns. The core serves the project
// with no terminal, and `status`, `-d` again and `stop`, each from a process
// of its own, find it through its socket. Once stopped, it is gone.
func TestTheCoreRunsInTheBackground(t *testing.T) {
	sb := newSandbox(t, fmt.Sprintf(localShop, t.TempDir(), testutil.Upstream(t, "shop")))
	pid := sb.startCore()

	shop := sb.waitProject("shop", "running")
	if want := fmt.Sprintf("https://shop.test:%d", sb.https); shop.URL != want {
		t.Errorf("shop's URL is %q, want %q", shop.URL, want)
	}
	if body, err := sb.get("shop.test", "/"); err != nil || !strings.HasPrefix(body, "shop:shop.test") {
		t.Errorf("the project answered %q, %v", body, err)
	}
	info := sb.status().Info
	if info.PID != pid || info.StartedBy != control.StartedDetached || !info.Reloads || info.API != control.APIVersion {
		t.Errorf("the core says %+v, want pid %d, started by detached, reloading", info, pid)
	}

	out, code := sb.nearhost("status")
	if code != 0 || !strings.HasPrefix(out, "Nearhost ") || !strings.Contains(out, fmt.Sprintf("is running (pid %d, started by detached at ", pid)) || !strings.Contains(out, "shop") {
		t.Errorf("nearhost status: exit %d, printed:\n%s", code, out)
	}

	// A second -d finds the first core and starts no other.
	out, code = sb.nearhost(append([]string{"-d"}, sb.serveArgs()...)...)
	if want := fmt.Sprintf("Nearhost is already running in the background (pid %d, started by detached).\n", pid); code != 0 || out != want {
		t.Errorf("a second nearhost -d: exit %d, printed %q, want %q", code, out, want)
	}

	if out, code := sb.nearhost("stop"); code != 0 || out != "Stopped.\n" {
		t.Fatalf("nearhost stop: exit %d, printed %q", code, out)
	}
	// Stop returns once the core has let go of everything.
	out, code = sb.nearhost("status")
	if code != 3 || !strings.HasPrefix(out, "Error: Nearhost is not running.") {
		t.Errorf("nearhost status after stop: exit %d, printed %q", code, out)
	}
	if _, err := sb.get("shop.test", "/"); err == nil {
		t.Errorf("the proxy still answers after stop")
	}
	if log := sb.coreLog(); !strings.Contains(log, "[shop] state: running") {
		t.Errorf("the core log does not say shop ran:\n%s", log)
	}
}

// A project added to the config while the core runs is served without a
// restart: the core reloads its config by itself.
func TestTheCoreInTheBackgroundReloadsItsConfig(t *testing.T) {
	sb := newSandbox(t, fmt.Sprintf(localShop, t.TempDir(), testutil.Upstream(t, "shop")))
	sb.startCore()
	sb.waitProject("shop", "running")

	blog := fmt.Sprintf(`  - name: blog
    server: local
    path: %s
    alias: blog.test
    enabled: true
    routes:
      - port: %d
        role: web
`, t.TempDir(), testutil.Upstream(t, "blog"))
	cfg, err := os.ReadFile(sb.config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sb.config, append(cfg, blog...), 0o600); err != nil {
		t.Fatal(err)
	}

	sb.waitProject("blog", "running")
	if body, err := sb.get("blog.test", "/"); err != nil || !strings.HasPrefix(body, "blog:blog.test") {
		t.Errorf("the added project answered %q, %v", body, err)
	}
	if body, err := sb.get("shop.test", "/"); err != nil || !strings.HasPrefix(body, "shop:shop.test") {
		t.Errorf("the first project answered %q after the reload, %v", body, err)
	}
}

// A core in the background never asks for a password: a project whose name
// the hosts file lacks waits, with a step that runs `nearhost hosts update`. That
// command, in a terminal, asks for the password, updates the file and tells
// the core, which then serves the project.
func TestHostsUpdateLetsTheCoreInTheBackgroundServe(t *testing.T) {
	// As in password_test.go: Windows asks through a dialog of its own, and
	// an elevated run writes the machine's hosts file alone.
	if runtime.GOOS == "windows" {
		t.Skip("Windows asks through its own prompt, not on the terminal")
	}
	if elevate.Elevated() {
		t.Skip("an elevated run writes the machine's hosts file alone")
	}
	sb := newSandbox(t, fmt.Sprintf(localShop, t.TempDir(), testutil.Upstream(t, "shop")))
	sb.manageHosts = true
	sb.startCore()

	shop := sb.waitProject("shop", "stopped")
	if p := shop.Problem; p == nil || !slices.ContainsFunc(p.Fix, func(s report.Step) bool { return s.Command == "nearhost hosts update" }) {
		t.Errorf("the stopped project's problem is %+v, want a step with the command that fixes it", p)
	}
	if got := sb.hostsFile(); got != untouched {
		t.Errorf("the core changed the hosts file:\n%s", got)
	}
	if ran := sb.elevated(); ran != "" {
		t.Errorf("the core ran %q with a password", ran)
	}

	term := start(t, sb.env, sb.dir, nearhostBin, "hosts", "update", "--config", sb.config)
	term.waitFor("the password prompt", "nearhost needs your password to update "+sb.hostsPath())
	term.typeText(password)
	term.press(keyEnter)
	term.waitFor("the update", "Updated "+sb.hostsPath()+".")
	if code := term.waitExit(); code != 0 {
		t.Fatalf("nearhost hosts update: exit %d; the screen:\n%s", code, term.screen())
	}
	if s := term.screen(); strings.Contains(s, password) {
		t.Errorf("the password shows on the screen:\n%s", s)
	}
	if got := sb.hostsFile(); !hasShop(got) {
		t.Errorf("the hosts file: %q", got)
	}

	sb.waitProject("shop", "running")
	if body, err := sb.get("shop.test", "/"); err != nil || !strings.HasPrefix(body, "shop:shop.test") {
		t.Errorf("the project answered %q, %v", body, err)
	}
}

// nearhost starts the core in the background when none runs. Quitting asks
// whether to stop it; left running, it goes on serving, as `nearhost status`
// says, until `nearhost stop`.
func TestTheInterfaceLeavesTheCoreRunning(t *testing.T) {
	sb, term := shop(t)
	term.press('q')
	term.waitFor("the quit box, Stop first", quitQuestion, "▸ [ Stop ]")
	term.press(keyTab)
	term.waitFor("Leave running chosen", "▸ [ Leave running ]")
	term.press(keyEnter)
	restored(term)

	sb.waitProject("shop", "running")
	if info := sb.status().Info; info.StartedBy != control.StartedTUI {
		t.Errorf("the core says it was started by %q, want tui", info.StartedBy)
	}
	if body, err := sb.get("shop.test", "/"); err != nil || !strings.HasPrefix(body, "shop:shop.test") {
		t.Errorf("the project answered %q, %v, with the interface gone", body, err)
	}
	if out, code := sb.nearhost("stop"); code != 0 || out != "Stopped.\n" {
		t.Fatalf("nearhost stop: exit %d, printed %q", code, out)
	}
	if out, code := sb.nearhost("status"); code != 3 {
		t.Errorf("nearhost status after stop: exit %d, printed %q", code, out)
	}
}

// nearhost on a core started with -d follows it: it shows what the core
// serves, and quitting asks nothing and leaves the core running.
func TestTheInterfaceAttachesToABackgroundCore(t *testing.T) {
	sb := newSandbox(t, fmt.Sprintf(localShop, t.TempDir(), testutil.Upstream(t, "shop")))
	pid := sb.startCore()
	sb.waitProject("shop", "running")

	term := sb.start()
	skipWelcome(term)
	term.waitFor("the core's project running", "● shop", "running   1 of 1 projects")
	leave(term)

	st := sb.status()
	if st.Info.PID != pid {
		t.Errorf("the core answering is pid %d, want %d", st.Info.PID, pid)
	}
	if p, ok := project(st, "shop"); !ok || p.State != "running" {
		t.Errorf("the core says shop is %+v once the interface quit", p)
	}
}
