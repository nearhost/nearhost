package agentapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/doctor"
	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/testutil"
)

// The skill's scripts, run against the API: they must find the socket,
// speak the API and turn its answers into the exit statuses the skill reads.
// nearhost.sh serves Linux, macOS and servers; nearhost.ps1 serves Windows
// laptops, so each laptop-side case runs against both.

const (
	script   = "../../agents/nearhost/skills/nearhost/scripts/nearhost.sh"
	psScript = "../../agents/nearhost/skills/nearhost/scripts/nearhost.ps1"
)

// scriptShell is an interpreter and the skill script it runs.
type scriptShell struct {
	name   string
	argv   []string
	script string
}

// shScript runs nearhost.sh with sh.
var shScript = scriptShell{"sh", []string{"sh"}, script}

// origin is the machine the scripts' Nearhost serves in a test. nearhost.ps1
// runs only on the laptop, so its Nearhost serves the laptop's own projects;
// nearhost.sh's serves the server mini's.
func (s scriptShell) origin() string {
	if s.powerShell() {
		return config.LocalServer
	}
	return "mini"
}

// powerShell reports whether s runs nearhost.ps1.
func (s scriptShell) powerShell() bool { return s.script == psScript }

// run runs s's script as runScript does.
func (s scriptShell) run(t *testing.T, home string, env []string, args ...string) scriptResult {
	t.Helper()
	return runScriptIn(t, s, home, env, args...)
}

// testPowerShell names the PowerShell to run nearhost.ps1 with, as a path or
// a command on PATH, in place of the ones found on PATH. Set it to
// powershell.exe on Windows to test Windows PowerShell 5.1 alone.
const testPowerShell = "NEARHOST_TEST_POWERSHELL"

// powerShells are the PowerShells to run nearhost.ps1 with: pwsh where it
// is installed and, on Windows, Windows PowerShell 5.1 too, which the skill
// runs there. In CI, where the workflow installs one, finding none fails.
func powerShells(t *testing.T) []scriptShell {
	t.Helper()
	names := []string{"pwsh"}
	if runtime.GOOS == "windows" {
		names = append(names, "powershell.exe")
	}
	if v := os.Getenv(testPowerShell); v != "" {
		names = []string{v}
	}
	var shells []scriptShell
	for _, name := range names {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		label := strings.TrimSuffix(strings.ToLower(filepath.Base(path)), ".exe")
		shells = append(shells, scriptShell{label, []string{path, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File"}, psScript})
	}
	if len(shells) == 0 && os.Getenv("CI") != "" {
		t.Fatalf("no PowerShell (%s) to test nearhost.ps1 with", strings.Join(names, ", "))
	}
	return shells
}

// scriptShells are the shells each laptop-side case runs in: sh with
// nearhost.sh, except on Windows, where only nearhost.ps1 runs, and each
// PowerShell found with nearhost.ps1.
func scriptShells(t *testing.T) []scriptShell {
	t.Helper()
	var shells []scriptShell
	if runtime.GOOS != "windows" {
		shells = append(shells, shScript)
	}
	shells = append(shells, powerShells(t)...)
	if len(shells) == 0 {
		t.Skip("no shell to run the skill's script in")
	}
	return shells
}

type scriptResult struct {
	code           int
	stdout, stderr string
}

// runScript runs nearhost.sh with home as $HOME, a PATH without nearhost,
// and env added. It skips on Windows, which has no sh to run it with.
func runScript(t *testing.T, home string, env []string, args ...string) scriptResult {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("nearhost.sh serves Linux, macOS and servers, not Windows")
	}
	return runScriptIn(t, shScript, home, env, args...)
}

// runScriptIn is runScript with the interpreter and script of shell.
func runScriptIn(t *testing.T, shell scriptShell, home string, env []string, args ...string) scriptResult {
	t.Helper()
	path, err := filepath.Abs(shell.script)
	if err != nil {
		t.Fatal(err)
	}
	argv := append(append(append([]string{}, shell.argv...), path), args...)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(scriptEnv(t, shell, home), env...)
	cmd.Dir = home
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return scriptResult{code, out.String(), errOut.String()}
}

// scriptEnv is the environment a script runs in, with home as the user's
// home folder and a PATH without nearhost.
func scriptEnv(t *testing.T, shell scriptShell, home string) []string {
	t.Helper()
	if runtime.GOOS != "windows" {
		env := []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
		if shell.powerShell() {
			// PowerShell keeps its caches under the XDG folders, which
			// would otherwise land in home.
			dir := filepath.Join(os.TempDir(), "nearhost-test-powershell")
			env = append(env, "XDG_CONFIG_HOME="+dir, "XDG_DATA_HOME="+dir, "XDG_CACHE_HOME="+dir,
				"POWERSHELL_TELEMETRY_OPTOUT=1", "POWERSHELL_UPDATECHECK=Off")
		}
		return env
	}
	// Windows needs much of its environment, such as SystemRoot, to run
	// anything, so this keeps it but for what the scripts read.
	drop := map[string]bool{"HOME": true, "USERPROFILE": true, "PATH": true, "NEARHOST_SOCKET": true, "CLAUDECODE": true,
		"CLAUDE_CODE_SESSION_ID": true, "XDG_STATE_HOME": true, "SSH_CONNECTION": true}
	var env []string
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); !drop[strings.ToUpper(k)] {
			env = append(env, kv)
		}
	}
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	path := []string{filepath.Join(root, "System32"), root, filepath.Join(root, "System32", "WindowsPowerShell", "v1.0")}
	for _, name := range []string{shell.argv[0], "git"} {
		if p, err := exec.LookPath(name); err == nil {
			path = append(path, filepath.Dir(p))
		}
	}
	return append(env, "HOME="+home, "USERPROFILE="+home, "PATH="+strings.Join(path, string(os.PathListSeparator)))
}

// eachShell runs f as a subtest in each of scriptShells.
func eachShell(t *testing.T, f func(t *testing.T, shell scriptShell)) {
	t.Helper()
	for _, shell := range scriptShells(t) {
		t.Run(shell.name, func(t *testing.T) { f(t, shell) })
	}
}

// scriptServer serves origin on home/.nearhost/<id>.sock, with mini's
// projects.
func scriptServer(t *testing.T, home, id, origin string, review bool) (*Server, *clock) {
	t.Helper()
	return scriptServerOf(t, home, id, origin, review, fakeSource{origin: mini})
}

func scriptServerOf(t *testing.T, home, id, origin string, review bool, src Source) (*Server, *clock) {
	t.Helper()
	return scriptServerWith(t, home, id, origin, review, Options{Source: src})
}

// scriptServerWith is scriptServerOf with opts, which may add a Doctor or a
// Reload; its Laptop and Now are set here.
func scriptServerWith(t *testing.T, home, id, origin string, review bool, opts Options) (*Server, *clock) {
	t.Helper()
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed")
	}
	c := &clock{now: time.Now()}
	opts.Laptop, opts.Now = "dev@"+id, c.Now
	s := New(opts)
	if review {
		s.SetReviewer(&fakeReviewer{})
	}
	if err := s.Listen(origin, filepath.Join(home, ".nearhost", id+".sock")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, c
}

var proposalID = regexp.MustCompile(`"id":"([0-9a-f]+)"`)

func TestScriptFindsTheSocketAndReadsStatus(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		scriptServer(t, home, "k3f9q2", shell.origin(), true)
		r := shell.run(t, home, nil, "socket")
		if r.code != 0 || !strings.Contains(r.stdout, filepath.Join(home, ".nearhost", "k3f9q2.sock")+"\n") || !strings.Contains(r.stdout, `"origin":"`+shell.origin()+`"`) {
			t.Fatalf("socket: %+v", r)
		}
		os.MkdirAll(filepath.Join(home, "code", "myapp", "src"), 0o755)
		r = shell.run(t, home, nil, "status", "--all")
		if r.code != 0 || !strings.Contains(r.stdout, `"name":"inner"`) {
			t.Fatalf("status --all: %+v", r)
		}
		r = shell.run(t, home, nil, "status", "/home/dev/code/myapp/src")
		if r.code != 1 || !strings.Contains(r.stderr, "is not a directory here") {
			t.Fatalf("status of a path not here: %+v", r)
		}
	})
}

func TestScriptProposesAndWaitsForTheAnswer(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		s, c := scriptServer(t, home, "k3f9q2", shell.origin(), true)
		app := filepath.Join(home, "my app")
		os.Mkdir(app, 0o755)
		r := shell.run(t, home, nil, "propose", "--web", "5173", "--name", `the "app"`, app)
		m := proposalID.FindStringSubmatch(r.stdout)
		if r.code != 0 || m == nil {
			t.Fatalf("propose: %+v", r)
		}
		id := m[1]
		s.mu.Lock()
		p := s.proposals[id]
		s.mu.Unlock()
		if p.Path != app || p.Web != 5173 || p.Name != `the "app"` {
			t.Fatalf("proposed %+v", p.Proposal)
		}

		go func() {
			time.Sleep(200 * time.Millisecond)
			s.Accept(id, "myapp")
		}()
		r = shell.run(t, home, nil, "wait", id)
		if r.code != 0 || !strings.Contains(r.stdout, `"state":"accepted"`) || !strings.Contains(r.stdout, `"url":"https://myapp.test"`) {
			t.Fatalf("wait: %+v", r)
		}

		r = shell.run(t, home, nil, "propose", app)
		id = proposalID.FindStringSubmatch(r.stdout)[1]
		s.Decline(id, "no ports found")
		if r := shell.run(t, home, nil, "wait", id); r.code != 6 || !strings.Contains(r.stdout, `"reason":"no ports found"`) {
			t.Fatalf("declined: %+v", r)
		}

		r = shell.run(t, home, nil, "propose", app)
		id = proposalID.FindStringSubmatch(r.stdout)[1]
		c.add(10 * time.Minute)
		if r := shell.run(t, home, nil, "wait", id); r.code != 7 || !strings.Contains(r.stdout, `"state":"expired"`) {
			t.Fatalf("expired: %+v", r)
		}
	})
}

func TestScriptReportsAProjectThatExists(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		dir := filepath.Join(home, "inner")
		os.Mkdir(dir, 0o755)
		real, _ := filepath.EvalSymlinks(dir)
		scriptServerOf(t, home, "k3f9q2", shell.origin(), true, fakeSource{shell.origin(): {{Name: "inner", Path: real, URL: "https://inner.test", State: "running"}}})
		r := shell.run(t, home, nil, "propose", dir)
		if r.code != 0 || !strings.Contains(r.stdout, `"state":"exists"`) || !strings.Contains(r.stdout, "https://inner.test") {
			t.Fatalf("%+v", r)
		}
	})
}

func TestScriptSaysWhenNearhostRunsWithoutItsInterface(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		scriptServer(t, home, "k3f9q2", shell.origin(), false)
		if r := shell.run(t, home, nil, "propose", home); r.code != 3 || !strings.Contains(r.stdout, "Nearhost on dev@k3f9q2 has no page or window to ask you in") {
			t.Fatalf("%+v", r)
		}
	})
}

func TestScriptAsksWhichLaptopWhenSeveralAnswer(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		scriptServer(t, home, "aaaaaa", shell.origin(), true)
		scriptServer(t, home, "bbbbbb", shell.origin(), true)
		r := shell.run(t, home, nil, "socket")
		if r.code != 4 || !strings.Contains(r.stderr, "aaaaaa.sock  (dev@aaaaaa)") || !strings.Contains(r.stderr, "bbbbbb.sock  (dev@bbbbbb)") {
			t.Fatalf("%+v", r)
		}
		chosen := filepath.Join(home, ".nearhost", "bbbbbb.sock")
		if r := shell.run(t, home, []string{"NEARHOST_SOCKET=" + chosen}, "socket"); r.code != 0 || !strings.Contains(r.stdout, `"laptop":"dev@bbbbbb"`) {
			t.Fatalf("with NEARHOST_SOCKET: %+v", r)
		}
	})
}

func TestScriptSkipsSocketsNothingAnswersOn(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		scriptServer(t, home, "k3f9q2", shell.origin(), true)
		// A socket a run that has ended left, as sshd leaves them.
		l, err := net.Listen("unix", filepath.Join(home, ".nearhost", "zzzzzz.sock"))
		if err != nil {
			t.Fatal(err)
		}
		l.(*net.UnixListener).SetUnlinkOnClose(false)
		l.Close()
		if r := shell.run(t, home, nil, "socket"); r.code != 0 || !strings.Contains(r.stdout, "k3f9q2.sock") {
			t.Fatalf("%+v", r)
		}
	})
}

func TestScriptSaysWhatToDoWhenNoNearhostAnswers(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed")
	}
	home := shortDir(t)
	r := runScript(t, home, []string{"SSH_CONNECTION=10.0.0.2 50000 10.0.0.9 22"}, "propose", home)
	host, _ := os.Hostname()
	host, _, _ = strings.Cut(host, ".")
	if r.code != 2 || !strings.Contains(r.stderr, "start nearhost there and connect this server: select "+host+" and press c, or, while Nearhost has nothing to serve, press c on its first screen and choose "+host+".") ||
		!strings.Contains(r.stderr, "nearhost add --server ") {
		t.Fatalf("on a server: %+v", r)
	}
	// Without SSH_CONNECTION, as in an agent's remote session that is not
	// ssh, this may be a server or a machine without Nearhost: the message
	// gives both.
	r = runScript(t, home, nil, "status")
	for _, want := range []string{
		"No Nearhost reaches this machine.",
		"If you reach it from a laptop that runs Nearhost, connect it there: start nearhost, select " + host + " and press c, or, while Nearhost has nothing to serve, press c on its first screen and choose " + host + ". Then ask again.",
		"If " + host + " is not in Nearhost yet, run this on the laptop: nearhost add --server " + host + " ",
		"To serve projects on this machine itself, install Nearhost here: https://nearhost.dev/docs/",
	} {
		if r.code != 2 || !strings.Contains(r.stderr, want) {
			t.Fatalf("without ssh, want %q in %+v", want, r)
		}
	}
}

func TestScriptRefusesAnAPIItDoesNotSpeak(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		if _, err := exec.LookPath("curl"); err != nil {
			t.Skip("curl is not installed")
		}
		home := shortDir(t)
		os.MkdirAll(filepath.Join(home, ".nearhost"), 0o700)
		l, err := net.Listen("unix", filepath.Join(home, ".nearhost", "k3f9q2.sock"))
		if err != nil {
			t.Fatal(err)
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"api":2,"interface":true,"laptop":"dev@future","origin":"mini"}`))
		})}
		go srv.Serve(l)
		defer srv.Close()
		if r := shell.run(t, home, nil, "socket"); r.code != 5 || !strings.Contains(r.stderr, "update the nearhost plugin") {
			t.Fatalf("%+v", r)
		}
	})
}

func TestScriptStatusFailsWhenNoProjectHoldsThePath(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		scriptServer(t, home, "k3f9q2", shell.origin(), true)
		r := shell.run(t, home, nil, "status", home)
		if r.code != 1 || !strings.Contains(r.stdout, "no project holds") {
			t.Fatalf("%+v", r)
		}
	})
}

func TestScriptFindsAProjectFromASymlinkedDirectory(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		real := filepath.Join(home, "code", "inner")
		os.MkdirAll(real, 0o755)
		resolved, _ := filepath.EvalSymlinks(real)
		link := filepath.Join(home, "link")
		testutil.Symlink(t, real, link)
		s, _ := scriptServerOf(t, home, "k3f9q2", shell.origin(), true, fakeSource{shell.origin(): {{Name: "inner", Path: resolved, URL: "https://inner.test"}}})
		if r := shell.run(t, home, nil, "status", link); r.code != 0 || !strings.Contains(r.stdout, `"name":"inner"`) {
			t.Fatalf("status from the link: %+v", r)
		}
		if r := shell.run(t, home, nil, "propose", link); r.code != 0 || !strings.Contains(r.stdout, `"state":"exists"`) {
			t.Fatalf("propose from the link: %+v", r)
		}
		other := filepath.Join(home, "other")
		os.Mkdir(other, 0o755)
		testutil.Symlink(t, other, filepath.Join(home, "otherlink"))
		r := shell.run(t, home, nil, "propose", filepath.Join(home, "otherlink"))
		id := proposalID.FindStringSubmatch(r.stdout)
		if r.code != 0 || id == nil {
			t.Fatalf("propose a new one from a link: %+v", r)
		}
		s.mu.Lock()
		got := s.proposals[id[1]].Path
		s.mu.Unlock()
		if got != filepath.Join(home, "otherlink") {
			t.Fatalf("proposed %q, want the path as the agent sees it", got)
		}
	})
}

func TestScriptProposesAPathWithATab(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		if runtime.GOOS == "windows" {
			t.Skip("Windows names have no tabs")
		}
		s, _ := scriptServer(t, home, "k3f9q2", shell.origin(), true)
		dir := filepath.Join(home, "a\tb")
		os.Mkdir(dir, 0o755)
		r := shell.run(t, home, nil, "propose", dir)
		id := proposalID.FindStringSubmatch(r.stdout)
		if r.code != 0 || id == nil {
			t.Fatalf("%+v", r)
		}
		s.mu.Lock()
		got := s.proposals[id[1]].Path
		s.mu.Unlock()
		if !strings.HasSuffix(got, string(filepath.Separator)+"a\tb") {
			t.Fatalf("proposed %q", got)
		}
	})
}

func TestScriptNamesTheProposedPathWhenNoNearhostAnswers(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed")
	}
	home := shortDir(t)
	dir := filepath.Join(home, "proj")
	os.Mkdir(dir, 0o755)
	r := runScript(t, home, []string{"SSH_CONNECTION=10.0.0.2 50000 10.0.0.9 22"}, "propose", dir)
	if r.code != 2 || !strings.Contains(r.stderr, "proj\n") {
		t.Fatalf("%+v", r)
	}
}

// scriptProject makes a directory that Nearhost holds as origin's project
// myapp.
func scriptProject(t *testing.T, home, origin string) (dir string, src Source) {
	t.Helper()
	dir = filepath.Join(home, "myapp")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(dir)
	return dir, fakeSource{origin: {{Name: "myapp", Path: real, URL: "https://myapp.test", State: "running"}}}
}

func TestScriptDoctorPrintsFindings(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		dir, src := scriptProject(t, home, shell.origin())
		d := &fakeDoctor{findings: []doctor.Finding{{ID: "routes.stale-port", Severity: "warning", Project: "myapp", Message: "nothing listens"}}}
		scriptServerWith(t, home, "k3f9q2", shell.origin(), true, Options{Source: src, Doctor: d})
		r := shell.run(t, home, nil, "doctor", dir)
		if r.code != 0 || !strings.Contains(r.stdout, `"findings"`) || !strings.Contains(r.stdout, "routes.stale-port") {
			t.Fatalf("doctor: %+v", r)
		}

		other := shortDir(t)
		scriptServerWith(t, other, "z9", shell.origin(), true, Options{Source: src})
		r = shell.run(t, other, nil, "doctor", ".")
		if r.code != 5 || !strings.Contains(r.stderr, "update Nearhost") || !strings.Contains(r.stderr, "doctor") {
			t.Fatalf("without a doctor: %+v", r)
		}
	})
}

func TestScriptReloadAndWait(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		dir, src := scriptProject(t, home, shell.origin())
		rl := &fakeReloader{}
		rl.answer(waiting, nil)
		s, _ := scriptServerWith(t, home, "k3f9q2", shell.origin(), true, Options{Source: src, Reload: rl})
		r := shell.run(t, home, nil, "reload", dir)
		m := proposalID.FindStringSubmatch(r.stdout)
		if r.code != 0 || m == nil || !strings.Contains(r.stdout, `"state":"pending"`) {
			t.Fatalf("reload: %+v", r)
		}
		id := m[1]

		go func() {
			time.Sleep(200 * time.Millisecond)
			s.ChangeAnswered("myapp", "b", true)
		}()
		if r := shell.run(t, home, nil, "wait", id); r.code != 0 || !strings.Contains(r.stdout, `"state":"accepted"`) {
			t.Fatalf("wait: %+v", r)
		}

		rl.answer(Reloaded{Items: []string{"a.example.com"}, SHA256: "c"}, nil)
		first := proposalID.FindStringSubmatch(shell.run(t, home, nil, "reload", dir).stdout)[1]
		rl.answer(Reloaded{Items: []string{"b.example.com"}, SHA256: "d"}, nil)
		if r := shell.run(t, home, nil, "reload", dir); r.code != 0 || proposalID.FindStringSubmatch(r.stdout)[1] == first {
			t.Fatalf("changed reload: %+v", r)
		}
		if r := shell.run(t, home, nil, "wait", first); r.code != 8 || !strings.Contains(r.stdout, `"state":"superseded"`) {
			t.Fatalf("superseded: %+v", r)
		}
	})
}

func TestScriptReloadUnderUp(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		dir, src := scriptProject(t, home, shell.origin())
		rl := &fakeReloader{}
		rl.answer(waiting, nil)
		scriptServerWith(t, home, "k3f9q2", shell.origin(), false, Options{Source: src, Reload: rl})
		if r := shell.run(t, home, nil, "reload", dir); r.code != 3 {
			t.Fatalf("%+v", r)
		}
	})
}

func TestScriptReloadDeclined(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		dir, src := scriptProject(t, home, shell.origin())
		rl := &fakeReloader{}
		rl.answer(Reloaded{Declined: true}, nil)
		scriptServerWith(t, home, "k3f9q2", shell.origin(), true, Options{Source: src, Reload: rl})
		if r := shell.run(t, home, nil, "reload", dir); r.code != 6 || !strings.Contains(r.stdout, `"state":"declined"`) {
			t.Fatalf("%+v", r)
		}
	})
}

// A Nearhost from before the doctor answers GET /v1 without capabilities.
// The skill runs doctor after an add and skips it on exit status 5.
func TestScriptDoctorOnANearhostWithoutCapabilities(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		if _, err := exec.LookPath("curl"); err != nil {
			t.Skip("curl is not installed")
		}
		home := shortDir(t)
		os.MkdirAll(filepath.Join(home, ".nearhost"), 0o700)
		l, err := net.Listen("unix", filepath.Join(home, ".nearhost", "k3f9q2.sock"))
		if err != nil {
			t.Fatal(err)
		}
		var calls []string
		var mu sync.Mutex
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			calls = append(calls, r.URL.Path)
			mu.Unlock()
			w.Write([]byte(`{"api":1,"interface":true,"laptop":"dev@older","origin":"mini"}`))
		})}
		go srv.Serve(l)
		defer srv.Close()
		for _, cmd := range []string{"doctor", "reload"} {
			if r := shell.run(t, home, nil, cmd, home); r.code != 5 || !strings.Contains(r.stderr, "Nearhost on dev@older doesn't support "+cmd) {
				t.Fatalf("%s: %+v", cmd, r)
			}
		}
		mu.Lock()
		defer mu.Unlock()
		for _, c := range calls {
			if c != "/v1" {
				t.Fatalf("called %s", c)
			}
		}
	})
}

// The script waits for the doctor and a reload longer than the server
// gives them, so it reads the server's answer rather than giving up first.
func TestScriptWaitsForTheDoctorAndReloadLongerThanTheServer(t *testing.T) {
	for file, call := range map[string]string{
		script:   `(?s)\n%s\)\n.*?\bcall "\$SOCK" ([0-9]+) `,
		psScript: `(?s)\n\t'%s' \{\n.*?\b(?:Invoke-PathCall|Invoke-Api) [^\n]*?-Limit ([0-9]+) `,
	} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		// A Windows checkout may have CRLF line endings.
		data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
		for cmd, server := range map[string]time.Duration{"doctor": doctorTimeout, "reload": reloadTimeout} {
			m := regexp.MustCompile(fmt.Sprintf(call, cmd)).FindSubmatch(data)
			if m == nil {
				t.Fatalf("no call in %s of %s", cmd, filepath.Base(file))
			}
			secs, _ := strconv.Atoi(string(m[1]))
			if time.Duration(secs)*time.Second <= server {
				t.Errorf("%s in %s: the script waits %ds, the server up to %s", cmd, filepath.Base(file), secs, server)
			}
		}
	}
}

// An id or port with a trailing newline is a usage error, not one the
// script passes on: .NET's $ would match before that newline.
func TestScriptRefusesATrailingNewline(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		for _, args := range [][]string{{"wait", "abc\n"}, {"passthrough-wait", "abc\n"}, {"open-wait", "abc\n"}, {"passthrough", "3000\n"}} {
			if r := shell.run(t, home, nil, args...); r.code != 1 || r.stdout != "" {
				t.Errorf("%q: %+v", args, r)
			}
		}
	})
}

// The skill runs nearhost.ps1 on Windows and nearhost.sh everywhere else,
// and lets Claude Code run either without asking, from Bash or PowerShell.
func TestSkillRunsThePowerShellScriptOnWindows(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(filepath.Dir(script), "..", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	skill := strings.ReplaceAll(string(data), "\r\n", "\n")
	front, _, ok := strings.Cut(strings.TrimPrefix(skill, "---\n"), "\n---\n")
	if !ok {
		t.Fatal("SKILL.md has no frontmatter")
	}
	// Git Bash would drop backslashes, so the path has slashes, quoted.
	ps := `powershell -NoProfile -ExecutionPolicy Bypass -File "${CLAUDE_SKILL_DIR}/scripts/nearhost.ps1" *`
	for _, want := range []string{"Bash(sh ${CLAUDE_SKILL_DIR}/scripts/nearhost.sh *)", "Bash(" + ps + ")", "PowerShell(" + ps + ")"} {
		if !strings.Contains(front+"\n", "\n  - "+want+"\n") {
			t.Errorf("allowed-tools lacks %s", want)
		}
	}
	if !strings.Contains(skill, "On Windows (not inside WSL), run `powershell -NoProfile -ExecutionPolicy Bypass -File \"<this skill's directory>/scripts/nearhost.ps1\" <command>`; everywhere else, `sh <this skill's directory>/scripts/nearhost.sh <command>`") {
		t.Error("the skill does not say which script to run where")
	}
	if strings.Contains(skill, "sh .../nearhost.sh") {
		t.Error("a step runs nearhost.sh on every platform")
	}
	// The nearhost program is on a Windows laptop's PATH too, so a step that
	// means the script must not read as that program.
	for _, cmd := range []string{"socket", "status", "propose", "wait", "doctor", "reload", "passthrough", "open"} {
		if regexp.MustCompile("`nearhost " + cmd + "\\b").MatchString(skill) {
			t.Errorf("a step names the script as `nearhost %s`, which is the nearhost program", cmd)
		}
	}
	if strings.Contains(skill, `\scripts\`) {
		t.Error("a path to the script has backslashes, which Git Bash drops")
	}
}

// On the laptop with the nearhost program but no Nearhost running, both
// scripts say to add the project and start it.
func TestScriptSaysNearhostIsNotRunningHere(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed")
	}
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		bin := filepath.Join(home, "bin")
		if err := os.Mkdir(bin, 0o700); err != nil {
			t.Fatal(err)
		}
		name, text := "nearhost", "#!/bin/sh\n"
		if runtime.GOOS == "windows" {
			name, text = "nearhost.cmd", "@echo off\r\n"
		}
		if err := os.WriteFile(filepath.Join(bin, name), []byte(text), 0o700); err != nil {
			t.Fatal(err)
		}
		path := bin + string(os.PathListSeparator) + "/usr/bin:/bin"
		if runtime.GOOS == "windows" {
			path = bin + string(os.PathListSeparator) + filepath.Join(os.Getenv("SystemRoot"), "System32")
		}
		r := shell.run(t, home, []string{"PATH=" + path}, "status")
		if r.code != 2 || r.stderr != "Nearhost is not running on this machine. Run: nearhost add <path>, then start nearhost to serve it.\n" {
			t.Fatalf("%+v", r)
		}
	})
}

// Windows is only ever the laptop, so without the nearhost program
// nearhost.ps1 says to install it, where nearhost.sh walks through a server
// too.
func TestPowerShellScriptSaysNearhostIsNotInstalled(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed")
	}
	for _, shell := range powerShells(t) {
		t.Run(shell.name, func(t *testing.T) {
			r := shell.run(t, shortDir(t), nil, "status")
			if r.code != 2 || r.stderr != "Nearhost is not installed on this machine. To serve projects here, install it: https://nearhost.dev/docs/\n" {
				t.Fatalf("%+v", r)
			}
		})
	}
}

// The skill's instructions for what the script answers. A reload re-reads
// .nearhostrc and nothing else, so only the findings about that file call
// for one; and after an add the doctor waits while the project starts, for
// about 30 seconds at most, since a degraded or stopped project never runs,
// and is skipped on a Nearhost without it.
func TestSkillReloadsOnlyForTheFileAndDoctorsAfterTheStart(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(filepath.Dir(script), "..", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	skill := strings.ReplaceAll(string(data), "\r\n", "\n")
	if regexp.MustCompile("kind `nearhost`, run[^\n]*reload").MatchString(skill) {
		t.Error("the skill reloads for every fix of kind nearhost")
	}
	reload := regexp.MustCompile("(?m)^[0-9]\\. [^\n]*`nearhostrc.pending`[^\n]*`nearhostrc.not-applied`[^\n]*reload .")
	if !reload.MatchString(skill) {
		t.Error("the skill does not reload for nearhostrc.pending and nearhostrc.not-applied")
	}
	add := regexp.MustCompile("(?m)^6\\. On `\"state\":\"accepted\"`.*$").FindString(skill)
	status, doctor := strings.Index(add, "`<script> status`"), strings.Index(add, "`<script> doctor .`")
	if status < 0 || doctor < status || !strings.Contains(add, "status 5") {
		t.Errorf("after an add: %q", add)
	}
	if !regexp.MustCompile("while its `state` is `starting`").MatchString(add) ||
		!regexp.MustCompile(`\b30 seconds\b`).MatchString(add) ||
		!strings.Contains(add, "whatever") {
		t.Errorf("after an add: %q", add)
	}
}

// dash exits on a failed shift, so a flag without its value must be checked
// first: it is a usage error (1), which the skill does not read as "no
// Nearhost reachable" (2). Skipped where dash is absent.
func TestScriptFlagWithoutItsValueIsAUsageErrorInEveryShell(t *testing.T) {
	shells := scriptShells(t)
	if _, err := exec.LookPath("dash"); err == nil && runtime.GOOS != "windows" {
		shells = append(shells, scriptShell{"dash", []string{"dash"}, script})
	}
	home := shortDir(t)
	for _, shell := range shells {
		for flag, want := range map[string]string{"--web": "--web needs a port", "--name": "--name needs a name"} {
			r := runScriptIn(t, shell, home, nil, "propose", flag)
			if r.code != 1 || !strings.Contains(r.stderr, want) {
				t.Fatalf("%s %s: %+v", shell.name, flag, r)
			}
		}
	}
}

// Without curl the script says so, rather than that Nearhost is not running.
func TestScriptWithoutCurlSaysSo(t *testing.T) {
	shells := scriptShells(t)
	if _, err := exec.LookPath("dash"); err == nil && runtime.GOOS != "windows" {
		shells = append(shells, scriptShell{"dash", []string{"dash"}, script})
	}
	home := shortDir(t)
	for _, shell := range shells {
		for _, cmd := range []string{"socket", "status", "propose", "wait", "doctor", "reload"} {
			args := []string{cmd}
			if cmd == "wait" {
				args = append(args, "abc123")
			}
			r := runScriptIn(t, shell, home, []string{"PATH=/nonexistent"}, args...)
			if r.code != 2 || !strings.Contains(r.stderr, "curl is needed") || strings.Contains(r.stderr, "not running") {
				t.Fatalf("%s %s: %+v", shell.name, cmd, r)
			}
		}
	}
}

// passScript serves origin like scriptServerWith, with a fake Passer and
// Grants and the install id "lid".
func passScript(t *testing.T, home, origin string, review bool) (*Server, *fakePasser, *fakeGrants, *fakeReviewer) {
	t.Helper()
	p := &fakePasser{live: map[string]Passthrough{}, taken: map[int]bool{}}
	g := &fakeGrants{tokens: map[[2]string]string{}}
	s, _ := scriptServerWith(t, home, "k3f9q2", origin, review, Options{ID: "lid", Source: fakeSource{origin: mini}, Passer: p, Grants: g})
	var rv *fakeReviewer
	if review {
		rv = &fakeReviewer{}
		s.SetReviewer(rv)
	}
	return s, p, g, rv
}

// claudeEnv is the environment of a Claude Code session s1.
var claudeEnv = []string{"CLAUDECODE=1", "CLAUDE_CODE_SESSION_ID=s1"}

// tokenPath is where the skill keeps requester's token for install id lid.
func tokenPath(home, requester string) string {
	return filepath.Join(home, ".local", "state", "nearhost", "tokens", "lid", requester)
}

// storeToken writes a token the way the skill does.
func storeToken(t *testing.T, home, requester, tok string) {
	t.Helper()
	path := tokenPath(home, requester)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestScriptPassthroughOpen(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		_, p, g, _ := passScript(t, home, "mini", false)
		g.tokens[[2]string{"mini", "claude-code"}] = "tok-x"
		storeToken(t, home, "claude-code", "tok-x")
		r := shell.run(t, home, claudeEnv, "passthrough", "3000", "--label", "browser preview")
		if r.code != 0 || !strings.Contains(r.stdout, `"state":"open"`) || !strings.Contains(r.stdout, `"url":"http://localhost:3000/"`) {
			t.Fatalf("passthrough: %+v", r)
		}
		opens := p.opened()
		if len(opens) != 1 || opens[0].Requester != "claude-code" || opens[0].Session != "s1" || opens[0].Remote != 3000 || opens[0].Label != "browser preview" {
			t.Fatalf("opened %+v", opens)
		}
		marker := filepath.Join(home, ".local", "state", "nearhost", "sessions", "s1")
		data, err := os.ReadFile(marker)
		if err != nil || string(data) != filepath.Join(home, ".nearhost", "k3f9q2.sock")+"\n" {
			t.Fatalf("marker %q: %v", data, err)
		}
		testutil.WantPerm(t, marker, 0o600)
	})
}

// A label reaches Nearhost byte for byte, backslashes and quotes too, in
// every awk the script may meet: mawk on Debian and Ubuntu servers once
// read "\\\\" in gsub's replacement as one backslash.
func TestScriptPassthroughLabelEscapes(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		_, p, g, _ := passScript(t, home, "mini", false)
		g.tokens[[2]string{"mini", "claude-code"}] = "tok-x"
		storeToken(t, home, "claude-code", "tok-x")
		labels := []string{`a\b`, `a\"b`, `a\\b`, `"q"`, `end\`}
		for i, label := range labels {
			r := shell.run(t, home, claudeEnv, "passthrough", strconv.Itoa(3000+i), "--label", label)
			if r.code != 0 || !strings.Contains(r.stdout, `"state":"open"`) {
				t.Fatalf("label %q: %+v", label, r)
			}
			if opens := p.opened(); len(opens) != i+1 || opens[i].Label != label {
				t.Fatalf("label %q: opened %+v", label, opens)
			}
		}
	})
}

func TestScriptPassthroughPromptThenAlways(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		s, _, g, rv := passScript(t, home, "mini", true)
		// The skill's directories are made private even when they exist already.
		tokens := filepath.Join(home, ".local", "state", "nearhost", "tokens")
		if err := os.MkdirAll(tokens, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Dir(tokens), 0o755); err != nil {
			t.Fatal(err)
		}
		r := shell.run(t, home, claudeEnv, "passthrough", "3000")
		m := proposalID.FindStringSubmatch(r.stdout)
		if r.code != 0 || m == nil || !strings.Contains(r.stdout, `"expires_at"`) || strings.Contains(r.stdout, `"state"`) {
			t.Fatalf("passthrough: %+v", r)
		}
		id := m[1]
		if got := rv.prompts(); len(got) != 1 || got[0].ID != id || got[0].Requester != "claude-code" {
			t.Fatalf("prompts %+v", got)
		}
		if err := s.AnswerPassthrough(context.Background(), id, AnswerAlways); err != nil {
			t.Fatal(err)
		}
		r = shell.run(t, home, claudeEnv, "passthrough-wait", id)
		if r.code != 0 || !strings.Contains(r.stdout, `"state":"open"`) || strings.Contains(r.stdout+r.stderr, "tok-1") || !json.Valid([]byte(r.stdout)) {
			t.Fatalf("passthrough-wait: %+v", r)
		}
		tok, err := os.ReadFile(tokenPath(home, "claude-code"))
		if err != nil || strings.TrimSpace(string(tok)) != "tok-1" {
			t.Fatalf("token %q: %v", tok, err)
		}
		testutil.WantPerm(t, tokenPath(home, "claude-code"), 0o600)
		for _, dir := range []string{filepath.Dir(tokenPath(home, "claude-code")), tokens, filepath.Dir(tokens)} {
			testutil.WantPerm(t, dir, 0o700)
		}
		if _, err := os.Stat(filepath.Join(home, ".local", "state", "nearhost", "sessions", "s1")); err != nil {
			t.Fatalf("no marker: %v", err)
		}
		if got := g.allowed(); len(got) != 1 {
			t.Fatalf("allowed %v", got)
		}
		// The token now opens the next one at once, with no prompt.
		r = shell.run(t, home, claudeEnv, "passthrough", "3001")
		if r.code != 0 || !strings.Contains(r.stdout, `"state":"open"`) || len(rv.prompts()) != 1 {
			t.Fatalf("with the token: %+v", r)
		}
	})
}

func TestScriptPassthroughReplyToken(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		s, _, _, _ := passScript(t, home, "mini", true)
		r := shell.run(t, home, claudeEnv, "passthrough", "3000")
		id := proposalID.FindStringSubmatch(r.stdout)[1]
		if err := s.AnswerPassthrough(context.Background(), id, AnswerAlways); err != nil {
			t.Fatal(err)
		}
		// The repeat request is answered with the open record, token included.
		r = shell.run(t, home, claudeEnv, "passthrough", "3000")
		if r.code != 0 || !strings.Contains(r.stdout, `"state":"open"`) || strings.Contains(r.stdout+r.stderr, "tok-1") || strings.Contains(r.stdout, "token") {
			t.Fatalf("repeat: %+v", r)
		}
		if json.Valid([]byte(r.stdout)) == false {
			t.Fatalf("not JSON: %q", r.stdout)
		}
		if tok, err := os.ReadFile(tokenPath(home, "claude-code")); err != nil || strings.TrimSpace(string(tok)) != "tok-1" {
			t.Fatalf("token %q: %v", tok, err)
		}
	})
}

func TestScriptPassthroughDeclinedExpired(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		s, c := scriptServerWith(t, home, "k3f9q2", "mini", true, Options{ID: "lid", Source: fakeSource{"mini": mini},
			Passer: &fakePasser{live: map[string]Passthrough{}, taken: map[int]bool{}}, Grants: &fakeGrants{tokens: map[[2]string]string{}}})
		r := shell.run(t, home, claudeEnv, "passthrough", "3000")
		id := proposalID.FindStringSubmatch(r.stdout)[1]
		if err := s.AnswerPassthrough(context.Background(), id, AnswerNo); err != nil {
			t.Fatal(err)
		}
		r = shell.run(t, home, claudeEnv, "passthrough-wait", id)
		if r.code != 6 || !strings.Contains(r.stdout, `"state":"declined"`) || strings.Contains(r.stdout, `"reason"`) || r.stderr != "" {
			t.Fatalf("declined: %+v", r)
		}
		// A No holds back the next request for backoff.
		c.add(backoff)
		r = shell.run(t, home, claudeEnv, "passthrough", "3001")
		id = proposalID.FindStringSubmatch(r.stdout)[1]
		c.add(11 * time.Minute)
		r = shell.run(t, home, claudeEnv, "passthrough-wait", id)
		if r.code != 7 || !strings.Contains(r.stdout, `"state":"expired"`) {
			t.Fatalf("expired: %+v", r)
		}
	})
}

func TestScriptPassthroughOpenFailedIsNoDecline(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		s, p, _, _ := passScript(t, home, "mini", true)
		p.err = errors.New(`ssh: listen tcp 127.0.0.1:3000, "bind": address already in use`)
		r := shell.run(t, home, claudeEnv, "passthrough", "3000")
		id := proposalID.FindStringSubmatch(r.stdout)[1]
		if err := s.AnswerPassthrough(context.Background(), id, AnswerYes); err == nil {
			t.Fatal("the open did not fail")
		}
		r = shell.run(t, home, claudeEnv, "passthrough-wait", id)
		want := `Nearhost could not open the port: ssh: listen tcp 127.0.0.1:3000, "bind": address already in use`
		if r.code != 1 || !strings.Contains(r.stdout, `"state":"declined"`) || strings.TrimSpace(r.stderr) != want {
			t.Fatalf("failed open: %+v", r)
		}
	})
}

func TestScriptPassthroughTokenNotInArguments(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		_, _, g, _ := passScript(t, home, "mini", false)
		g.tokens[[2]string{"mini", "claude-code"}] = "tok-secret"
		storeToken(t, home, "claude-code", "tok-secret")
		if runtime.GOOS == "windows" {
			t.Skip("the curl that records its arguments is a sh script")
		}
		// A curl that records its arguments, as ps would show them.
		bin := filepath.Join(home, "bin")
		if err := os.Mkdir(bin, 0o700); err != nil {
			t.Fatal(err)
		}
		args := filepath.Join(home, "args")
		real, err := exec.LookPath("curl")
		if err != nil {
			t.Skip("no curl")
		}
		wrapper := "#!/bin/sh\nprintf '%s\\n' \"$*\" >>" + args + "\nexec " + real + " \"$@\"\n"
		if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(wrapper), 0o700); err != nil {
			t.Fatal(err)
		}
		r := shell.run(t, home, append([]string{"PATH=" + bin + ":/usr/bin:/bin"}, claudeEnv...), "passthrough", "3000")
		if r.code != 0 || !strings.Contains(r.stdout, `"state":"open"`) {
			t.Fatalf("passthrough: %+v", r)
		}
		seen, err := os.ReadFile(args)
		if err != nil || !strings.Contains(string(seen), "/v1/passthroughs") {
			t.Fatalf("curl was not called: %q %v", seen, err)
		}
		if strings.Contains(string(seen), "tok-secret") {
			t.Fatalf("the token is in curl's arguments:\n%s", seen)
		}
	})
}

func TestScriptPassthroughRevokedToken(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		_, _, _, rv := passScript(t, home, "mini", true)
		storeToken(t, home, "claude-code", "revoked")
		posts, front := countPosts(t, home)
		r := shell.run(t, home, append([]string{"NEARHOST_SOCKET=" + front}, claudeEnv...), "passthrough", "3000")
		if r.code != 0 || proposalID.FindStringSubmatch(r.stdout) == nil || strings.Contains(r.stdout, `"state"`) {
			t.Fatalf("passthrough: %+v", r)
		}
		if _, err := os.Stat(tokenPath(home, "claude-code")); !os.IsNotExist(err) {
			t.Fatalf("the revoked token stayed: %v", err)
		}
		if got := rv.prompts(); len(got) != 1 {
			t.Fatalf("prompts %+v", got)
		}
		if n := posts(); n != 2 {
			t.Fatalf("the server saw %d passthrough requests, want 2", n)
		}
	})
}

func TestScriptPassthroughAgent(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		s, p, g, _ := passScript(t, home, "mini", true)
		g.tokens[[2]string{"mini", "agent"}] = "tok-agent"
		storeToken(t, home, "agent", "tok-agent")
		r := shell.run(t, home, nil, "passthrough", "3000")
		m := proposalID.FindStringSubmatch(r.stdout)
		if r.code != 0 || m == nil || strings.Contains(r.stdout, `"state"`) {
			t.Fatalf("a token was sent: %+v", r)
		}
		if err := s.AnswerPassthrough(context.Background(), m[1], AnswerYes); err != nil {
			t.Fatal(err)
		}
		r = shell.run(t, home, nil, "passthrough-wait", m[1])
		if r.code != 0 || !strings.Contains(r.stdout, `"state":"open"`) {
			t.Fatalf("wait: %+v", r)
		}
		if got := p.opened(); len(got) != 1 || got[0].Requester != "agent" || got[0].Session != "" {
			t.Fatalf("opened %+v", got)
		}
		if tok, _ := os.ReadFile(tokenPath(home, "agent")); strings.TrimSpace(string(tok)) != "tok-agent" {
			t.Fatalf("the agent's token file changed: %q", tok)
		}
		if entries, _ := os.ReadDir(filepath.Join(home, ".local", "state", "nearhost", "sessions")); len(entries) != 0 {
			t.Fatalf("a marker without a session: %v", entries)
		}
	})
}

func TestScriptPassthroughNotConnected(t *testing.T) {
	home := shortDir(t)
	host, _ := os.Hostname()
	host, _, _ = strings.Cut(host, ".")
	r := runScript(t, home, []string{"SSH_CONNECTION=1.2.3.4 5 6.7.8.9 22"}, "passthrough", "3000")
	want := "Nearhost isn't connected to this server. On your laptop, select " + host + " in Nearhost and press c, then ask again. Or forward it yourself: ssh -N -L 3000:localhost:3000 " + host + "\n"
	if r.code != 2 || r.stderr != want {
		t.Fatalf("%+v, want %q", r, want)
	}
}

func TestScriptPassthroughOldNearhost(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		scriptServer(t, home, "k3f9q2", "mini", true)
		host, _ := os.Hostname()
		host, _, _ = strings.Cut(host, ".")
		r := shell.run(t, home, nil, "passthrough", "3000")
		want := "Nearhost on dev@k3f9q2 is too old for passthrough; update it there. Meanwhile, forward it yourself: ssh -N -L 3000:localhost:3000 " + host + "\n"
		if r.code != 5 || r.stderr != want {
			t.Fatalf("%+v, want %q", r, want)
		}
	})
}

func TestScriptPassthroughCloseSession(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		_, p, g, _ := passScript(t, home, "mini", false)
		g.tokens[[2]string{"mini", "claude-code"}] = "tok-x"
		storeToken(t, home, "claude-code", "tok-x")
		r := shell.run(t, home, claudeEnv, "passthrough", "3000")
		if r.code != 0 {
			t.Fatalf("passthrough: %+v", r)
		}
		marker := filepath.Join(home, ".local", "state", "nearhost", "sessions", "s1")
		r = shell.run(t, home, nil, "passthrough-close", "--session", "s1")
		if r.code != 0 || len(p.closed()) != 1 {
			t.Fatalf("close: %+v, closed %v", r, p.closed())
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("marker stayed: %v", err)
		}

		// With no marker it neither scans ~/.nearhost nor waits.
		dead, err := net.Listen("unix", filepath.Join(home, ".nearhost", "dead.sock"))
		if err != nil {
			t.Fatal(err)
		}
		defer dead.Close()
		var mu sync.Mutex
		dials := 0
		go func() {
			for {
				c, err := dead.Accept()
				if err != nil {
					return
				}
				mu.Lock()
				dials++
				mu.Unlock()
				c.Close()
			}
		}()
		// PowerShell takes most of a second to start, more on Windows, but
		// still well inside the hook's 3 seconds.
		limit := 1500 * time.Millisecond
		if shell.powerShell() {
			limit = 2500 * time.Millisecond
		}
		start := time.Now()
		r = shell.run(t, home, nil, "passthrough-close", "--session", "none")
		mu.Lock()
		defer mu.Unlock()
		if r.code != 0 || time.Since(start) > limit || dials != 0 || len(p.closed()) != 1 {
			t.Fatalf("no marker: %+v after %v, %d dials", r, time.Since(start), dials)
		}
	})
}

func TestScriptPassthroughCloseByID(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		s, p, _, _ := passScript(t, home, "mini", true)
		r := shell.run(t, home, nil, "passthrough", "3000")
		id := proposalID.FindStringSubmatch(r.stdout)[1]
		if err := s.AnswerPassthrough(context.Background(), id, AnswerYes); err != nil {
			t.Fatal(err)
		}
		r = shell.run(t, home, nil, "passthrough-close", id)
		if r.code != 0 || len(p.closed()) != 1 || p.closed()[0] != id {
			t.Fatalf("close: %+v, closed %v", r, p.closed())
		}
		// An id nothing knows still exits 0.
		if r = shell.run(t, home, nil, "passthrough-close", "ffff"); r.code != 0 {
			t.Fatalf("unknown id: %+v", r)
		}
	})
}

func TestScriptPassthroughXDGStateHome(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		state := filepath.Join(shortDir(t), "xdg")
		_, _, g, _ := passScript(t, home, "mini", false)
		g.tokens[[2]string{"mini", "claude-code"}] = "tok-x"
		path := filepath.Join(state, "nearhost", "tokens", "lid", "claude-code")
		os.MkdirAll(filepath.Dir(path), 0o700)
		os.WriteFile(path, []byte("tok-x\n"), 0o600)
		env := append([]string{"XDG_STATE_HOME=" + state}, claudeEnv...)
		r := shell.run(t, home, env, "passthrough", "3000")
		if r.code != 0 || !strings.Contains(r.stdout, `"state":"open"`) {
			t.Fatalf("passthrough: %+v", r)
		}
		marker := filepath.Join(state, "nearhost", "sessions", "s1")
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("no marker under XDG_STATE_HOME: %v", err)
		}
		if _, err := os.Stat(filepath.Join(home, ".local")); err == nil {
			t.Fatal("wrote under ~/.local")
		}
		r = shell.run(t, home, env, "passthrough-close", "--session", "s1")
		if r.code != 0 {
			t.Fatalf("close: %+v", r)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("marker stayed: %v", err)
		}
	})
}

// countPosts proxies home's socket k3f9q2 through a second socket the
// script finds instead, and returns a func counting the POSTs to
// the given path that went through it, /v1/passthroughs by default.
func countPosts(t *testing.T, home string, path ...string) (posts func() int, front string) {
	t.Helper()
	counted := "/v1/passthroughs"
	if len(path) > 0 {
		counted = path[0]
	}
	return countCalls(t, home, "POST", counted)
}

// countCalls is countPosts for requests of any method to counted.
func countCalls(t *testing.T, home, method, counted string) (calls func() int, front string) {
	t.Helper()
	real := filepath.Join(home, ".nearhost", "k3f9q2.sock")
	front = filepath.Join(home, "front.sock")
	ln, err := net.Listen("unix", front)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	n := 0
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == method && r.URL.Path == counted {
			mu.Lock()
			n++
			mu.Unlock()
		}
		r.RequestURI = ""
		r.URL.Scheme, r.URL.Host = "http", "nearhost"
		resp, err := client(real).Do(r)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return n
	}, front
}

func TestScriptPassthroughPendingMarker(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		s, _, _, _ := passScript(t, home, "mini", true)
		r := shell.run(t, home, claudeEnv, "passthrough", "3000")
		id := proposalID.FindStringSubmatch(r.stdout)[1]
		marker := filepath.Join(home, ".local", "state", "nearhost", "sessions", "s1")
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("no marker while pending: %v", err)
		}
		if r = shell.run(t, home, nil, "passthrough-close", "--session", "s1"); r.code != 0 {
			t.Fatalf("close: %+v", r)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("marker stayed: %v", err)
		}
		if err := s.AnswerPassthrough(context.Background(), id, AnswerYes); err == nil {
			t.Fatal("the request was still pending after its session closed")
		}
	})
}

func TestScriptPassthroughNotConnectedAnswer(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		_, p, g, _ := passScript(t, home, "mini", false)
		p.err = fmt.Errorf("dial: %w", ErrNotConnected)
		g.tokens[[2]string{"mini", "claude-code"}] = "tok-x"
		storeToken(t, home, "claude-code", "tok-x")
		host, _ := os.Hostname()
		host, _, _ = strings.Cut(host, ".")
		r := shell.run(t, home, claudeEnv, "passthrough", "3000")
		want := "Nearhost isn't connected to this server. On your laptop, select " + host + " in Nearhost and press c, then ask again. Or forward it yourself: ssh -N -L 3000:localhost:3000 " + host + "\n"
		if r.code != 2 || r.stderr != want {
			t.Fatalf("%+v, want %q", r, want)
		}
	})
}

func TestScriptPassthroughBadPort(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		passScript(t, home, "mini", true)
		for _, port := range []string{"03000", "0", "70000", "abc"} {
			if r := shell.run(t, home, claudeEnv, "passthrough", port); r.code != 1 || r.stdout != "" {
				t.Errorf("port %s: %+v", port, r)
			}
		}
	})
}

// fakeSSH puts an ssh in home/bin, which drops its options, -- and the host,
// then runs the rest with sh -c under SSH_CONNECTION=fake, stdin passed
// through, as ssh runs a command on a server. It leaves home/ssh-ran when it
// runs, and the command it was given in home/ssh-cmd. It returns the PATH to
// run shell's script with.
func fakeSSH(t *testing.T, shell scriptShell, home string) string {
	t.Helper()
	return fakeSSHWith(t, shell, home, `while [ $# -gt 0 ]; do
	case $1 in
	-o) shift 2 ;;
	--) shift; break ;;
	-*) shift ;;
	*) break ;;
	esac
done
shift
printf '%s\n' "$@" >'`+filepath.Join(home, "ssh-cmd")+`'
SSH_CONNECTION=fake exec sh -c "$*"
`)
}

// fakeSSHWith is fakeSSH with body as what the ssh does after noting it ran.
func fakeSSHWith(t *testing.T, shell scriptShell, home, body string) string {
	t.Helper()
	return fakeSSHIn(t, shell, home, filepath.Join(home, "bin"), body)
}

// fakeSSHIn is fakeSSHWith with the ssh in dir. On Windows, where
// nearhost.ps1 runs ssh.exe, it is dir\ssh.cmd, which runs body with Git for
// Windows' sh, as the server's sh; with no sh, or one that cannot reach the
// test's Nearhost when it has one, the test skips.
func fakeSSHIn(t *testing.T, shell scriptShell, home, dir, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := ": >'" + filepath.Join(home, "ssh-ran") + "'\n" + body
	if runtime.GOOS != "windows" {
		if err := os.WriteFile(fakeSSHPath(dir), []byte("#!/bin/sh\n"+src), 0o755); err != nil {
			t.Fatal(err)
		}
		return "PATH=" + dir + ":/usr/bin:/bin"
	}
	sh, dirs := gitSh(t, home)
	script := filepath.Join(dir, "ssh.sh")
	if err := os.WriteFile(script, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := "@set \"PATH=" + dirs + ";%PATH%\"\r\n@\"" + sh + "\" \"" + script + "\" %*\r\n@exit %ERRORLEVEL%\r\n"
	if err := os.WriteFile(fakeSSHPath(dir), []byte(cmd), 0o755); err != nil {
		t.Fatal(err)
	}
	path := ""
	for _, kv := range scriptEnv(t, shell, home) {
		if k, v, _ := strings.Cut(kv, "="); strings.EqualFold(k, "PATH") {
			path = v
		}
	}
	return "PATH=" + dir + ";" + path
}

// fakeSSHPath is the fake ssh fakeSSHIn puts in dir, as Get-Command ssh
// finds it.
func fakeSSHPath(dir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, "ssh.cmd")
	}
	return filepath.Join(dir, "ssh")
}

// gitSh finds Git for Windows' sh, before the scripts' PATH drops it, and
// the folders it needs on PATH, for the fake ssh to run nearhost.sh with as
// the server would. When home has a Nearhost socket, that sh must reach it
// as nearhost.sh does, with [ -S ] and curl --unix-socket; otherwise, or with
// no sh, the test skips rather than fake the server.
func gitSh(t *testing.T, home string) (sh, dirs string) {
	t.Helper()
	sh = needSh(t, "to run nearhost.sh on the fake ssh's server")
	list := []string{filepath.Dir(sh)}
	if usr := filepath.Join(filepath.Dir(sh), "..", "usr", "bin"); dirExists(usr) {
		list = append(list, filepath.Clean(usr))
	}
	dirs = strings.Join(list, ";")
	socks, _ := filepath.Glob(filepath.Join(home, ".nearhost", "*.sock"))
	if len(socks) > 0 {
		probe := exec.Command(sh, "-c", `[ -S "$1" ] && curl -s --max-time 2 --unix-socket "$1" http://nearhost/v1 >/dev/null`, "sh", socks[0])
		probe.Env = append(os.Environ(), "PATH="+dirs+";"+os.Getenv("PATH"))
		if out, err := probe.CombinedOutput(); err != nil {
			t.Skipf("Git for Windows' sh cannot reach a Windows socket as nearhost.sh does ([ -S ] and curl --unix-socket: %v %s), so it cannot play the server", err, out)
		}
	}
	return sh, dirs
}

// needSh finds sh, Git for Windows' on Windows, which the test needs for
// why. With none it skips, but in CI, where Windows runners have Git for
// Windows, it fails: those cases must not pass by not running.
func needSh(t *testing.T, why string) string {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("no sh (Git for Windows) here %s", why)
		}
		t.Skipf("no sh (Git for Windows) here %s", why)
	}
	return sh
}

// dirExists reports whether dir is a directory.
func dirExists(dir string) bool {
	fi, err := os.Stat(dir)
	return err == nil && fi.IsDir()
}

func TestScriptPassthroughOnAnotherHost(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		_, p, g, _ := passScript(t, home, "mini", false)
		g.tokens[[2]string{"mini", "claude-code"}] = "tok-x"
		storeToken(t, home, "claude-code", "tok-x")
		env := append([]string{fakeSSH(t, shell, home)}, claudeEnv...)
		// Labels come from text the agent read: none may reach a shell on
		// the host as anything but its bytes, whatever that shell is.
		labels := []string{
			`it's "x" $(id) ` + "`id` \\ ;",
			`a\b`,
			`a\"b`,
			`x\' ; echo INJECTED #`,
		}
		for i, label := range labels {
			r := shell.run(t, home, env, "passthrough", strconv.Itoa(3000+i), "--on", "apollo", "--label", label)
			if r.code != 0 || !strings.Contains(r.stdout, `"state":"open"`) || strings.Contains(r.stdout+r.stderr, "INJECTED\n") {
				t.Fatalf("passthrough --on %q: %+v", label, r)
			}
			opens := p.opened()
			if len(opens) != i+1 || opens[i].Requester != "claude-code" || opens[i].Session != "s1" || opens[i].Remote != 3000+i || opens[i].Label != label {
				t.Fatalf("label %q: opened %+v", label, opens)
			}
			// The command ssh runs on the host is a constant: the login
			// shell there, POSIX or not, sees no argument.
			if cmd, err := os.ReadFile(filepath.Join(home, "ssh-cmd")); err != nil || string(cmd) != "sh -s\n" {
				t.Fatalf("ssh ran %q: %v", cmd, err)
			}
		}
	})
}

func TestScriptPassthroughWaitOnAnotherHost(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		s, _, _, rv := passScript(t, home, "mini", true)
		env := append([]string{fakeSSH(t, shell, home)}, claudeEnv...)
		r := shell.run(t, home, env, "passthrough", "3000", "--on", "apollo")
		m := proposalID.FindStringSubmatch(r.stdout)
		if r.code != 0 || m == nil {
			t.Fatalf("passthrough --on: %+v", r)
		}
		if got := rv.prompts(); len(got) != 1 || got[0].Requester != "claude-code" {
			t.Fatalf("prompts %+v", got)
		}
		// A request the user is asked about is in the marker too, so the
		// session's end withdraws it.
		marker := filepath.Join(home, ".local", "state", "nearhost", "sessions", "s1")
		want := "ssh " + fakeSSHPath(filepath.Join(home, "bin")) + " apollo\n"
		if data, err := os.ReadFile(marker); err != nil || string(data) != want {
			t.Fatalf("marker %q, want %q: %v", data, want, err)
		}
		if err := s.AnswerPassthrough(context.Background(), m[1], AnswerYes); err != nil {
			t.Fatal(err)
		}
		r = shell.run(t, home, env, "passthrough-wait", m[1], "--on", "apollo")
		if r.code != 0 || !strings.Contains(r.stdout, `"state":"open"`) || !json.Valid([]byte(r.stdout)) {
			t.Fatalf("passthrough-wait --on: %+v", r)
		}
	})
}

func TestScriptOnRefusesAnOptionLikeHost(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		passScript(t, home, "mini", false)
		env := append([]string{fakeSSH(t, shell, home)}, claudeEnv...)
		for _, host := range []string{"-oProxyCommand=x", "a b", "", "a;b", "a'b"} {
			for _, args := range [][]string{{"passthrough", "3000", "--on", host}, {"passthrough-wait", "abc123", "--on", host}} {
				r := shell.run(t, home, env, args...)
				if r.code != 1 || r.stdout != "" {
					t.Errorf("%q: %+v", args, r)
				}
				if _, err := os.Stat(filepath.Join(home, "ssh-ran")); !os.IsNotExist(err) {
					t.Fatalf("%q ran ssh: %v", args, err)
				}
			}
		}
	})
}

func TestScriptOnSaysWhenSSHFails(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		path := fakeSSHWith(t, shell, home, "echo 'ssh: Could not resolve hostname apollo' >&2\necho 'second line' >&2\nexit 255\n")
		r := shell.run(t, home, append([]string{path}, claudeEnv...), "passthrough", "3000", "--on", "apollo")
		want := "SSH could not reach apollo: ssh: Could not resolve hostname apollo\n"
		if r.code != 2 || r.stderr != want || r.stdout != "" {
			t.Fatalf("%+v, want %q", r, want)
		}
	})
}

func TestScriptOnPassesTheRemoteAnswerThrough(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		path := fakeSSHWith(t, shell, home, "echo '{\"error\":\"x\"}'\necho 'No Nearhost reaches this server.' >&2\nexit 2\n")
		r := shell.run(t, home, append([]string{path}, claudeEnv...), "passthrough", "3000", "--on", "apollo")
		if r.code != 2 || r.stdout != "{\"error\":\"x\"}\n" || r.stderr != "No Nearhost reaches this server.\n" {
			t.Fatalf("%+v", r)
		}
	})
}

func TestScriptRefusesRemoteFromATerminal(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		passScript(t, home, "mini", false)
		r := shell.run(t, home, nil, "--remote", "x", "y", "status")
		if r.code != 1 || r.stdout != "" {
			t.Fatalf("--remote without SSH_CONNECTION: %+v", r)
		}
		// Over SSH, it still takes only the requesters the script sends.
		r = shell.run(t, home, []string{"SSH_CONNECTION=fake"}, "--remote", "../x", "s1", "passthrough", "3000")
		if r.code != 1 || r.stdout != "" {
			t.Fatalf("--remote with a bad requester: %+v", r)
		}
	})
}

// fakeSSHFailing is fakeSSH, but for the host fail, which it cannot reach:
// it says so and exits 255, as ssh does.
func fakeSSHFailing(t *testing.T, shell scriptShell, home, fail string) string {
	t.Helper()
	return fakeSSHWith(t, shell, home, `while [ $# -gt 0 ]; do
	case $1 in
	-o) shift 2 ;;
	--) shift; break ;;
	-*) shift ;;
	*) break ;;
	esac
done
if [ "$1" = '`+fail+`' ]; then
	echo "ssh: connect to host $1 port 22: No route to host" >&2
	exit 255
fi
shift
SSH_CONNECTION=fake exec sh -c "$*"
`)
}

// sessionTargets opens three passthroughs for session s1: one here and one
// on each of apollo and leto, all on the same fake Nearhost, reached through
// a front socket that counts the DELETEs. It returns the passer, the count,
// the environment that reaches the front socket with the fake ssh, the
// marker's path and the lines it should hold.
func sessionTargets(t *testing.T, shell scriptShell, home string) (*fakePasser, func() int, []string, string, string) {
	t.Helper()
	_, p, g, _ := passScript(t, home, "mini", false)
	g.tokens[[2]string{"mini", "claude-code"}] = "tok-x"
	storeToken(t, home, "claude-code", "tok-x")
	deletes, front := countCalls(t, home, http.MethodDelete, "/v1/passthroughs")
	env := []string{fakeSSH(t, shell, home), "NEARHOST_SOCKET=" + front}
	agent := append(append([]string{}, env...), claudeEnv...)
	for i, on := range []string{"", "apollo", "leto"} {
		args := []string{"passthrough", strconv.Itoa(3000 + i)}
		if on != "" {
			args = append(args, "--on", on)
		}
		if r := shell.run(t, home, agent, args...); r.code != 0 || !strings.Contains(r.stdout, `"state":"open"`) {
			t.Fatalf("%q: %+v", args, r)
		}
	}
	ssh := fakeSSHPath(filepath.Join(home, "bin"))
	want := front + "\nssh " + ssh + " apollo\nssh " + ssh + " leto\n"
	return p, deletes, env, filepath.Join(home, ".local", "state", "nearhost", "sessions", "s1"), want
}

func TestScriptSessionMarkerKeepsEveryTarget(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		_, _, env, marker, want := sessionTargets(t, shell, home)
		if data, err := os.ReadFile(marker); err != nil || string(data) != want {
			t.Fatalf("marker %q, want %q: %v", data, want, err)
		}
		testutil.WantPerm(t, marker, 0o600)
		// Opening another one where the session has one already adds
		// nothing, here or on another host.
		agent := append(append([]string{}, env...), claudeEnv...)
		for _, args := range [][]string{{"passthrough", "3010", "--on", "apollo"}, {"passthrough", "3011"}} {
			if r := shell.run(t, home, agent, args...); r.code != 0 {
				t.Fatalf("%q: %+v", args, r)
			}
		}
		if data, err := os.ReadFile(marker); err != nil || string(data) != want {
			t.Fatalf("marker after repeats %q, want %q: %v", data, want, err)
		}
	})
}

func TestScriptCloseSessionClosesEveryTarget(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		p, deletes, env, marker, _ := sessionTargets(t, shell, home)
		r := shell.run(t, home, append(env, "CLAUDE_CODE_SESSION_ID=s1"), "passthrough-close", "--session", "s1")
		if r.code != 0 || r.stdout != "" || r.stderr != "" {
			t.Fatalf("close: %+v", r)
		}
		if n := deletes(); n != 3 {
			t.Fatalf("%d DELETEs, want 3", n)
		}
		// Every target here is the one Nearhost, which the closes reach at
		// once, so one may close what another just closed too.
		seen := map[string]bool{}
		for _, id := range p.closed() {
			seen[id] = true
		}
		if len(seen) != 3 {
			t.Fatalf("closed %v", p.closed())
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("marker stayed: %v", err)
		}
		if cmd, err := os.ReadFile(filepath.Join(home, "ssh-cmd")); err != nil || string(cmd) != "sh -s\n" {
			t.Fatalf("ssh ran %q: %v", cmd, err)
		}
	})
}

func TestScriptCloseSessionKeepsWhatFailed(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		_, deletes, env, marker, _ := sessionTargets(t, shell, home)
		env[0] = fakeSSHFailing(t, shell, home, "leto")
		r := shell.run(t, home, env, "passthrough-close", "--session", "s1")
		if r.code != 0 || r.stdout != "" || r.stderr != "" {
			t.Fatalf("close: %+v", r)
		}
		if n := deletes(); n != 2 {
			t.Fatalf("%d DELETEs, want 2", n)
		}
		want := "ssh " + fakeSSHPath(filepath.Join(home, "bin")) + " leto\n"
		if data, err := os.ReadFile(marker); err != nil || string(data) != want {
			t.Fatalf("marker %q, want %q: %v", data, want, err)
		}
	})
}

func TestScriptCloseSessionIsCutOff(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		if runtime.GOOS == "windows" {
			// The fake is ssh.cmd running sh: ending cmd.exe at the cut-off
			// leaves its sh running, which a real ssh.exe does not.
			t.Skip("the fake ssh on Windows cannot be cut off as ssh.exe is")
		}
		home := shortDir(t)
		pidFile := filepath.Join(home, "ssh-pid")
		path := fakeSSHWith(t, shell, home, "echo $$ >'"+pidFile+"'\nexec sleep 10\n")
		t.Cleanup(func() {
			if b, err := os.ReadFile(pidFile); err == nil {
				if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
					if proc, err := os.FindProcess(pid); err == nil {
						proc.Kill()
					}
				}
			}
		})
		line := "ssh " + fakeSSHPath(filepath.Join(home, "bin")) + " apollo"
		gone := filepath.Join(home, "gone.sock")
		marker := writeMarker(t, home, "s1", gone+"\n"+line)
		start := time.Now()
		r := shell.run(t, home, []string{path}, "passthrough-close", "--session", "s1")
		took := time.Since(start)
		// The SessionEnd hook runs nearhost.sh, within its 3 seconds;
		// PowerShell takes up to a second more to start.
		limit := 3 * time.Second
		if shell.powerShell() {
			limit += 2 * time.Second
		}
		if r.code != 0 || took > limit {
			t.Fatalf("close: %+v after %v", r, took)
		}
		if took < 2*time.Second {
			t.Fatalf("close returned after %v, before the ssh was cut off", took)
		}
		// The ssh that was cut off stays to be tried again; the socket that
		// is gone does not.
		if data, err := os.ReadFile(marker); err != nil || string(data) != line+"\n" {
			t.Fatalf("marker %q: %v", data, err)
		}
		// The ssh that was cut off was ended, not left running.
		b, err := os.ReadFile(pidFile)
		if err != nil {
			t.Fatal(err)
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		deadline := time.Now().Add(time.Second)
		for {
			if err := exec.Command("kill", "-0", strconv.Itoa(pid)).Run(); err != nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the ssh %d still runs", pid)
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
}

func TestScriptCloseSessionDropsMalformedLines(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		path := fakeSSH(t, shell, home)
		bad := []string{
			"ssh relative/ssh apollo",
			"ssh " + fakeSSHPath(filepath.Join(home, "bin")) + " -oProxyCommand=x",
			"ssh " + fakeSSHPath(filepath.Join(home, "bin")),
			"relative.sock",
			"",
		}
		marker := writeMarker(t, home, "s1", strings.Join(bad, "\n"))
		r := shell.run(t, home, []string{path}, "passthrough-close", "--session", "s1")
		if r.code != 0 || r.stdout != "" || r.stderr != "" {
			t.Fatalf("close: %+v", r)
		}
		if _, err := os.Stat(filepath.Join(home, "ssh-ran")); !os.IsNotExist(err) {
			t.Fatalf("ran ssh for a malformed line: %v", err)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("marker stayed: %v", err)
		}
	})
}

func TestScriptCloseOneOnAnotherHost(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		_, p, g, _ := passScript(t, home, "mini", false)
		g.tokens[[2]string{"mini", "claude-code"}] = "tok-x"
		storeToken(t, home, "claude-code", "tok-x")
		env := append([]string{fakeSSH(t, shell, home)}, claudeEnv...)
		r := shell.run(t, home, env, "passthrough", "3000", "--on", "apollo")
		m := proposalID.FindStringSubmatch(r.stdout)
		if r.code != 0 || m == nil {
			t.Fatalf("passthrough --on: %+v", r)
		}
		os.Remove(filepath.Join(home, "ssh-cmd"))
		r = shell.run(t, home, env, "passthrough-close", m[1], "--on", "apollo")
		if r.code != 0 || len(p.closed()) != 1 || p.closed()[0] != m[1] {
			t.Fatalf("close --on: %+v, closed %v", r, p.closed())
		}
		if cmd, err := os.ReadFile(filepath.Join(home, "ssh-cmd")); err != nil || string(cmd) != "sh -s\n" {
			t.Fatalf("ssh ran %q: %v", cmd, err)
		}
	})
}

// nearhost.ps1 names in the marker the ssh it ran, by the full path
// Get-Command ssh gives, so the SessionEnd hook's nearhost.sh, in Git Bash,
// closes with the same ssh.exe, keys and known hosts.
func TestScriptPs1MarkerNamesItsSSH(t *testing.T) {
	for _, shell := range powerShells(t) {
		t.Run(shell.name, func(t *testing.T) {
			home := shortDir(t)
			// ssh.exe may be under C:\Program Files. On Windows the fake is a
			// .cmd, which cmd.exe runs, and its quoting rules are not ssh.exe's,
			// so that one has no space.
			dir := filepath.Join(home, "Open SSH")
			if runtime.GOOS == "windows" {
				dir = filepath.Join(home, "OpenSSH")
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			// It answers as a passthrough the user is asked about, without
			// reading what it is sent, as an ssh that fails early does not.
			src, sep := "#!/bin/sh\necho '{\"id\":\"ab12\"}'\n", ":"
			if runtime.GOOS == "windows" {
				src, sep = "@echo {\"id\":\"ab12\"}\r\n", ";"
			}
			if err := os.WriteFile(fakeSSHPath(dir), []byte(src), 0o755); err != nil {
				t.Fatal(err)
			}
			path := "PATH=" + dir + sep + "/usr/bin" + sep + "/bin"
			if runtime.GOOS == "windows" {
				for _, kv := range scriptEnv(t, shell, home) {
					if k, v, _ := strings.Cut(kv, "="); strings.EqualFold(k, "PATH") {
						path = "PATH=" + dir + ";" + v
					}
				}
			}
			probe := exec.Command(shell.argv[0], "-NoProfile", "-Command", "(Get-Command ssh -CommandType Application | Select-Object -First 1).Path")
			probe.Env = append(scriptEnv(t, shell, home), path)
			out, err := probe.Output()
			if err != nil {
				t.Fatal(err)
			}
			got := strings.TrimSpace(string(out))
			if got != fakeSSHPath(dir) {
				t.Fatalf("Get-Command ssh gave %q, want %q", got, fakeSSHPath(dir))
			}
			r := shell.run(t, home, append([]string{path}, claudeEnv...), "passthrough", "3000", "--on", "apollo")
			if r.code != 0 || !strings.Contains(r.stdout, `"id":"ab12"`) {
				t.Fatalf("passthrough --on: %+v", r)
			}
			marker := filepath.Join(home, ".local", "state", "nearhost", "sessions", "s1")
			if data, err := os.ReadFile(marker); err != nil || string(data) != "ssh "+got+" apollo\n" {
				t.Fatalf("marker %q, want %q: %v", data, "ssh "+got+" apollo\n", err)
			}
		})
	}
}

// A marker's ssh line names the ssh by its full path, which may hold
// spaces, as C:\Program Files does: the host is its last field.
func TestScriptCloseSessionTakesAnSSHPathWithSpaces(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		if runtime.GOOS == "windows" {
			t.Skip("the fake ssh on Windows is a .cmd, which cmd.exe cannot run from a path with a space as it runs ssh.exe")
		}
		home := shortDir(t)
		dir := filepath.Join(home, "Program Files", "Open SSH")
		args := filepath.Join(home, "ssh-args")
		path := fakeSSHIn(t, shell, home, dir, "printf '%s\\n' \"$@\" >'"+args+"'\ncat >/dev/null\n")
		marker := writeMarker(t, home, "s1", "ssh "+fakeSSHPath(dir)+" apollo")
		r := shell.run(t, home, []string{path}, "passthrough-close", "--session", "s1")
		if r.code != 0 || r.stdout != "" || r.stderr != "" {
			t.Fatalf("close: %+v", r)
		}
		data, err := os.ReadFile(args)
		if err != nil {
			t.Fatalf("the ssh at a path with spaces did not run: %v", err)
		}
		if want := "-o\nBatchMode=yes\n-o\nConnectTimeout=2\n--\napollo\nsh -s\n"; string(data) != want {
			t.Fatalf("ssh ran with %q, want %q", data, want)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("marker stayed after the close: %v", err)
		}
	})
}

// On Windows the SessionEnd hook runs nearhost.sh in Git Bash, and the
// marker names the ssh nearhost.ps1 ran as Get-Command gave it: a drive
// letter, backslashes and perhaps a space, as in C:\Program Files. Git Bash
// searches PATH for a command name without a slash, so the close must run
// that ssh by a path it reads as one, still as a single word.
func TestScriptCloseSessionRunsADriveLetterSSH(t *testing.T) {
	home := shortDir(t)
	shell := shScript
	var line, dir string
	env := []string{}
	if runtime.GOOS == "windows" {
		sh := needSh(t, ", which runs the SessionEnd hook")
		shell = scriptShell{"git-sh", []string{sh}, script}
		_, dirs := gitSh(t, home)
		dir = filepath.Join(home, "Open SSH")
		ssh := buildFakeSSHExe(t, dir)
		line = "ssh " + ssh + " apollo"
		if !regexp.MustCompile(`^ssh [A-Za-z]:\\.* .*\\ssh\.exe apollo$`).MatchString(line) {
			t.Fatalf("line %q does not name a drive-letter ssh with a space", line)
		}
		for _, kv := range scriptEnv(t, shell, home) {
			if k, v, _ := strings.Cut(kv, "="); strings.EqualFold(k, "PATH") {
				env = append(env, "PATH="+dirs+";"+v)
			}
		}
	} else {
		// Elsewhere no path starts with a drive letter, but a relative one,
		// from home, where the script runs, can look like it.
		dir = filepath.Join(home, "C:", "fake ssh")
		buildFakeSSHExe(t, dir)
		line = `ssh C:\fake ssh\ssh.exe apollo`
	}
	marker := writeMarker(t, home, "s1", line)
	r := shell.run(t, home, env, "passthrough-close", "--session", "s1")
	if r.code != 0 || r.stdout != "" || r.stderr != "" {
		t.Fatalf("close: %+v", r)
	}
	args, err := os.ReadFile(filepath.Join(dir, "ssh-args"))
	if err != nil {
		t.Fatalf("the ssh at %q did not run: %v", line, err)
	}
	if want := "-o\nBatchMode=yes\n-o\nConnectTimeout=2\n--\napollo\nsh -s\n"; string(args) != want {
		t.Fatalf("ssh ran with %q, want %q", args, want)
	}
	stdin, err := os.ReadFile(filepath.Join(dir, "ssh-stdin"))
	first, _, _ := strings.Cut(string(stdin), "\n")
	if err != nil || !strings.HasPrefix(first, "set -- --remote ") || !strings.HasSuffix(first, " 'passthrough-close' '--session' 's1'") {
		t.Fatalf("ssh was sent %.200q: %v", stdin, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker stayed after the close: %v", err)
	}
}

// buildFakeSSHExe builds dir/ssh.exe, a program that answers as an ssh
// that ran the close: it notes its arguments in dir/ssh-args and what it was
// sent in dir/ssh-stdin, and exits 0. It returns its path.
func buildFakeSSHExe(t *testing.T, dir string) string {
	t.Helper()
	src := t.TempDir()
	files := map[string]string{
		"go.mod": "module fakessh\n\ngo 1.21\n",
		"main.go": `package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	exe, err := os.Executable()
	if err != nil {
		os.Exit(3)
	}
	in, _ := io.ReadAll(os.Stdin)
	dir := filepath.Dir(exe)
	os.WriteFile(filepath.Join(dir, "ssh-stdin"), in, 0o644)
	os.WriteFile(filepath.Join(dir, "ssh-args"), []byte(strings.Join(os.Args[1:], "\n")+"\n"), 0o644)
}
`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(src, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "ssh.exe")
	build := exec.Command("go", "build", "-o", exe, ".")
	build.Dir = src
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return exe
}

// writeMarker writes a session marker for session sid naming sock, the way
// the skill does, and returns its path.
func writeMarker(t *testing.T, home, sid, sock string) string {
	t.Helper()
	marker := filepath.Join(home, ".local", "state", "nearhost", "sessions", sid)
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte(sock+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return marker
}

// answerDelete serves every request on a socket in home with status and
// returns the socket's path and a count of the DELETE requests served.
func answerDelete(t *testing.T, home string, status int) (string, *atomic.Int32) {
	t.Helper()
	return answerDeleteAt(t, filepath.Join(home, "d.sock"), status)
}

// answerDeleteAt is answerDelete on the socket sock.
func answerDeleteAt(t *testing.T, sock string, status int) (string, *atomic.Int32) {
	t.Helper()
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	deletes := new(atomic.Int32)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes.Add(1)
		}
		w.WriteHeader(status)
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return sock, deletes
}

func TestScriptCloseKeepsMarkerOnFailure(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		if _, err := exec.LookPath("curl"); err != nil {
			t.Skip("curl is not installed")
		}
		home := shortDir(t)
		sock, deletes := answerDelete(t, home, http.StatusBadGateway)
		marker := writeMarker(t, home, "s1", sock)
		r := shell.run(t, home, nil, "passthrough-close", "--session", "s1")
		if r.code != 0 {
			t.Fatalf("close: %+v", r)
		}
		if n := deletes.Load(); n != 1 {
			t.Fatalf("%d DELETE requests, want 1", n)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("marker lost after a failed close: %v", err)
		}
	})
}

// A marker whose socket is gone names a Nearhost that is no longer running,
// whose passthroughs ended with it, so the marker goes.
func TestScriptCloseRemovesMarkerWithoutSocket(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		marker := writeMarker(t, home, "s1", filepath.Join(home, "gone.sock"))
		r := shell.run(t, home, nil, "passthrough-close", "--session", "s1")
		if r.code != 0 || r.stdout != "" || r.stderr != "" {
			t.Fatalf("close: %+v", r)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("marker stayed with its socket gone: %v", err)
		}
	})
}

func TestScriptCloseRemovesMarkerOnSuccess(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		if _, err := exec.LookPath("curl"); err != nil {
			t.Skip("curl is not installed")
		}
		home := shortDir(t)
		sock, _ := answerDelete(t, home, http.StatusNoContent)
		marker := writeMarker(t, home, "s1", sock)
		r := shell.run(t, home, nil, "passthrough-close", "--session", "s1")
		if r.code != 0 {
			t.Fatalf("close: %+v", r)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("marker stayed after a close: %v", err)
		}
	})
}

// On Windows the SessionEnd hook runs nearhost.sh in Git Bash, and the
// marker names the laptop's socket as nearhost.ps1 writes it, from a drive
// letter: the close must reach that Nearhost, not drop the line.
func TestScriptCloseSessionClosesADriveLetterSocket(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed")
	}
	shell := shScript
	home := shortDir(t)
	var line string
	if runtime.GOOS == "windows" {
		sh := needSh(t, ", which runs the SessionEnd hook")
		shell = scriptShell{"git-sh", []string{sh}, script}
		sock, deletes := answerDelete(t, home, http.StatusNoContent)
		line = sock
		if !regexp.MustCompile(`^[A-Za-z]:\\`).MatchString(line) {
			t.Fatalf("socket %q is not a drive-letter path", line)
		}
		checkDriveClose(t, shell, home, line, deletes)
		return
	}
	// Elsewhere no path starts with a drive letter, but a relative one can
	// look like it: C:/d.sock, from home, where the script runs.
	dir := filepath.Join(home, "C:")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	_, deletes := answerDeleteAt(t, filepath.Join(dir, "d.sock"), http.StatusNoContent)
	checkDriveClose(t, shell, home, "C:/d.sock", deletes)
}

// checkDriveClose writes line as session s1's marker, runs shell's close,
// and checks the Nearhost on line was asked once and the marker went.
func checkDriveClose(t *testing.T, shell scriptShell, home, line string, deletes *atomic.Int32) {
	t.Helper()
	marker := writeMarker(t, home, "s1", line)
	r := shell.run(t, home, nil, "passthrough-close", "--session", "s1")
	if r.code != 0 || r.stdout != "" || r.stderr != "" {
		t.Fatalf("close: %+v", r)
	}
	if n := deletes.Load(); n != 1 {
		t.Fatalf("%d DELETE requests for %s, want 1", n, line)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker stayed after the close: %v", err)
	}
}

func TestScriptDecodesHTMLEscapes(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		s, p, _, _ := passScript(t, home, "mini", true)
		p.err = errors.New("port <3000> & more\u2028on\u2029lines")
		r := shell.run(t, home, claudeEnv, "passthrough", "3000")
		id := proposalID.FindStringSubmatch(r.stdout)[1]
		if err := s.AnswerPassthrough(context.Background(), id, AnswerYes); err == nil {
			t.Fatal("the open did not fail")
		}
		r = shell.run(t, home, claudeEnv, "passthrough-wait", id)
		if !strings.Contains(r.stdout, `\u003c3000\u003e \u0026 more\u2028on\u2029lines`) {
			t.Fatalf("body is not escaped: %q", r.stdout)
		}
		want := "Nearhost could not open the port: port <3000> & more on lines"
		if strings.TrimSpace(r.stderr) != want {
			t.Fatalf("stderr %q, want %q", r.stderr, want)
		}
	})
}

// docScript serves origin like scriptServerWith, with a fake Shower and
// DocGrants and the install id "lid".
func docScript(t *testing.T, home, origin string, review bool) (*Server, *fakeShower, *fakeDocGrants, *fakeReviewer) {
	t.Helper()
	sh := &fakeShower{served: map[[2]string]string{}}
	g := newFakeDocGrants()
	s, _ := scriptServerWith(t, home, "k3f9q2", origin, review, Options{ID: "lid", Source: fakeSource{origin: mini}, Shower: sh, DocGrants: g})
	rv := &fakeReviewer{}
	if review {
		s.SetReviewer(rv)
	}
	return s, sh, g, rv
}

// docToken is where the skill keeps requester's documents token for "lid".
func docToken(home, requester string) string {
	return tokenPath(home, requester) + ".documents"
}

// docFile writes a document at rel under dir and returns its resolved path.
func docFile(t *testing.T, dir, rel string) string {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("# plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// realDir is dir with its symlinks resolved, as the script reports paths.
func realDir(t *testing.T, dir string) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestScriptOpenSendsRoot(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		_, sh, g, rv := docScript(t, home, shell.origin(), true)
		repo := filepath.Join(realDir(t, home), "repo")
		docFile(t, repo, "docs/p.md")
		if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
			t.Skipf("git: %v %s", err, out)
		}

		// Without a token the prompt carries the session, label and requester.
		r := shell.run(t, home, claudeEnv, "open", "repo/docs/p.md", "--label", "plan")
		if r.code != 0 || proposalID.FindStringSubmatch(r.stdout) == nil {
			t.Fatalf("open: %+v", r)
		}
		got := rv.docPrompts()
		if len(got) != 1 || got[0].Requester != "claude-code" || got[0].Session != "s1" || got[0].Label != "plan" || got[0].Root != repo || got[0].Doc != "docs/p.md" {
			t.Fatalf("prompts %+v", got)
		}

		// With a documents token the same request opens at once.
		g.grant(shell.origin(), "claude-code", "doc-tok", repo)
		if err := os.MkdirAll(filepath.Dir(docToken(home, "claude-code")), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(docToken(home, "claude-code"), []byte("doc-tok\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		r = shell.run(t, home, claudeEnv, "open", filepath.Join(repo, "docs", "p.md"))
		if r.code != 0 || !strings.Contains(r.stdout, `"state":"open"`) || !strings.Contains(r.stdout, `"url":"https://localhost:6327/d/tree/docs/p.md"`) || strings.Contains(r.stdout, "doc-tok") {
			t.Fatalf("open: %+v", r)
		}
		if shows := sh.showed(); len(shows) != 1 || shows[0].Root != repo || shows[0].Doc != "docs/p.md" || shows[0].Requester != "claude-code" {
			t.Fatalf("shows %+v", shows)
		}
	})
}

func TestScriptOpenWithoutGit(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		_, _, _, rv := docScript(t, home, shell.origin(), true)
		dir := filepath.Join(realDir(t, home), "notes")
		docFile(t, dir, "p.html")
		// Git stops above the temp directory, so a TMPDIR inside a
		// repository does not give the script that repository's root.
		ceiling := "GIT_CEILING_DIRECTORIES=" + filepath.Dir(realDir(t, home))
		r := shell.run(t, home, append([]string{ceiling}, claudeEnv...), "open", filepath.Join(dir, "p.html"))
		if r.code != 0 || proposalID.FindStringSubmatch(r.stdout) == nil {
			t.Fatalf("open: %+v", r)
		}
		got := rv.docPrompts()
		if len(got) != 1 || got[0].Root != dir || got[0].Doc != "p.html" {
			t.Fatalf("prompts %+v", got)
		}
	})
}

func TestScriptOpenWaitStoresDocToken(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		s, _, _, _ := docScript(t, home, shell.origin(), true)
		storeToken(t, home, "claude-code", "pass-tok")
		dir := filepath.Join(realDir(t, home), "notes")
		p := docFile(t, dir, "p.md")
		r := shell.run(t, home, claudeEnv, "open", p)
		m := proposalID.FindStringSubmatch(r.stdout)
		if r.code != 0 || m == nil {
			t.Fatalf("open: %+v", r)
		}
		if err := s.AnswerDocument(context.Background(), m[1], AnswerAlways); err != nil {
			t.Fatal(err)
		}
		r = shell.run(t, home, claudeEnv, "open-wait", m[1])
		if r.code != 0 || !strings.Contains(r.stdout, `"state":"open"`) || strings.Contains(r.stdout, "token") {
			t.Fatalf("open-wait: %+v", r)
		}
		tok, err := os.ReadFile(docToken(home, "claude-code"))
		if err != nil || strings.TrimSpace(string(tok)) != "tok-1" {
			t.Fatalf("documents token %q: %v", tok, err)
		}
		testutil.WantPerm(t, docToken(home, "claude-code"), 0o600)
		if pt, _ := os.ReadFile(tokenPath(home, "claude-code")); strings.TrimSpace(string(pt)) != "pass-tok" {
			t.Fatalf("the passthrough token changed: %q", pt)
		}
	})
}

func TestScriptOpenWaitDeclinedExpired(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		s, _, _, _ := docScript(t, home, shell.origin(), true)
		p := docFile(t, filepath.Join(realDir(t, home), "notes"), "p.md")
		r := shell.run(t, home, claudeEnv, "open", p)
		m := proposalID.FindStringSubmatch(r.stdout)
		if m == nil {
			t.Fatalf("open: %+v", r)
		}
		if err := s.AnswerDocument(context.Background(), m[1], AnswerNo); err != nil {
			t.Fatal(err)
		}
		if r := shell.run(t, home, claudeEnv, "open-wait", m[1]); r.code != 6 || !strings.Contains(r.stdout, `"state":"declined"`) {
			t.Fatalf("declined: %+v", r)
		}
	})
}

func TestScriptOpenRetriesOn401(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		_, _, _, rv := docScript(t, home, shell.origin(), true)
		storeToken(t, home, "claude-code", "pass-tok")
		if err := os.WriteFile(docToken(home, "claude-code"), []byte("revoked\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		p := docFile(t, filepath.Join(realDir(t, home), "notes"), "p.md")
		posts, front := countPosts(t, home, "/v1/documents")
		r := shell.run(t, home, append([]string{"NEARHOST_SOCKET=" + front}, claudeEnv...), "open", p)
		if r.code != 0 || proposalID.FindStringSubmatch(r.stdout) == nil || strings.Contains(r.stdout, `"state"`) {
			t.Fatalf("open: %+v", r)
		}
		if _, err := os.Stat(docToken(home, "claude-code")); !os.IsNotExist(err) {
			t.Fatalf("the revoked token stayed: %v", err)
		}
		if _, err := os.Stat(tokenPath(home, "claude-code")); err != nil {
			t.Fatalf("the passthrough token went: %v", err)
		}
		if got := rv.docPrompts(); len(got) != 1 {
			t.Fatalf("prompts %+v", got)
		}
		if n := posts(); n != 2 {
			t.Fatalf("the server saw %d requests, want 2", n)
		}
	})
}

func TestScriptOpenOldNearhost(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		scriptServer(t, home, "k3f9q2", shell.origin(), true)
		p := docFile(t, filepath.Join(realDir(t, home), "notes"), "p.md")
		r := shell.run(t, home, nil, "open", p)
		if r.code != 5 || !strings.Contains(r.stderr, "update Nearhost") || !strings.Contains(r.stderr, p) {
			t.Fatalf("%+v", r)
		}
	})
}

func TestScriptOpenNoNearhost(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		p := docFile(t, filepath.Join(realDir(t, home), "notes"), "p.md")
		r := shell.run(t, home, nil, "open", p)
		if r.code != 2 || !strings.Contains(r.stderr, "isn't running or connected") || !strings.Contains(r.stderr, p) {
			t.Fatalf("%+v", r)
		}
	})
}

func TestScriptOpenNotConnectedAndUp(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		p := docFile(t, filepath.Join(realDir(t, home), "notes"), "p.md")
		sh := &fakeShower{served: map[[2]string]string{}, resolveErr: ErrNotConnected}
		scriptServerWith(t, home, "k3f9q2", shell.origin(), true, Options{ID: "lid", Source: fakeSource{shell.origin(): mini}, Shower: sh, DocGrants: newFakeDocGrants()})
		r := shell.run(t, home, nil, "open", p)
		if r.code != 2 || !strings.Contains(r.stderr, "isn't connected") || !strings.Contains(r.stderr, p) {
			t.Fatalf("not connected: %+v", r)
		}
		home = shortDir(t)
		p = docFile(t, filepath.Join(realDir(t, home), "notes"), "p.md")
		docScript(t, home, shell.origin(), false)
		r = shell.run(t, home, nil, "open", p)
		if r.code != 3 || !strings.Contains(r.stderr, "has no page or window to ask you in") || !strings.Contains(r.stderr, p) {
			t.Fatalf("up: %+v", r)
		}
	})
}

// Always allow on a folder new to a grant adds the folder and gives no new
// token, so the script keeps the documents token it has, which still works.
func TestScriptOpenKeepsTheTokenForANewFolder(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		s, sh, g, rv := docScript(t, home, shell.origin(), true)
		g.grant(shell.origin(), "claude-code", "doc-tok", "/srv/elsewhere")
		if err := os.MkdirAll(filepath.Dir(docToken(home, "claude-code")), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(docToken(home, "claude-code"), []byte("doc-tok\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		p := docFile(t, filepath.Join(realDir(t, home), "notes"), "p.md")
		r := shell.run(t, home, claudeEnv, "open", p)
		m := proposalID.FindStringSubmatch(r.stdout)
		if r.code != 0 || m == nil {
			t.Fatalf("open: %+v", r)
		}
		if got := rv.docPrompts(); len(got) != 1 || !got[0].NewRoot {
			t.Fatalf("prompts %+v", got)
		}
		if err := s.AnswerDocument(context.Background(), m[1], AnswerAlways); err != nil {
			t.Fatal(err)
		}
		r = shell.run(t, home, claudeEnv, "open-wait", m[1])
		if r.code != 0 || !strings.Contains(r.stdout, `"state":"open"`) || len(sh.showed()) != 1 {
			t.Fatalf("open-wait: %+v", r)
		}
		if tok, _ := os.ReadFile(docToken(home, "claude-code")); strings.TrimSpace(string(tok)) != "doc-tok" {
			t.Fatalf("the token file changed: %q", tok)
		}
		// The kept token now opens the folder without a prompt, with no
		// tree served, as after a restart.
		sh.mu.Lock()
		clear(sh.served)
		sh.mu.Unlock()
		r = shell.run(t, home, claudeEnv, "open", p)
		if r.code != 0 || !strings.Contains(r.stdout, `"state":"open"`) || len(rv.docPrompts()) != 1 {
			t.Fatalf("open again: %+v", r)
		}
	})
}

func TestScriptOpenAgentKeepsNoToken(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		s, sh, g, rv := docScript(t, home, shell.origin(), true)
		g.grant(shell.origin(), "agent", "tok-agent")
		if err := os.MkdirAll(filepath.Dir(docToken(home, "agent")), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(docToken(home, "agent"), []byte("tok-agent\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		p := docFile(t, filepath.Join(realDir(t, home), "notes"), "p.md")
		r := shell.run(t, home, nil, "open", p)
		m := proposalID.FindStringSubmatch(r.stdout)
		if r.code != 0 || m == nil || strings.Contains(r.stdout, `"state"`) {
			t.Fatalf("a token was sent: %+v", r)
		}
		if got := rv.docPrompts(); len(got) != 1 || got[0].Requester != "agent" || got[0].Session != "" || got[0].CanAlways {
			t.Fatalf("prompts %+v", got)
		}
		if err := s.AnswerDocument(context.Background(), m[1], AnswerYes); err != nil {
			t.Fatal(err)
		}
		r = shell.run(t, home, nil, "open-wait", m[1])
		if r.code != 0 || !strings.Contains(r.stdout, `"state":"open"`) || len(sh.showed()) != 1 {
			t.Fatalf("open-wait: %+v", r)
		}
		if tok, _ := os.ReadFile(docToken(home, "agent")); strings.TrimSpace(string(tok)) != "tok-agent" {
			t.Fatalf("the agent's token file changed: %q", tok)
		}
	})
}

func TestScriptOpenRefusedGivesThePath(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		p := docFile(t, filepath.Join(realDir(t, home), "notes"), "p.md")
		sh := &fakeShower{served: map[[2]string]string{}, resolveErr: fmt.Errorf("%w: Nearhost won't serve your whole home folder", ErrBadDocument)}
		scriptServerWith(t, home, "k3f9q2", shell.origin(), true, Options{ID: "lid", Source: fakeSource{shell.origin(): mini}, Shower: sh, DocGrants: newFakeDocGrants()})
		r := shell.run(t, home, claudeEnv, "open", p)
		if r.code != 1 || !strings.Contains(r.stdout, "whole home folder") || !strings.Contains(r.stderr, "Nothing was opened") || !strings.Contains(r.stderr, p) {
			t.Fatalf("refused: %+v", r)
		}
	})
}

func TestScriptOpenWaitExpired(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		sh := &fakeShower{served: map[[2]string]string{}}
		_, c := scriptServerWith(t, home, "k3f9q2", shell.origin(), true, Options{ID: "lid", Source: fakeSource{shell.origin(): mini}, Shower: sh, DocGrants: newFakeDocGrants()})
		p := docFile(t, filepath.Join(realDir(t, home), "notes"), "p.md")
		r := shell.run(t, home, claudeEnv, "open", p)
		m := proposalID.FindStringSubmatch(r.stdout)
		if m == nil {
			t.Fatalf("open: %+v", r)
		}
		c.add(11 * time.Minute)
		if r := shell.run(t, home, claudeEnv, "open-wait", m[1]); r.code != 7 || !strings.Contains(r.stdout, `"state":"expired"`) {
			t.Fatalf("expired: %+v", r)
		}
	})
}

// relay accepts connections on a Unix socket at path and passes each to
// the TCP address addr, as sshd and ssh do for a forward to a loopback
// listener.
func relay(t *testing.T, path, addr string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			in, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer in.Close()
				out, err := net.Dial("tcp", addr)
				if err != nil {
					return
				}
				defer out.Close()
				go func() {
					io.Copy(out, in)
					out.(*net.TCPConn).CloseWrite()
				}()
				io.Copy(in, out)
			}()
		}
	}()
}

// On a server forwarded to a Windows laptop the socket reaches a loopback
// listener that answers only with the token, which the script sends from
// the file beside the socket.
func TestScriptSendsTheTokenFileBesideTheSocket(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed")
	}
	home := shortDir(t)
	s := New(Options{Laptop: "dev@hera", Source: fakeSource{"leto": mini}})
	t.Cleanup(s.Close)
	addr, token, err := s.ListenLoopback("leto")
	if err != nil {
		t.Fatal(err)
	}
	relay(t, filepath.Join(home, ".nearhost", "k3f9q2.sock"), addr)

	// Without the token file the listener refuses, and the script says
	// what it was told rather than that no Nearhost reaches this machine.
	if r := runScript(t, home, nil, "status", "--all"); r.code != 2 || !strings.Contains(r.stderr, noToken) || strings.Contains(r.stderr, "No Nearhost reaches") {
		t.Fatalf("without the token file: %+v", r)
	}
	sock := filepath.Join(home, ".nearhost", "k3f9q2.sock")
	if r := runScript(t, home, []string{"NEARHOST_SOCKET=" + sock}, "socket"); r.code != 2 || !strings.Contains(r.stderr, noToken) {
		t.Fatalf("without the token file, with NEARHOST_SOCKET: %+v", r)
	}
	doc := filepath.Join(home, "plan.md")
	if err := os.WriteFile(doc, []byte("# Plan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := runScript(t, home, nil, "open", doc); r.code != 2 || !strings.Contains(r.stderr, noToken) || strings.Contains(r.stderr, "isn't running") ||
		!strings.Contains(r.stderr, "Tell the user the document is at") {
		t.Fatalf("open without the token file: %+v", r)
	}
	if err := os.WriteFile(filepath.Join(home, ".nearhost", "k3f9q2.token"), []byte("X-Nearhost-Token: "+token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := runScript(t, home, nil, "status", "--all")
	if r.code != 0 || !strings.Contains(r.stdout, `"name":"inner"`) {
		t.Fatalf("with the token file: %+v", r)
	}
	if r := runScript(t, home, []string{"NEARHOST_SOCKET=" + sock}, "socket"); r.code != 0 || !strings.Contains(r.stdout, `"origin":"leto"`) {
		t.Fatalf("with NEARHOST_SOCKET: %+v", r)
	}
}

// A socket that wants a token this machine lacks is reported only when no
// other Nearhost answers.
func TestScriptPrefersASocketThatAnswersToOneThatWantsAToken(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed")
	}
	home := shortDir(t)
	s := New(Options{Laptop: "dev@hera", Source: fakeSource{"leto": mini}})
	t.Cleanup(s.Close)
	addr, _, err := s.ListenLoopback("leto")
	if err != nil {
		t.Fatal(err)
	}
	// Sorted after the one that answers, and before it, so the order of
	// the glob does not decide.
	relay(t, filepath.Join(home, ".nearhost", "aaaaaa.sock"), addr)
	scriptServer(t, home, "k3f9q2", "leto", true)
	relay(t, filepath.Join(home, ".nearhost", "zzzzzz.sock"), addr)
	if r := runScript(t, home, nil, "socket"); r.code != 0 || !strings.Contains(r.stdout, "k3f9q2.sock") {
		t.Fatalf("%+v", r)
	}
}

func TestScriptPassthroughRefusedWaits(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		s, c := scriptServerWith(t, home, "k3f9q2", "mini", true, Options{ID: "lid", Source: fakeSource{"mini": mini},
			Passer: &fakePasser{live: map[string]Passthrough{}, taken: map[int]bool{}}, Grants: &fakeGrants{tokens: map[[2]string]string{}}})
		r := shell.run(t, home, claudeEnv, "passthrough", "3000")
		if err := s.AnswerPassthrough(context.Background(), proposalID.FindStringSubmatch(r.stdout)[1], AnswerNo); err != nil {
			t.Fatal(err)
		}
		c.add(10 * time.Second)
		posts, front := countPosts(t, home)
		r = shell.run(t, home, append([]string{"NEARHOST_SOCKET=" + front}, claudeEnv...), "passthrough", "3001")
		want := "The user said No to a passthrough from this machine in Nearhost. Do not ask again on your own; if the user asks you to, ask again in 20 seconds or later."
		if r.code != 6 || strings.TrimSpace(r.stderr) != want || !strings.Contains(r.stdout, `refused; ask again in 20 s`) {
			t.Fatalf("refused: %+v", r)
		}
		if n := posts(); n != 1 {
			t.Fatalf("the script asked %d times, want 1", n)
		}
	})
}

// The cap on open passthroughs is a 429 too, but no refusal: the scripts
// report it as a failure with Nearhost's message, and do not ask again.
func TestScriptPassthroughCapFails(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		g := &fakeGrants{tokens: map[[2]string]string{{"mini", "claude-code"}: "good"}}
		scriptServerWith(t, home, "k3f9q2", "mini", true, Options{ID: "lid", Source: fakeSource{"mini": mini},
			Passer: &fakePasser{live: map[string]Passthrough{}, taken: map[int]bool{}}, Grants: g})
		sock := filepath.Join(home, ".nearhost", "k3f9q2.sock")
		for port := 3000; port < 3000+maxPassthroughs; port++ {
			if st, out := call(t, sock, "POST", "/v1/passthroughs", fmt.Sprintf(`{"port": %d, "requester": "claude-code", "token": "good"}`, port)); st != http.StatusCreated {
				t.Fatalf("port %d: %d %v", port, st, out)
			}
		}
		posts, front := countPosts(t, home)
		r := shell.run(t, home, append([]string{"NEARHOST_SOCKET=" + front}, claudeEnv...), "passthrough", "4000")
		if r.code != 1 || !strings.Contains(r.stdout, tooManyPasses) || strings.Contains(r.stderr, "said No") {
			t.Fatalf("%+v", r)
		}
		if n := posts(); n != 1 {
			t.Fatalf("the script asked %d times, want 1", n)
		}
	})
}

// A doctor run already going on the server is a 429 too, but no refusal:
// the scripts report it as a failure with Nearhost's message, and do not
// ask again.
func TestScriptDoctorBusyFails(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		dir, src := scriptProject(t, home, shell.origin())
		d := &fakeDoctor{gate: make(chan struct{})}
		defer close(d.gate)
		scriptServerWith(t, home, "k3f9q2", shell.origin(), true, Options{Source: src, Doctor: d})
		sock := filepath.Join(home, ".nearhost", "k3f9q2.sock")
		go func() {
			if resp, err := client(sock).Get("http://nearhost/v1/doctor?path=" + url.QueryEscape(dir)); err == nil {
				resp.Body.Close()
			}
		}()
		waitCalls(t, d, 1)
		gets, front := countCalls(t, home, "GET", "/v1/doctor")
		r := shell.run(t, home, []string{"NEARHOST_SOCKET=" + front}, "doctor", dir)
		if r.code != 1 || !strings.Contains(r.stdout, doctorBusy) || strings.Contains(r.stderr, "said No") {
			t.Fatalf("%+v", r)
		}
		if n := gets(); n != 1 {
			t.Fatalf("the script asked %d times, want 1", n)
		}
	})
}

// A doctor run that started moments ago is a 429 with a wait, but no
// refusal: the scripts report it as a failure with Nearhost's message, and
// do not ask again.
func TestScriptDoctorTooSoonFails(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		dir, src := scriptProject(t, home, shell.origin())
		scriptServerWith(t, home, "k3f9q2", shell.origin(), true, Options{Source: src, Doctor: &fakeDoctor{}})
		if r := shell.run(t, home, nil, "doctor", dir); r.code != 0 {
			t.Fatalf("the first run: %+v", r)
		}
		gets, front := countCalls(t, home, "GET", "/v1/doctor")
		r := shell.run(t, home, []string{"NEARHOST_SOCKET=" + front}, "doctor", dir)
		want := "a doctor run for this server started moments ago; ask again in 5 s"
		if r.code != 1 || !strings.Contains(r.stdout, want) || strings.Contains(r.stderr, "said No") {
			t.Fatalf("%+v", r)
		}
		if n := gets(); n != 1 {
			t.Fatalf("the script asked %d times, want 1", n)
		}
	})
}

func TestScriptOpenRefusedWaits(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		s, _, _, _ := docScript(t, home, shell.origin(), true)
		p := docFile(t, filepath.Join(realDir(t, home), "notes"), "p.md")
		r := shell.run(t, home, claudeEnv, "open", p)
		if err := s.AnswerDocument(context.Background(), proposalID.FindStringSubmatch(r.stdout)[1], AnswerNo); err != nil {
			t.Fatal(err)
		}
		posts, front := countPosts(t, home, "/v1/documents")
		r = shell.run(t, home, append([]string{"NEARHOST_SOCKET=" + front}, claudeEnv...), "open", p)
		want := "The user said No to a document from this machine in Nearhost, so nothing was opened. Do not ask again on your own; if the user asks you to, ask again in 30 seconds or later. Tell the user the document is at " + p + "."
		if r.code != 6 || strings.TrimSpace(r.stderr) != want || !strings.Contains(r.stdout, `refused; ask again in 30 s`) {
			t.Fatalf("refused: %+v", r)
		}
		if n := posts(); n != 1 {
			t.Fatalf("the script asked %d times, want 1", n)
		}
	})
}

func TestScriptWaitHeldBack(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		s, c := scriptServerWith(t, home, "k3f9q2", "mini", true, Options{ID: "lid", Source: fakeSource{"mini": mini},
			Passer: &fakePasser{live: map[string]Passthrough{}, taken: map[int]bool{}}, Grants: &fakeGrants{tokens: map[[2]string]string{}}})
		first := proposalID.FindStringSubmatch(shell.run(t, home, claudeEnv, "passthrough", "3000").stdout)[1]
		held := proposalID.FindStringSubmatch(shell.run(t, home, claudeEnv, "passthrough", "3001").stdout)[1]
		if err := s.AnswerPassthrough(context.Background(), first, AnswerNo); err != nil {
			t.Fatal(err)
		}
		c.add(10 * time.Second)
		posts, front := countPosts(t, home)
		r := shell.run(t, home, append([]string{"NEARHOST_SOCKET=" + front}, claudeEnv...), "passthrough-wait", held)
		want := "The user said No to another passthrough from this machine in Nearhost, so this one was not asked. Do not ask again on your own; if the user asks you to, ask again in 20 seconds or later."
		if r.code != 6 || strings.TrimSpace(r.stderr) != want || !strings.Contains(r.stdout, `"held":true`) {
			t.Fatalf("held: %+v", r)
		}
		if n := posts(); n != 0 {
			t.Fatalf("the script asked again %d times", n)
		}
		c.add(30 * time.Second)
		r = shell.run(t, home, claudeEnv, "passthrough-wait", held)
		want = "The user said No to another passthrough from this machine in Nearhost, so this one was not asked. Do not ask again on your own; if the user asks you to, you may ask again now."
		if r.code != 6 || strings.TrimSpace(r.stderr) != want {
			t.Fatalf("after the hold: %+v", r)
		}
	})
}

func TestScriptOpenWaitHeldBack(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		s, _, _, _ := docScript(t, home, shell.origin(), true)
		dir := filepath.Join(realDir(t, home), "notes")
		first := proposalID.FindStringSubmatch(shell.run(t, home, claudeEnv, "open", docFile(t, dir, "a.md")).stdout)[1]
		held := proposalID.FindStringSubmatch(shell.run(t, home, claudeEnv, "open", docFile(t, dir, "b.md")).stdout)[1]
		if err := s.AnswerDocument(context.Background(), first, AnswerNo); err != nil {
			t.Fatal(err)
		}
		r := shell.run(t, home, claudeEnv, "open-wait", held)
		want := "The user said No to another document from this machine in Nearhost, so this one was not asked. Do not ask again on your own; if the user asks you to, ask again in 30 seconds or later."
		if r.code != 6 || strings.TrimSpace(r.stderr) != want {
			t.Fatalf("held: %+v", r)
		}
	})
}

// pageScript serves origin like scriptServerWith, with everything a request
// can ask for and page as the address of Nearhost's page, which "" leaves out.
func pageScript(t *testing.T, home, origin string, review bool, page string) string {
	t.Helper()
	dir, src := scriptProject(t, home, origin)
	rl := &fakeReloader{}
	rl.answer(waiting, nil)
	scriptServerWith(t, home, "k3f9q2", origin, review, Options{
		ID: "lid", Source: src, Reload: rl,
		Passer: &fakePasser{live: map[string]Passthrough{}, taken: map[int]bool{}}, Grants: &fakeGrants{tokens: map[[2]string]string{}},
		Shower: &fakeShower{served: map[[2]string]string{}}, DocGrants: newFakeDocGrants(),
		PageURL: func() string { return page },
	})
	return dir
}

// The scripts tell the agent where to answer a request on Nearhost's page,
// for the user to open.
func TestScriptPrintsApproveURL(t *testing.T) {
	const page = "https://nearhost.localhost:6327"
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		dir := pageScript(t, home, shell.origin(), true, page)
		answer := func(path string) string {
			return "Answer in Nearhost on dev@k3f9q2, or open " + page + path + "\n"
		}

		// A passthrough is a server's, so its Nearhost serves "mini" for both shells.
		serverHome := shortDir(t)
		pageScript(t, serverHome, "mini", true, page)
		r := shell.run(t, serverHome, claudeEnv, "passthrough", "3000")
		id := proposalID.FindStringSubmatch(r.stdout)
		if r.code != 0 || id == nil || r.stderr != answer("/approve/"+id[1]) {
			t.Fatalf("passthrough: %+v", r)
		}
		p := docFile(t, filepath.Join(realDir(t, home), "notes"), "p.md")
		r = shell.run(t, home, claudeEnv, "open", p)
		id = proposalID.FindStringSubmatch(r.stdout)
		if r.code != 0 || id == nil || r.stderr != answer("/approve/"+id[1]) {
			t.Fatalf("open: %+v", r)
		}
		r = shell.run(t, home, nil, "reload", dir)
		if r.code != 0 || r.stderr != answer("/change/myapp") {
			t.Fatalf("reload: %+v", r)
		}
	})
}

func TestScriptNoURLNoLine(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		dir := pageScript(t, home, shell.origin(), true, "")
		p := docFile(t, filepath.Join(realDir(t, home), "notes"), "p.md")
		fresh := filepath.Join(home, "new")
		os.Mkdir(fresh, 0o755)
		serverHome := shortDir(t)
		pageScript(t, serverHome, "mini", true, "")
		for _, args := range [][]string{{"passthrough", "3000"}, {"open", p}, {"reload", dir}, {"propose", fresh}} {
			h := home
			if args[0] == "passthrough" {
				// A passthrough is a server's, so its Nearhost serves "mini" for both shells.
				h = serverHome
			}
			r := shell.run(t, h, claudeEnv, args...)
			if r.code != 0 || !strings.Contains(r.stdout, `"expires_at"`) || r.stderr != "" {
				t.Fatalf("%s: %+v", args[0], r)
			}
		}
	})
}

// A proposal to add a project cannot be accepted on the page, so the script
// gives the advice and the link to decline it.
func TestScriptAddAdvice(t *testing.T) {
	const page = "https://nearhost.localhost:6327"
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		pageScript(t, home, shell.origin(), true, page)
		app := filepath.Join(home, "new")
		os.Mkdir(app, 0o755)
		r := shell.run(t, home, nil, "propose", app)
		id := proposalID.FindStringSubmatch(r.stdout)
		want := "Run nearhost on dev@k3f9q2 to review it. Or decline it at " + page + "/approve/"
		if r.code != 0 || id == nil || r.stderr != want+id[1]+"\n" {
			t.Fatalf("propose: %+v", r)
		}
	})
}

// With nothing to answer on, every request that needs an answer exits 3 and
// says so as the API gave it.
func TestScriptExit3NewCopy(t *testing.T) {
	const copy = "Nearhost on dev@k3f9q2 has no page or window to ask you in; open nearhost there, then ask again."
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		dir := pageScript(t, home, shell.origin(), false, "")
		p := docFile(t, filepath.Join(realDir(t, home), "notes"), "p.md")
		fresh := filepath.Join(home, "new")
		os.Mkdir(fresh, 0o755)
		serverHome := shortDir(t)
		pageScript(t, serverHome, "mini", false, "")
		for _, args := range [][]string{{"propose", fresh}, {"reload", dir}, {"passthrough", "3000"}, {"open", p}} {
			h := home
			if args[0] == "passthrough" {
				// A passthrough is a server's, so its Nearhost serves "mini" for both shells.
				h = serverHome
			}
			r := shell.run(t, h, claudeEnv, args...)
			if r.code != 3 || !strings.Contains(r.stdout+r.stderr, copy) || strings.Contains(r.stdout+r.stderr, "nearhost up") {
				t.Fatalf("%s: %+v", args[0], r)
			}
		}
	})
}

// legacyNoPrompts is what an older Nearhost answered a request with when
// nothing could ask the user, as it ran as nearhost up.
const legacyNoPrompts = "open nearhost on dev@k3f9q2 to approve this; `nearhost up` cannot show prompts"

// legacyScript serves origin on home/.nearhost/k3f9q2.sock as pageScript
// does with nothing to answer on, but as an older Nearhost: each 409 that
// says so has its body in that Nearhost's words.
func legacyScript(t *testing.T, home, origin string) {
	t.Helper()
	_, src := scriptProject(t, home, origin)
	realHome := shortDir(t)
	scriptServerWith(t, realHome, "k3f9q2", origin, false, Options{
		ID: "lid", Source: src,
		Passer: &fakePasser{live: map[string]Passthrough{}, taken: map[int]bool{}}, Grants: &fakeGrants{tokens: map[[2]string]string{}},
		Shower: &fakeShower{served: map[[2]string]string{}}, DocGrants: newFakeDocGrants(),
	})
	realSock := filepath.Join(realHome, ".nearhost", "k3f9q2.sock")
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) { r.Out.URL.Scheme, r.Out.URL.Host = "http", "nearhost" },
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", realSock)
		}},
		ModifyResponse: func(resp *http.Response) error {
			if resp.StatusCode != http.StatusConflict {
				return nil
			}
			b, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				return err
			}
			if bytes.Contains(b, []byte("has no page or window to ask you in")) {
				b, _ = json.Marshal(map[string]string{"error": legacyNoPrompts})
			}
			resp.Body = io.NopCloser(bytes.NewReader(b))
			resp.ContentLength = int64(len(b))
			resp.Header.Set("Content-Length", strconv.Itoa(len(b)))
			return nil
		},
	}
	os.MkdirAll(filepath.Join(home, ".nearhost"), 0o700)
	l, err := net.Listen("unix", filepath.Join(home, ".nearhost", "k3f9q2.sock"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: proxy}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
}

// An older Nearhost that says it cannot show prompts as nearhost up still
// gets exit 3 for a passthrough and a document.
func TestScriptExit3LegacyCopy(t *testing.T) {
	eachShell(t, func(t *testing.T, shell scriptShell) {
		home := shortDir(t)
		legacyScript(t, home, shell.origin())
		p := docFile(t, filepath.Join(realDir(t, home), "notes"), "p.md")
		serverHome := shortDir(t)
		legacyScript(t, serverHome, "mini")
		for _, args := range [][]string{{"passthrough", "3000"}, {"open", p}} {
			h := home
			if args[0] == "passthrough" {
				// A passthrough is a server's, so its Nearhost serves "mini" for both shells.
				h = serverHome
			}
			r := shell.run(t, h, claudeEnv, args...)
			if r.code != 3 || !strings.Contains(r.stdout+r.stderr, "cannot show prompts") {
				t.Fatalf("%s: %+v", args[0], r)
			}
		}
	})
}

// skillTrap is a message carrying what, read as a reply's own, would
// mislead the skill: in its data every name the skill reads, and in its
// text and quoted lines those names as JSON.
func skillTrap(level report.Level) *report.Message {
	m := report.Warnf(report.DiscoveryIncomplete, `a preset said "state":"open" and "error":"refused; ask again in 99 s"`).
		Because(`"reason":"a warning's reason"`).
		Quote(`"approve_url":"https://evil.test/approve"`, `"token":"evil"`, `"state":"declined"`)
	m.Level, m.Context = level, "read presets"
	for _, k := range []string{"error", "state", "api", "laptop", "approve_url", "advice", "reason", "id", "token"} {
		m.With(k, "evil")
	}
	return m.With("held", true).With("retry_after", 99).With("preset", "laravel")
}

// warnReloader reports warn as each reload runs.
type warnReloader struct {
	*fakeReloader
	warn *report.Message
}

func (r warnReloader) Reload(ctx context.Context, name string) (Reloaded, error) {
	report.To(ctx, r.warn)
	return r.fakeReloader.Reload(ctx, name)
}

// warnShower reports warn as each document is resolved.
type warnShower struct {
	*fakeShower
	warn *report.Message
}

func (s warnShower) Resolve(ctx context.Context, origin, p, root string) (Resolved, error) {
	report.To(ctx, s.warn)
	return s.fakeShower.Resolve(ctx, origin, p, root)
}

// The skill as shipped reads replies that carry warnings, and projects that
// carry problems, as it read them before: the first "error" is the reply's,
// the state, the link and the wait are the reply's own, and the exit
// statuses are unchanged.
func TestSkillReadsBodiesWithWarningsAndProblems(t *testing.T) {
	const page = "https://nearhost.localhost:6327"
	const copy = "Nearhost on dev@k3f9q2 has no page or window to ask you in; open nearhost there, then ask again."
	eachShell(t, func(t *testing.T, shell scriptShell) {
		o := shell.origin()
		reloadServer := func(t *testing.T, review bool, got Reloaded) (home, dir string) {
			home = shortDir(t)
			dir, src := scriptProject(t, home, o)
			rl := &fakeReloader{}
			rl.answer(got, nil)
			scriptServerWith(t, home, "k3f9q2", o, review, Options{
				ID: "lid", Source: src, Reload: warnReloader{rl, skillTrap(report.Warning)}, PageURL: func() string { return page },
			})
			return home, dir
		}
		docServer := func(t *testing.T, review bool) (*Server, string, string) {
			home := shortDir(t)
			s, _ := scriptServerWith(t, home, "k3f9q2", o, review, Options{
				ID: "lid", Source: fakeSource{o: mini}, DocGrants: newFakeDocGrants(),
				Shower: warnShower{&fakeShower{served: map[[2]string]string{}}, skillTrap(report.Warning)},
			})
			return s, home, docFile(t, filepath.Join(realDir(t, home), "notes"), "p.md")
		}
		clean := func(t *testing.T, r scriptResult) {
			t.Helper()
			if strings.Contains(r.stderr, "evil") || strings.Contains(r.stderr, "99 seconds") || strings.Contains(r.stderr, "a warning's reason") {
				t.Fatalf("the skill read a warning's field as the reply's: %+v", r)
			}
		}

		t.Run("200 status with a problem", func(t *testing.T) {
			home := shortDir(t)
			dir := filepath.Join(home, "myapp")
			os.Mkdir(dir, 0o755)
			real := realDir(t, dir)
			scriptServerOf(t, home, "k3f9q2", o, true, fakeSource{o: {{
				Name: "myapp", Path: real, URL: "https://myapp.test", State: "error",
				Error: "start web: port 8080 is in use", Problem: skillTrap(report.Error),
			}}})
			r := shell.run(t, home, nil, "status", dir)
			if r.code != 0 || !strings.Contains(r.stdout, `"error":"start web: port 8080 is in use"`) || !strings.Contains(r.stdout, `"problem":{`) ||
				!strings.Contains(r.stdout, `"context":"read presets"`) || !strings.Contains(r.stdout, `"verbatim":[`) {
				t.Fatalf("status: %+v", r)
			}
			clean(t, r)
		})
		t.Run("202 reload waiting", func(t *testing.T) {
			home, dir := reloadServer(t, true, waiting)
			r := shell.run(t, home, nil, "reload", dir)
			if r.code != 0 || !strings.Contains(r.stdout, `"warnings":[`) || r.stderr != "Answer in Nearhost on dev@k3f9q2, or open "+page+"/change/myapp\n" {
				t.Fatalf("reload: %+v", r)
			}
		})
		t.Run("200 reload declined", func(t *testing.T) {
			home, dir := reloadServer(t, true, Reloaded{Declined: true})
			r := shell.run(t, home, nil, "reload", dir)
			if r.code != 6 || !strings.Contains(r.stdout, `"warnings":[`) {
				t.Fatalf("reload declined: %+v", r)
			}
			clean(t, r)
		})
		t.Run("409 reload without a window", func(t *testing.T) {
			home, dir := reloadServer(t, false, waiting)
			r := shell.run(t, home, nil, "reload", dir)
			if r.code != 3 || !strings.Contains(r.stdout, `{"error":"`+copy+`"`) || !strings.Contains(r.stdout, `"warnings":[`) {
				t.Fatalf("reload without a window: %+v", r)
			}
			clean(t, r)
		})
		t.Run("409 open without a window", func(t *testing.T) {
			_, home, p := docServer(t, false)
			r := shell.run(t, home, claudeEnv, "open", p)
			if r.code != 3 || !strings.HasPrefix(r.stderr, copy+"\n") {
				t.Fatalf("open without a window: %+v", r)
			}
			clean(t, r)
		})
		t.Run("429 open refused", func(t *testing.T) {
			s, home, p := docServer(t, true)
			r := shell.run(t, home, claudeEnv, "open", p)
			m := proposalID.FindStringSubmatch(r.stdout)
			if m == nil {
				t.Fatalf("open: %+v", r)
			}
			if err := s.AnswerDocument(context.Background(), m[1], AnswerNo); err != nil {
				t.Fatal(err)
			}
			r = shell.run(t, home, claudeEnv, "open", p)
			want := "The user said No to a document from this machine in Nearhost, so nothing was opened. Do not ask again on your own; if the user asks you to, ask again in 30 seconds or later. Tell the user the document is at " + p + "."
			if r.code != 6 || strings.TrimSpace(r.stderr) != want || !strings.Contains(r.stdout, `{"error":"refused; ask again in 30 s"`) ||
				!strings.Contains(r.stdout, `"warnings":[`) {
				t.Fatalf("refused: %+v", r)
			}
		})
	})
}
