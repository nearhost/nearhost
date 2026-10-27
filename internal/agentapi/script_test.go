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
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/doctor"
)

// The skill's script, run against the API: it must find the socket, speak
// the API and turn its answers into the exit statuses the skill reads.

const script = "../../agents/nearhost/skills/nearhost/scripts/nearhost.sh"

type scriptResult struct {
	code           int
	stdout, stderr string
}

// runScript runs the script with home as $HOME, a PATH without nearhost,
// and env added.
func runScript(t *testing.T, home string, env []string, args ...string) scriptResult {
	t.Helper()
	return runScriptIn(t, "sh", home, env, args...)
}

// runScriptIn is runScript with the interpreter shell.
func runScriptIn(t *testing.T, shell, home string, env []string, args ...string) scriptResult {
	t.Helper()
	path, err := filepath.Abs(script)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(shell, append([]string{path}, args...)...)
	cmd.Env = append([]string{"HOME=" + home, "PATH=/usr/bin:/bin"}, env...)
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
	home := shortDir(t)
	scriptServer(t, home, "k3f9q2", "mini", true)
	r := runScript(t, home, nil, "socket")
	if r.code != 0 || !strings.Contains(r.stdout, home+"/.nearhost/k3f9q2.sock\n") || !strings.Contains(r.stdout, `"origin":"mini"`) {
		t.Fatalf("socket: %+v", r)
	}
	os.MkdirAll(filepath.Join(home, "code", "myapp", "src"), 0o755)
	r = runScript(t, home, nil, "status", "--all")
	if r.code != 0 || !strings.Contains(r.stdout, `"name":"inner"`) {
		t.Fatalf("status --all: %+v", r)
	}
	r = runScript(t, home, nil, "status", "/home/dev/code/myapp/src")
	if r.code != 1 || !strings.Contains(r.stderr, "is not a directory here") {
		t.Fatalf("status of a path not here: %+v", r)
	}
}

func TestScriptProposesAndWaitsForTheAnswer(t *testing.T) {
	home := shortDir(t)
	s, c := scriptServer(t, home, "k3f9q2", "mini", true)
	app := filepath.Join(home, "my app")
	os.Mkdir(app, 0o755)
	r := runScript(t, home, nil, "propose", "--web", "5173", "--name", `the "app"`, app)
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
	r = runScript(t, home, nil, "wait", id)
	if r.code != 0 || !strings.Contains(r.stdout, `"state":"accepted"`) || !strings.Contains(r.stdout, `"url":"https://myapp.test"`) {
		t.Fatalf("wait: %+v", r)
	}

	r = runScript(t, home, nil, "propose", app)
	id = proposalID.FindStringSubmatch(r.stdout)[1]
	s.Decline(id, "no ports found")
	if r := runScript(t, home, nil, "wait", id); r.code != 6 || !strings.Contains(r.stdout, `"reason":"no ports found"`) {
		t.Fatalf("declined: %+v", r)
	}

	r = runScript(t, home, nil, "propose", app)
	id = proposalID.FindStringSubmatch(r.stdout)[1]
	c.add(10 * time.Minute)
	if r := runScript(t, home, nil, "wait", id); r.code != 7 || !strings.Contains(r.stdout, `"state":"expired"`) {
		t.Fatalf("expired: %+v", r)
	}
}

func TestScriptReportsAProjectThatExists(t *testing.T) {
	home := shortDir(t)
	dir := filepath.Join(home, "inner")
	os.Mkdir(dir, 0o755)
	real, _ := filepath.EvalSymlinks(dir)
	scriptServerOf(t, home, "k3f9q2", "mini", true, fakeSource{"mini": {{Name: "inner", Path: real, URL: "https://inner.test", State: "running"}}})
	r := runScript(t, home, nil, "propose", dir)
	if r.code != 0 || !strings.Contains(r.stdout, `"state":"exists"`) || !strings.Contains(r.stdout, "https://inner.test") {
		t.Fatalf("%+v", r)
	}
}

func TestScriptSaysWhenNearhostRunsWithoutItsInterface(t *testing.T) {
	home := shortDir(t)
	scriptServer(t, home, "k3f9q2", "mini", false)
	if r := runScript(t, home, nil, "propose", home); r.code != 3 || !strings.Contains(r.stdout, "open nearhost on dev@k3f9q2") {
		t.Fatalf("%+v", r)
	}
}

func TestScriptAsksWhichLaptopWhenSeveralAnswer(t *testing.T) {
	home := shortDir(t)
	scriptServer(t, home, "aaaaaa", "mini", true)
	scriptServer(t, home, "bbbbbb", "mini", true)
	r := runScript(t, home, nil, "socket")
	if r.code != 4 || !strings.Contains(r.stderr, "aaaaaa.sock  (dev@aaaaaa)") || !strings.Contains(r.stderr, "bbbbbb.sock  (dev@bbbbbb)") {
		t.Fatalf("%+v", r)
	}
	chosen := filepath.Join(home, ".nearhost", "bbbbbb.sock")
	if r := runScript(t, home, []string{"NEARHOST_SOCKET=" + chosen}, "socket"); r.code != 0 || !strings.Contains(r.stdout, `"laptop":"dev@bbbbbb"`) {
		t.Fatalf("with NEARHOST_SOCKET: %+v", r)
	}
}

func TestScriptSkipsSocketsNothingAnswersOn(t *testing.T) {
	home := shortDir(t)
	scriptServer(t, home, "k3f9q2", "mini", true)
	// A socket a run that has ended left, as sshd leaves them.
	l, err := net.Listen("unix", filepath.Join(home, ".nearhost", "zzzzzz.sock"))
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()
	if r := runScript(t, home, nil, "socket"); r.code != 0 || !strings.Contains(r.stdout, "k3f9q2.sock") {
		t.Fatalf("%+v", r)
	}
}

func TestScriptSaysWhatToDoWhenNoNearhostAnswers(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed")
	}
	home := shortDir(t)
	r := runScript(t, home, []string{"SSH_CONNECTION=10.0.0.2 50000 10.0.0.9 22"}, "propose", home)
	if r.code != 2 || !strings.Contains(r.stderr, "start nearhost there and connect this server (select it and press c)") ||
		!strings.Contains(r.stderr, "nearhost add --server ") {
		t.Fatalf("on a server: %+v", r)
	}
	r = runScript(t, home, nil, "status")
	if r.code != 2 || !strings.Contains(r.stderr, "Nearhost is not installed here") {
		t.Fatalf("on a machine without it: %+v", r)
	}
}

func TestScriptRefusesAnAPIItDoesNotSpeak(t *testing.T) {
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
	if r := runScript(t, home, nil, "socket"); r.code != 5 || !strings.Contains(r.stderr, "update the nearhost plugin") {
		t.Fatalf("%+v", r)
	}
}

func TestScriptStatusFailsWhenNoProjectHoldsThePath(t *testing.T) {
	home := shortDir(t)
	scriptServer(t, home, "k3f9q2", "mini", true)
	r := runScript(t, home, nil, "status", home)
	if r.code != 1 || !strings.Contains(r.stdout, "no project holds") {
		t.Fatalf("%+v", r)
	}
}

func TestScriptFindsAProjectFromASymlinkedDirectory(t *testing.T) {
	home := shortDir(t)
	real := filepath.Join(home, "code", "inner")
	os.MkdirAll(real, 0o755)
	resolved, _ := filepath.EvalSymlinks(real)
	link := filepath.Join(home, "link")
	os.Symlink(real, link)
	s, _ := scriptServerOf(t, home, "k3f9q2", "mini", true, fakeSource{"mini": {{Name: "inner", Path: resolved, URL: "https://inner.test"}}})
	if r := runScript(t, home, nil, "status", link); r.code != 0 || !strings.Contains(r.stdout, `"name":"inner"`) {
		t.Fatalf("status from the link: %+v", r)
	}
	if r := runScript(t, home, nil, "propose", link); r.code != 0 || !strings.Contains(r.stdout, `"state":"exists"`) {
		t.Fatalf("propose from the link: %+v", r)
	}
	other := filepath.Join(home, "other")
	os.Mkdir(other, 0o755)
	os.Symlink(other, filepath.Join(home, "otherlink"))
	r := runScript(t, home, nil, "propose", filepath.Join(home, "otherlink"))
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
}

func TestScriptProposesAPathWithATab(t *testing.T) {
	home := shortDir(t)
	s, _ := scriptServer(t, home, "k3f9q2", "mini", true)
	dir := filepath.Join(home, "a\tb")
	os.Mkdir(dir, 0o755)
	r := runScript(t, home, nil, "propose", dir)
	id := proposalID.FindStringSubmatch(r.stdout)
	if r.code != 0 || id == nil {
		t.Fatalf("%+v", r)
	}
	s.mu.Lock()
	got := s.proposals[id[1]].Path
	s.mu.Unlock()
	if !strings.HasSuffix(got, "/a\tb") {
		t.Fatalf("proposed %q", got)
	}
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

// scriptProject makes a directory that Nearhost holds as the project myapp.
func scriptProject(t *testing.T, home string) (dir string, src Source) {
	t.Helper()
	dir = filepath.Join(home, "myapp")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(dir)
	return dir, fakeSource{"mini": {{Name: "myapp", Path: real, URL: "https://myapp.test", State: "running"}}}
}

func TestScriptDoctorPrintsFindings(t *testing.T) {
	home := shortDir(t)
	dir, src := scriptProject(t, home)
	d := &fakeDoctor{findings: []doctor.Finding{{ID: "routes.stale-port", Severity: "warning", Project: "myapp", Message: "nothing listens"}}}
	scriptServerWith(t, home, "k3f9q2", "mini", true, Options{Source: src, Doctor: d})
	r := runScript(t, home, nil, "doctor", dir)
	if r.code != 0 || !strings.Contains(r.stdout, `"findings"`) || !strings.Contains(r.stdout, "routes.stale-port") {
		t.Fatalf("doctor: %+v", r)
	}

	other := shortDir(t)
	scriptServerWith(t, other, "z9", "mini", true, Options{Source: src})
	r = runScript(t, other, nil, "doctor", ".")
	if r.code != 5 || !strings.Contains(r.stderr, "update Nearhost") || !strings.Contains(r.stderr, "doctor") {
		t.Fatalf("without a doctor: %+v", r)
	}
}

func TestScriptReloadAndWait(t *testing.T) {
	home := shortDir(t)
	dir, src := scriptProject(t, home)
	rl := &fakeReloader{}
	rl.answer(waiting, nil)
	s, _ := scriptServerWith(t, home, "k3f9q2", "mini", true, Options{Source: src, Reload: rl})
	r := runScript(t, home, nil, "reload", dir)
	m := proposalID.FindStringSubmatch(r.stdout)
	if r.code != 0 || m == nil || !strings.Contains(r.stdout, `"state":"pending"`) {
		t.Fatalf("reload: %+v", r)
	}
	id := m[1]

	go func() {
		time.Sleep(200 * time.Millisecond)
		s.ChangeAnswered("myapp", "b", true)
	}()
	if r := runScript(t, home, nil, "wait", id); r.code != 0 || !strings.Contains(r.stdout, `"state":"accepted"`) {
		t.Fatalf("wait: %+v", r)
	}

	rl.answer(Reloaded{Items: []string{"a.example.com"}, SHA256: "c"}, nil)
	first := proposalID.FindStringSubmatch(runScript(t, home, nil, "reload", dir).stdout)[1]
	rl.answer(Reloaded{Items: []string{"b.example.com"}, SHA256: "d"}, nil)
	if r := runScript(t, home, nil, "reload", dir); r.code != 0 || proposalID.FindStringSubmatch(r.stdout)[1] == first {
		t.Fatalf("changed reload: %+v", r)
	}
	if r := runScript(t, home, nil, "wait", first); r.code != 8 || !strings.Contains(r.stdout, `"state":"superseded"`) {
		t.Fatalf("superseded: %+v", r)
	}
}

func TestScriptReloadUnderUp(t *testing.T) {
	home := shortDir(t)
	dir, src := scriptProject(t, home)
	rl := &fakeReloader{}
	rl.answer(waiting, nil)
	scriptServerWith(t, home, "k3f9q2", "mini", false, Options{Source: src, Reload: rl})
	if r := runScript(t, home, nil, "reload", dir); r.code != 3 {
		t.Fatalf("%+v", r)
	}
}

func TestScriptReloadDeclined(t *testing.T) {
	home := shortDir(t)
	dir, src := scriptProject(t, home)
	rl := &fakeReloader{}
	rl.answer(Reloaded{Declined: true}, nil)
	scriptServerWith(t, home, "k3f9q2", "mini", true, Options{Source: src, Reload: rl})
	if r := runScript(t, home, nil, "reload", dir); r.code != 6 || !strings.Contains(r.stdout, `"state":"declined"`) {
		t.Fatalf("%+v", r)
	}
}

// A Nearhost from before the doctor answers GET /v1 without capabilities.
// The skill runs doctor after an add and skips it on exit status 5.
func TestScriptDoctorOnANearhostWithoutCapabilities(t *testing.T) {
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
		if r := runScript(t, home, nil, cmd, home); r.code != 5 || !strings.Contains(r.stderr, "Nearhost on dev@older doesn't support "+cmd) {
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
}

// The script waits for the doctor and a reload longer than the server
// gives them, so it reads the server's answer rather than giving up first.
func TestScriptWaitsForTheDoctorAndReloadLongerThanTheServer(t *testing.T) {
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	for cmd, server := range map[string]time.Duration{"doctor": doctorTimeout, "reload": reloadTimeout} {
		m := regexp.MustCompile(`(?s)\n` + cmd + `\)\n.*?\bcall "\$SOCK" ([0-9]+) `).FindSubmatch(data)
		if m == nil {
			t.Fatalf("no call in %s", cmd)
		}
		secs, _ := strconv.Atoi(string(m[1]))
		if time.Duration(secs)*time.Second <= server {
			t.Errorf("%s: the script waits %ds, the server up to %s", cmd, secs, server)
		}
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
	skill := string(data)
	if regexp.MustCompile("kind `nearhost`, run[^\n]*reload").MatchString(skill) {
		t.Error("the skill reloads for every fix of kind nearhost")
	}
	reload := regexp.MustCompile("(?m)^[0-9]\\. [^\n]*`nearhostrc.pending`[^\n]*`nearhostrc.not-applied`[^\n]*reload .")
	if !reload.MatchString(skill) {
		t.Error("the skill does not reload for nearhostrc.pending and nearhostrc.not-applied")
	}
	add := regexp.MustCompile("(?m)^6\\. On `\"state\":\"accepted\"`.*$").FindString(skill)
	status, doctor := strings.Index(add, "nearhost.sh status"), strings.Index(add, "nearhost.sh doctor .")
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
	shells := []string{"sh"}
	if _, err := exec.LookPath("dash"); err == nil {
		shells = append(shells, "dash")
	}
	home := shortDir(t)
	for _, shell := range shells {
		for flag, want := range map[string]string{"--web": "--web needs a port", "--name": "--name needs a name"} {
			r := runScriptIn(t, shell, home, nil, "propose", flag)
			if r.code != 1 || !strings.Contains(r.stderr, want) {
				t.Fatalf("%s %s: %+v", shell, flag, r)
			}
		}
	}
}

// Without curl the script says so, rather than that Nearhost is not running.
func TestScriptWithoutCurlSaysSo(t *testing.T) {
	shells := []string{"sh"}
	if _, err := exec.LookPath("dash"); err == nil {
		shells = append(shells, "dash")
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
				t.Fatalf("%s %s: %+v", shell, cmd, r)
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

// mode is the permission bits of path.
func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

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
	home := shortDir(t)
	_, p, g, _ := passScript(t, home, "mini", false)
	g.tokens[[2]string{"mini", "claude-code"}] = "tok-x"
	storeToken(t, home, "claude-code", "tok-x")
	r := runScript(t, home, claudeEnv, "passthrough", "3000", "--label", "browser preview")
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
	if m := mode(t, marker); m != 0o600 {
		t.Fatalf("marker mode %v", m)
	}
}

func TestScriptPassthroughPromptThenAlways(t *testing.T) {
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
	r := runScript(t, home, claudeEnv, "passthrough", "3000")
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
	r = runScript(t, home, claudeEnv, "passthrough-wait", id)
	if r.code != 0 || !strings.Contains(r.stdout, `"state":"open"`) || strings.Contains(r.stdout+r.stderr, "tok-1") || !json.Valid([]byte(r.stdout)) {
		t.Fatalf("passthrough-wait: %+v", r)
	}
	tok, err := os.ReadFile(tokenPath(home, "claude-code"))
	if err != nil || strings.TrimSpace(string(tok)) != "tok-1" {
		t.Fatalf("token %q: %v", tok, err)
	}
	if m := mode(t, tokenPath(home, "claude-code")); m != 0o600 {
		t.Fatalf("token mode %v", m)
	}
	for _, dir := range []string{filepath.Dir(tokenPath(home, "claude-code")), tokens, filepath.Dir(tokens)} {
		if m := mode(t, dir); m != 0o700 {
			t.Fatalf("%s mode %v", dir, m)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "state", "nearhost", "sessions", "s1")); err != nil {
		t.Fatalf("no marker: %v", err)
	}
	if got := g.allowed(); len(got) != 1 {
		t.Fatalf("allowed %v", got)
	}
	// The token now opens the next one at once, with no prompt.
	r = runScript(t, home, claudeEnv, "passthrough", "3001")
	if r.code != 0 || !strings.Contains(r.stdout, `"state":"open"`) || len(rv.prompts()) != 1 {
		t.Fatalf("with the token: %+v", r)
	}
}

func TestScriptPassthroughReplyToken(t *testing.T) {
	home := shortDir(t)
	s, _, _, _ := passScript(t, home, "mini", true)
	r := runScript(t, home, claudeEnv, "passthrough", "3000")
	id := proposalID.FindStringSubmatch(r.stdout)[1]
	if err := s.AnswerPassthrough(context.Background(), id, AnswerAlways); err != nil {
		t.Fatal(err)
	}
	// The repeat request is answered with the open record, token included.
	r = runScript(t, home, claudeEnv, "passthrough", "3000")
	if r.code != 0 || !strings.Contains(r.stdout, `"state":"open"`) || strings.Contains(r.stdout+r.stderr, "tok-1") || strings.Contains(r.stdout, "token") {
		t.Fatalf("repeat: %+v", r)
	}
	if json.Valid([]byte(r.stdout)) == false {
		t.Fatalf("not JSON: %q", r.stdout)
	}
	if tok, err := os.ReadFile(tokenPath(home, "claude-code")); err != nil || strings.TrimSpace(string(tok)) != "tok-1" {
		t.Fatalf("token %q: %v", tok, err)
	}
}

func TestScriptPassthroughDeclinedExpired(t *testing.T) {
	home := shortDir(t)
	s, c := scriptServerWith(t, home, "k3f9q2", "mini", true, Options{ID: "lid", Source: fakeSource{"mini": mini},
		Passer: &fakePasser{live: map[string]Passthrough{}, taken: map[int]bool{}}, Grants: &fakeGrants{tokens: map[[2]string]string{}}})
	r := runScript(t, home, claudeEnv, "passthrough", "3000")
	id := proposalID.FindStringSubmatch(r.stdout)[1]
	if err := s.AnswerPassthrough(context.Background(), id, AnswerNo); err != nil {
		t.Fatal(err)
	}
	r = runScript(t, home, claudeEnv, "passthrough-wait", id)
	if r.code != 6 || !strings.Contains(r.stdout, `"state":"declined"`) || strings.Contains(r.stdout, `"reason"`) || r.stderr != "" {
		t.Fatalf("declined: %+v", r)
	}
	r = runScript(t, home, claudeEnv, "passthrough", "3001")
	id = proposalID.FindStringSubmatch(r.stdout)[1]
	c.add(11 * time.Minute)
	r = runScript(t, home, claudeEnv, "passthrough-wait", id)
	if r.code != 7 || !strings.Contains(r.stdout, `"state":"expired"`) {
		t.Fatalf("expired: %+v", r)
	}
}

func TestScriptPassthroughOpenFailedIsNoDecline(t *testing.T) {
	home := shortDir(t)
	s, p, _, _ := passScript(t, home, "mini", true)
	p.err = errors.New(`ssh: listen tcp 127.0.0.1:3000, "bind": address already in use`)
	r := runScript(t, home, claudeEnv, "passthrough", "3000")
	id := proposalID.FindStringSubmatch(r.stdout)[1]
	if err := s.AnswerPassthrough(context.Background(), id, AnswerYes); err == nil {
		t.Fatal("the open did not fail")
	}
	r = runScript(t, home, claudeEnv, "passthrough-wait", id)
	want := `Nearhost could not open the port: ssh: listen tcp 127.0.0.1:3000, "bind": address already in use`
	if r.code != 1 || !strings.Contains(r.stdout, `"state":"declined"`) || strings.TrimSpace(r.stderr) != want {
		t.Fatalf("failed open: %+v", r)
	}
}

func TestScriptPassthroughTokenNotInArguments(t *testing.T) {
	home := shortDir(t)
	_, _, g, _ := passScript(t, home, "mini", false)
	g.tokens[[2]string{"mini", "claude-code"}] = "tok-secret"
	storeToken(t, home, "claude-code", "tok-secret")
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
	r := runScript(t, home, append([]string{"PATH=" + bin + ":/usr/bin:/bin"}, claudeEnv...), "passthrough", "3000")
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
}

func TestScriptPassthroughRevokedToken(t *testing.T) {
	home := shortDir(t)
	_, _, _, rv := passScript(t, home, "mini", true)
	storeToken(t, home, "claude-code", "revoked")
	posts, front := countPosts(t, home)
	r := runScript(t, home, append([]string{"NEARHOST_SOCKET=" + front}, claudeEnv...), "passthrough", "3000")
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
}

func TestScriptPassthroughAgent(t *testing.T) {
	home := shortDir(t)
	s, p, g, _ := passScript(t, home, "mini", true)
	g.tokens[[2]string{"mini", "agent"}] = "tok-agent"
	storeToken(t, home, "agent", "tok-agent")
	r := runScript(t, home, nil, "passthrough", "3000")
	m := proposalID.FindStringSubmatch(r.stdout)
	if r.code != 0 || m == nil || strings.Contains(r.stdout, `"state"`) {
		t.Fatalf("a token was sent: %+v", r)
	}
	if err := s.AnswerPassthrough(context.Background(), m[1], AnswerYes); err != nil {
		t.Fatal(err)
	}
	r = runScript(t, home, nil, "passthrough-wait", m[1])
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
	home := shortDir(t)
	scriptServer(t, home, "k3f9q2", "mini", true)
	host, _ := os.Hostname()
	host, _, _ = strings.Cut(host, ".")
	r := runScript(t, home, nil, "passthrough", "3000")
	want := "Nearhost on dev@k3f9q2 is too old for passthrough; update it there. Meanwhile, forward it yourself: ssh -N -L 3000:localhost:3000 " + host + "\n"
	if r.code != 5 || r.stderr != want {
		t.Fatalf("%+v, want %q", r, want)
	}
}

func TestScriptPassthroughCloseSession(t *testing.T) {
	home := shortDir(t)
	_, p, g, _ := passScript(t, home, "mini", false)
	g.tokens[[2]string{"mini", "claude-code"}] = "tok-x"
	storeToken(t, home, "claude-code", "tok-x")
	r := runScript(t, home, claudeEnv, "passthrough", "3000")
	if r.code != 0 {
		t.Fatalf("passthrough: %+v", r)
	}
	marker := filepath.Join(home, ".local", "state", "nearhost", "sessions", "s1")
	r = runScript(t, home, nil, "passthrough-close", "--session", "s1")
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
	start := time.Now()
	r = runScript(t, home, nil, "passthrough-close", "--session", "none")
	mu.Lock()
	defer mu.Unlock()
	if r.code != 0 || time.Since(start) > 1500*time.Millisecond || dials != 0 || len(p.closed()) != 1 {
		t.Fatalf("no marker: %+v after %v, %d dials", r, time.Since(start), dials)
	}
}

func TestScriptPassthroughCloseByID(t *testing.T) {
	home := shortDir(t)
	s, p, _, _ := passScript(t, home, "mini", true)
	r := runScript(t, home, nil, "passthrough", "3000")
	id := proposalID.FindStringSubmatch(r.stdout)[1]
	if err := s.AnswerPassthrough(context.Background(), id, AnswerYes); err != nil {
		t.Fatal(err)
	}
	r = runScript(t, home, nil, "passthrough-close", id)
	if r.code != 0 || len(p.closed()) != 1 || p.closed()[0] != id {
		t.Fatalf("close: %+v, closed %v", r, p.closed())
	}
	// An id nothing knows still exits 0.
	if r = runScript(t, home, nil, "passthrough-close", "ffff"); r.code != 0 {
		t.Fatalf("unknown id: %+v", r)
	}
}

func TestScriptPassthroughXDGStateHome(t *testing.T) {
	home := shortDir(t)
	state := filepath.Join(shortDir(t), "xdg")
	_, _, g, _ := passScript(t, home, "mini", false)
	g.tokens[[2]string{"mini", "claude-code"}] = "tok-x"
	path := filepath.Join(state, "nearhost", "tokens", "lid", "claude-code")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte("tok-x\n"), 0o600)
	env := append([]string{"XDG_STATE_HOME=" + state}, claudeEnv...)
	r := runScript(t, home, env, "passthrough", "3000")
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
	r = runScript(t, home, env, "passthrough-close", "--session", "s1")
	if r.code != 0 {
		t.Fatalf("close: %+v", r)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker stayed: %v", err)
	}
}

// countPosts proxies home's socket k3f9q2 through a second socket the
// script finds instead, and returns a func counting the POSTs to
// /v1/passthroughs that went through it.
func countPosts(t *testing.T, home string) (posts func() int, front string) {
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
		if r.Method == "POST" && r.URL.Path == "/v1/passthroughs" {
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
	home := shortDir(t)
	s, _, _, _ := passScript(t, home, "mini", true)
	r := runScript(t, home, claudeEnv, "passthrough", "3000")
	id := proposalID.FindStringSubmatch(r.stdout)[1]
	marker := filepath.Join(home, ".local", "state", "nearhost", "sessions", "s1")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("no marker while pending: %v", err)
	}
	if r = runScript(t, home, nil, "passthrough-close", "--session", "s1"); r.code != 0 {
		t.Fatalf("close: %+v", r)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker stayed: %v", err)
	}
	if err := s.AnswerPassthrough(context.Background(), id, AnswerYes); err == nil {
		t.Fatal("the request was still pending after its session closed")
	}
}

func TestScriptPassthroughNotConnectedAnswer(t *testing.T) {
	home := shortDir(t)
	_, p, g, _ := passScript(t, home, "mini", false)
	p.err = fmt.Errorf("dial: %w", ErrNotConnected)
	g.tokens[[2]string{"mini", "claude-code"}] = "tok-x"
	storeToken(t, home, "claude-code", "tok-x")
	host, _ := os.Hostname()
	host, _, _ = strings.Cut(host, ".")
	r := runScript(t, home, claudeEnv, "passthrough", "3000")
	want := "Nearhost isn't connected to this server. On your laptop, select " + host + " in Nearhost and press c, then ask again. Or forward it yourself: ssh -N -L 3000:localhost:3000 " + host + "\n"
	if r.code != 2 || r.stderr != want {
		t.Fatalf("%+v, want %q", r, want)
	}
}

func TestScriptPassthroughBadPort(t *testing.T) {
	home := shortDir(t)
	passScript(t, home, "mini", true)
	for _, port := range []string{"03000", "0", "70000", "abc"} {
		if r := runScript(t, home, claudeEnv, "passthrough", port); r.code != 1 || r.stdout != "" {
			t.Errorf("port %s: %+v", port, r)
		}
	}
}
