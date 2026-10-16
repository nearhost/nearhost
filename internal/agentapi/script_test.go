package agentapi

import (
	"bytes"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
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
	path, err := filepath.Abs(script)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", append([]string{path}, args...)...)
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
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed")
	}
	c := &clock{now: time.Now()}
	s := New(Options{Laptop: "ecs@" + id, Source: src, Now: c.Now})
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
	if real, _ := filepath.EvalSymlinks(app); p.Path != real || p.Web != 5173 || p.Name != `the "app"` {
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
	if r := runScript(t, home, nil, "propose", home); r.code != 3 || !strings.Contains(r.stdout, "open nearhost on ecs@k3f9q2") {
		t.Fatalf("%+v", r)
	}
}

func TestScriptAsksWhichLaptopWhenSeveralAnswer(t *testing.T) {
	home := shortDir(t)
	scriptServer(t, home, "aaaaaa", "mini", true)
	scriptServer(t, home, "bbbbbb", "mini", true)
	r := runScript(t, home, nil, "socket")
	if r.code != 4 || !strings.Contains(r.stderr, "aaaaaa.sock  (ecs@aaaaaa)") || !strings.Contains(r.stderr, "bbbbbb.sock  (ecs@bbbbbb)") {
		t.Fatalf("%+v", r)
	}
	chosen := filepath.Join(home, ".nearhost", "bbbbbb.sock")
	if r := runScript(t, home, []string{"NEARHOST_SOCKET=" + chosen}, "socket"); r.code != 0 || !strings.Contains(r.stdout, `"laptop":"ecs@bbbbbb"`) {
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
		w.Write([]byte(`{"api":2,"interface":true,"laptop":"ecs@future","origin":"mini"}`))
	})}
	go srv.Serve(l)
	defer srv.Close()
	if r := runScript(t, home, nil, "socket"); r.code != 5 || !strings.Contains(r.stderr, "update the nearhost plugin") {
		t.Fatalf("%+v", r)
	}
}
