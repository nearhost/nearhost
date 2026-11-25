package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/report"
)

// mainStderr runs Main with args and returns its exit code and what it
// wrote to standard error.
func mainStderr(t *testing.T, args ...string) (int, string) {
	t.Helper()
	noCleanOld(t)
	path := filepath.Join(t.TempDir(), "stderr")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = f
	code := Main(args)
	os.Stderr = stderr
	f.Close()
	got, _ := os.ReadFile(path)
	return code, string(got)
}

// With --json, a command's error goes to standard error as one JSON object
// on a line of its own, in the shape the APIs nest messages in, and its
// exit status stays.
func TestJSONCommandErrorsAreJSONLines(t *testing.T) {
	configHome(t)
	code, got := mainStderr(t, "status", "--json")
	if code != statusNotRunningCode {
		t.Errorf("exit %d", code)
	}
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 1 || !strings.HasSuffix(got, "\n") {
		t.Fatalf("stderr %q, want one JSON line", got)
	}
	var m report.Message
	if err := json.Unmarshal([]byte(lines[0]), &m); err != nil {
		t.Fatalf("stderr %q: %v", got, err)
	}
	if m.Code != report.CoreNotRunning || m.Level != report.Error || m.Title != "Nearhost is not running." || len(m.Fix) != 2 {
		t.Errorf("message %+v", m)
	}
	if strings.Contains(lines[0], `"error":`) {
		t.Errorf("a nested message has an error key: %s", lines[0])
	}
}

// Without --json, the same error is rendered for a terminal; under
// --verbose its cause, which says what the title already says, is not
// repeated.
func TestStatusNotRunningVerboseHasNoRepeatedCause(t *testing.T) {
	configHome(t)
	code, got := mainStderr(t, "status", "--verbose")
	if code != statusNotRunningCode || !strings.HasPrefix(got, "Error: Nearhost is not running.\n(core.not-running)\n") {
		t.Fatalf("exit %d, stderr %q", code, got)
	}
	if strings.Contains(got, "Cause:") {
		t.Errorf("the cause repeats the title:\n%s", got)
	}
}

// Messages written one after another to standard error have a blank line
// between them, for a terminal; as JSON each is a line of its own.
func TestStderrMessagesAreSpaced(t *testing.T) {
	warn := report.Warnf(report.HostsCoreNotTold, "First.")
	fail := report.Errorf(report.Unexpected, "Second.")
	var b bytes.Buffer
	o := &messageOut{writer: func() io.Writer { return &b }}
	o.Report(warn)
	if code := o.exit(fail); code != 1 {
		t.Errorf("exit %d", code)
	}
	o.Report(report.Notef(report.UpdateAvailable, "Third."))
	if want := "Warning: First.\n\nError: Second.\n\nNote: Third.\n"; b.String() != want {
		t.Errorf("stderr %q, want %q", b.String(), want)
	}

	b.Reset()
	o = &messageOut{writer: func() io.Writer { return &b }, json: true}
	o.Report(warn)
	o.exit(fail)
	lines := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"title":"First."`) || !strings.Contains(lines[1], `"title":"Second."`) {
		t.Errorf("stderr %q, want two JSON lines", b.String())
	}

	// An error already said is not said again, and adds no blank line.
	b.Reset()
	o = &messageOut{writer: func() io.Writer { return &b }}
	o.Report(warn)
	o.exit(alreadySaid{fail})
	if b.String() != "Warning: First.\n" {
		t.Errorf("stderr %q", b.String())
	}
}

// A command's sink and Main share one writer for standard error, so a
// warning the command reported and the error it returned are spaced too.
func TestCommandSinkIsMainsWriter(t *testing.T) {
	root := newRootCmd()
	var stderr bytes.Buffer
	root.SetErr(&stderr)
	root.SetOut(io.Discard)
	root.RunE = func(cmd *cobra.Command, args []string) error {
		sinkOf(cmd).Report(report.Warnf(report.HostsCoreNotTold, "Warned."))
		return errors.New("failed")
	}
	root.SetArgs(nil)
	cmd, err := root.ExecuteC()
	out, ok := sinkIn(cmd).(*messageOut)
	if !ok {
		t.Fatalf("the command's sink is %T", sinkIn(cmd))
	}
	out.exit(err)
	if want := "Warning: Warned.\n\nError: failed\n"; stderr.String() != want {
		t.Errorf("stderr %q, want %q", stderr.String(), want)
	}
}

// Every argument a command does not take is a cli.usage message: an
// unknown command, with what it may have meant, and a wrong count of
// arguments or a bad flag with how to see what the command takes.
func TestUnknownArgumentsAreUsageMessages(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		title string
		step  report.Step
	}{
		{[]string{"bogus"}, `Unknown command "bogus" for "nearhost".`, report.Step{}},
		{[]string{"bogus", "more"}, `Unknown command "bogus" for "nearhost".`, report.Step{}},
		{[]string{"service", "bogus"}, `Unknown command "bogus" for "nearhost service".`, report.Step{}},
		{[]string{"service", "instal"}, `Unknown command "instal" for "nearhost service".`, report.Step{Text: "Did you mean this?", Command: "install"}},
		{[]string{"completion", "bogus"}, `Unknown command "bogus" for "nearhost completion".`, report.Step{}},
		{[]string{"version", "extra"}, `Unknown command "extra" for "nearhost version".`, report.Step{}},
		{[]string{"preset", "registry", "remove"}, "Accepts 1 arg(s), received 0.", report.Step{Text: "See what it takes:", Command: "nearhost preset registry remove --help"}},
		{[]string{"ls", "--bogus"}, "Unknown flag: --bogus.", report.Step{Text: "See what it takes:", Command: "nearhost ls --help"}},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			configHome(t)
			root := NewRoot()
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			root.SetArgs(tc.args)
			err := root.Execute()
			m := messageIn(t, err)
			if m.Code != report.CLIUsage || m.Title != tc.title {
				t.Errorf("code %q, title %q, want %q", m.Code, m.Title, tc.title)
			}
			if tc.step != (report.Step{}) {
				wantSteps(t, m, tc.step)
			} else if len(m.Fix) != 0 {
				t.Errorf("steps %+v, want none", m.Fix)
			}
			if exitCode(err, io.Discard) != 1 {
				t.Error("exit status is not 1")
			}
		})
	}
}

// A bad port flag is a cli.usage message.
func TestServeFlagsAreUsageMessages(t *testing.T) {
	f := serveFlags{httpsPort: 443, httpPort: 443, webPort: defaultWebPort}
	m := messageIn(t, f.validate())
	if m.Code != report.CLIUsage || m.Title != "--https-port and --http-port must differ (both are 443)." {
		t.Errorf("code %q, title %q", m.Code, m.Title)
	}
}

// The core's refusal to update the hosts file says how to update it from
// a terminal, as a step with the command on its own, and the project's
// problem adds no step to start it again: the core does that by itself.
func TestHostsNeedTerminalHasACommandStep(t *testing.T) {
	wantSteps(t, errHostsNeedTerminal, report.Step{Text: "Update it from a terminal:", Command: "nearhost hosts update"})
	m := engine.HostsProblem("web", errHostsNeedTerminal)
	wantSteps(t, m, report.Step{Text: "Update it from a terminal:", Command: "nearhost hosts update"})
	if !strings.Contains(m.Title, errHostsNeedTerminal.Title) {
		t.Errorf("title %q lost the refusal %q, which the interface replaces", m.Title, errHostsNeedTerminal.Title)
	}
	// The command is the step's alone, not written in the title too.
	if want := "the hosts file needs updating, and Nearhost in the background never asks for sudo."; errHostsNeedTerminal.Title != want {
		t.Errorf("title %q, want %q", errHostsNeedTerminal.Title, want)
	}
	// The interface knows the refusal by its code and step, not its text.
	b := &clientBackend{mirror: newMirror(control.State{Projects: map[string]engine.ProjectStatus{
		"web":   {State: engine.StateStopped, Message: "reworded", Problem: m},
		"other": {State: engine.StateStopped, Message: m.Title, Problem: engine.HostsProblem("other", errors.New("sudo: no"))},
	}})}
	if !b.waitsOnHosts("web") || b.waitsOnHosts("other") {
		t.Errorf("waits on hosts: web %v, other %v; want true, false", b.waitsOnHosts("web"), b.waitsOnHosts("other"))
	}
}

// add's refusals of a taken name and a missing directory have codes and
// steps with the command on its own.
func TestAddTakenAndMissingAreMessages(t *testing.T) {
	cfgPath, _ := existing(t)
	dir := t.TempDir()
	_, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", dir, "--name", "shop", "--web", "9000")
	m := messageIn(t, err)
	if m.Code != report.AddNameTaken || m.Title != `A project called "shop" already exists.` {
		t.Errorf("code %q, title %q", m.Code, m.Title)
	}
	if len(m.Fix) != 1 || m.Fix[0].Text != "Add this one under another name:" ||
		!strings.HasPrefix(m.Fix[0].Command, "nearhost add ") || !strings.HasSuffix(m.Fix[0].Command, " --name <name>") {
		t.Errorf("steps %+v", m.Fix)
	}

	_, err = execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", dir, "--name", "other", "--alias", "shop.test", "--web", "9000")
	m = messageIn(t, err)
	if m.Code != report.AddAliasTaken || m.Title != `Alias shop.test is already used by project "shop".` {
		t.Errorf("code %q, title %q", m.Code, m.Title)
	}
	if len(m.Fix) != 1 || !strings.HasSuffix(m.Fix[0].Command, " --alias <domain>") {
		t.Errorf("steps %+v", m.Fix)
	}

	missing := filepath.Join(t.TempDir(), "missing")
	_, err = execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", missing)
	m = messageIn(t, err)
	if m.Code != report.AddNoDirectory || m.Title != "There is no directory at "+missing+"." {
		t.Errorf("code %q, title %q", m.Code, m.Title)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", file)
	if m := messageIn(t, err); m.Code != report.AddNoDirectory || m.Title != file+" is not a directory." {
		t.Errorf("code %q, title %q", m.Code, m.Title)
	}
}

// With --json, a message on standard error, which is usually a terminal,
// carries no escape sequence, C1 control, DEL or bidi override, raw or
// decoded, in any of its text, and keeps the lines of its explanation.
func TestJSONStderrMessagesWithoutControlCharacters(t *testing.T) {
	const hostile = "a\x1b[31mb\x7fc\u009b31md‮e"
	m := report.Errorf(report.Unexpected, "Title %s", hostile)
	m.Context = "start " + hostile
	m.Why = "first\n" + hostile
	m.Verbatim = []string{"log " + hostile}
	m.Fix = []report.Step{report.Step{Text: "Do " + hostile, Command: "nearhost " + hostile}}
	m.Data = map[string]any{"name": hostile, "list": []any{hostile}}
	var b bytes.Buffer
	o := &messageOut{writer: func() io.Writer { return &b }, json: true}
	o.Report(m)
	raw := b.String()
	if strings.ContainsAny(raw, "\x1b\x7f\u009b‮") {
		t.Errorf("the JSON line carries control characters: %q", raw)
	}
	var got report.Message
	if err := json.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatalf("%v: %q", err, raw)
	}
	decoded := got.Title + got.Context + got.Why + strings.Join(got.Verbatim, "") + got.Fix[0].Text + got.Fix[0].Command
	if strings.ContainsAny(decoded, "\x1b\x7f\u009b‮") {
		t.Errorf("the decoded message carries control characters: %q", decoded)
	}
	if !strings.HasPrefix(got.Why, "first\n") {
		t.Errorf("the explanation lost its lines: %q", got.Why)
	}
	if got.Title != "Title a[31mbc31mde" {
		t.Errorf("title %q", got.Title)
	}
}

// add's refusals of a relative path on a server, a path it cannot name a
// project after and a domain it cannot serve have codes, and steps with the
// command on its own line, not written into the sentence.
func TestAddPathNameAndAliasRefusalsAreMessages(t *testing.T) {
	cfg := &config.Config{Servers: mini}
	again := func(set ...string) string { return "nearhost add /srv/app " + strings.Join(set, " ") }
	for _, tc := range []struct {
		name  string
		flags addFlags
		args  []string
		code  report.Code
		title string
		step  report.Step
	}{
		{"relative", addFlags{}, []string{"srv/app"}, report.AddRelativePath,
			"A project on a server needs its absolute path there, or one under ~/.",
			report.Step{Text: "Add it with its path on the server:", Command: "nearhost add /path/on/server --server mini"}},
		{"name", addFlags{alias: "app.test"}, []string{"/"}, report.AddBadName,
			"Nearhost cannot name a project after /.",
			report.Step{Text: "Pass a name for it:", Command: "nearhost add /srv/app --name <name>"}},
		{"alias", addFlags{alias: "localhost"}, []string{"/srv/app"}, report.AddBadAlias,
			`Invalid main name "localhost": must be a lowercase domain name, not a wildcard, localhost or an IP address.`,
			report.Step{Text: "Pass another domain to serve it at:", Command: "nearhost add /srv/app --alias <domain>"}},
	} {
		_, err := newProject(cfg, "config.yaml", mini[0], tc.flags, tc.args, again)
		m, ok := report.As(err)
		if !ok || m.Code != tc.code || m.Title != tc.title {
			t.Errorf("%s: %v, message %+v", tc.name, err, m)
			continue
		}
		wantSteps(t, m, tc.step)
		if strings.Contains(m.Title, "nearhost") || strings.Contains(m.Title, "--") {
			t.Errorf("%s: the title names a command: %q", tc.name, m.Title)
		}
	}
}

// A name given with --name that no project can have is blamed, not the
// path, which was never asked to name the project.
func TestAddBadNameFromTheFlagNamesTheName(t *testing.T) {
	cfg := &config.Config{Servers: mini}
	again := func(set ...string) string { return "nearhost add /srv/app " + strings.Join(set, " ") }
	_, err := newProject(cfg, "config.yaml", mini[0], addFlags{name: "a/b", alias: "app.test"}, []string{"/srv/app"}, again)
	m, ok := report.As(err)
	if !ok || m.Code != report.AddBadName || m.Title != `The name "a/b" is not one a project can have.` || m.Data["name"] != "a/b" {
		t.Fatalf("%v, message %+v", err, m)
	}
	wantSteps(t, m, report.Step{Text: "Pass another name:", Command: "nearhost add /srv/app --name <name>"})
}
