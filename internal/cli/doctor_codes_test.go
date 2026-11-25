package cli

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/doctor"
	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/service"
	"github.com/nearhost/nearhost/internal/testutil"
	"github.com/nearhost/nearhost/internal/transport"
)

// doctorRows runs doctor --json with b and returns its checks by name, as
// JSON objects.
func doctorRows(t *testing.T, b doctorBackend) (map[string]map[string]any, []map[string]any) {
	t.Helper()
	stdout, _, _ := runSplit(t, b, "doctor", "--json")
	var got struct {
		Checks   []map[string]any `json:"checks"`
		Findings []map[string]any `json:"findings"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, stdout)
	}
	rows := map[string]map[string]any{}
	for _, c := range got.Checks {
		rows[c["name"].(string)] = c
	}
	return rows, got.Findings
}

// steps reads a row's or a finding's steps back as report.Steps.
func steps(t *testing.T, v any) []report.Step {
	t.Helper()
	b, _ := json.Marshal(v)
	var s []report.Step
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("steps %s: %v", b, err)
	}
	return s
}

// The error nearhost doctor predicts and the row that predicts it carry the
// same code, so "run nearhost doctor" leads to the matching row, and the
// same step.
func TestDoctorRowSharesTheErrorsCode(t *testing.T) {
	const exe = "/home/me/.local/bin/nearhost"
	u := newUpRun(t, &config.Config{Projects: []config.Project{localProject("web", testutil.Upstream(t, "web"), true)}})
	perm := deniedPortErr("HTTPS port 443 needs permission to bind", permissionError())
	_, upErr := execute(t, newUpCmd(setcapBackend(u, perm, exe)), u.args()...)
	m, ok := report.As(upErr)
	if !ok || m.Code != report.PortNeedsPermission {
		t.Fatalf("up's error %v is not %s", upErr, report.PortNeedsPermission)
	}

	doctorConfig(t)
	f := healthy()
	f.goos, f.exe = "linux", exe
	f.lowLevel, f.lowDetail = levelFail, "not root and the binary lacks cap_net_bind_service"
	f.portErr = map[string]error{"tcp 443": syscall.EACCES, "udp 443": syscall.EACCES, "tcp 80": syscall.EACCES}
	rows, _ := doctorRows(t, f.backend())
	for _, name := range []string{"privileged ports", "port 443", "port 80"} {
		row := rows[name]
		if row["code"] != string(m.Code) {
			t.Errorf("%s: code %v, want up's %s", name, row["code"], m.Code)
		}
		if got := steps(t, row["steps"]); len(got) != 1 || got[0] != m.Fix[0] {
			t.Errorf("%s: steps %+v, want up's %+v", name, got, m.Fix[0])
		}
		// The fix agents read is today's.
		if row["fix"] != "`"+doctor.SetcapFix(exe)+"`" {
			t.Errorf("%s: fix %v", name, row["fix"])
		}
	}
}

// Each row that predicts an error has that error's code.
func TestDoctorRowCodes(t *testing.T) {
	doctorConfig(t)
	f := healthy()
	f.hosts = "127.0.0.1 localhost\n" // the block is missing
	f.serverErr = map[string]error{"mini": &transport.CheckError{Host: "mini.lan", Stderr: "Host key verification failed."}}
	b := f.backend()
	rows, _ := doctorRows(t, b)
	for name, want := range map[string]report.Code{
		"hosts block": report.HostsNeedsUpdate,
		"server mini": report.SSHUnreachable,
		"port 443":    "",
	} {
		if got, _ := rows[name]["code"].(string); got != string(want) {
			t.Errorf("%s: code %q, want %q", name, got, want)
		}
	}
	if got := steps(t, rows["server mini"]["steps"]); len(got) != 2 || got[0] != sshStep("mini.lan") {
		t.Errorf("server steps %+v", got)
	}

	for _, tc := range []struct {
		err  error
		want report.Code
	}{
		{&transport.AgentError{Problem: transport.AgentNotReady, Msg: "could not ready ~/.nearhost"}, report.AgentsNotReady},
		{&transport.AgentError{Problem: transport.AgentTooLong, Msg: "too long"}, report.AgentsPathTooLong},
		{&transport.AgentError{Msg: "refused"}, report.AgentsRefused},
		{errors.New("refused"), report.AgentsRefused},
	} {
		f := healthy()
		b := f.backend()
		b.checkAgents = func(context.Context, config.Server) error { return tc.err }
		rows, _ := doctorRows(t, b)
		if got := rows["agents on mini"]["code"]; got != string(tc.want) {
			t.Errorf("%v: agents code %v, want %s", tc.err, got, tc.want)
		}
	}
}

func TestDoctorCoreRowCodes(t *testing.T) {
	m := newCoreMachine(t)
	info := thisCore(control.StartedDetached)
	info.API = control.APIVersion + 1
	m.answer(t, info, nil, nil)
	doctorConfig(t)
	rows, _ := doctorRows(t, m.backend())
	if got := rows["core"]["code"]; got != string(report.CoreAPIMismatch) {
		t.Errorf("core code %v, want %s", got, report.CoreAPIMismatch)
	}

	// A check the core makes of itself keeps the code and steps it sent.
	m = newCoreMachine(t)
	problem := report.Warnf(report.CoreNoDesktop, "Nearhost in the background cannot open links.").
		Step("Install the service:", "nearhost service install").
		Step("Then stop Nearhost:", "nearhost stop")
	m.answer(t, thisCore(control.StartedDetached), []control.EnvCheck{
		{Name: "browser", Level: "warn", Detail: "no desktop", Fix: "run nearhost service install", Problem: problem},
	}, nil)
	rows, _ = doctorRows(t, m.backend())
	row := rows["core: browser"]
	if row["code"] != string(report.CoreNoDesktop) || !reflect.DeepEqual(steps(t, row["steps"]), problem.Fix) || row["fix"] != "Run nearhost service install" {
		t.Errorf("core: browser %v", row)
	}
	// In the terminal, a row of several steps shows them under its fix.
	out, err := execute(t, newDoctorCmd(m.backend()), "doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	wantContains(t, out, "warn  core: browser    no desktop\n"+
		"      fix: Install the service:\n"+
		"             nearhost service install\n"+
		"           Then stop Nearhost:\n"+
		"             nearhost stop\n")
}

// The service row of a system with no user manager shows its step, as other
// rows show theirs.
func TestDoctorNoUserManagerRowShowsItsStep(t *testing.T) {
	m := newCoreMachine(t)
	b := m.backend()
	b.service = func() service.Manager { return &fakeManager{instErr: service.ErrNoUserManager} }
	out, err := m.doctor(t, b)
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	wantContains(t, out, "info  service          "+messageOf(service.ErrNoUserManager).Title+"\n"+
		"      fix: Start Nearhost from your shell's startup instead: `nearhost -d`\n")
	rows, _ := doctorRows(t, b)
	if rows["service"]["code"] != string(report.ServiceNoUserManager) {
		t.Errorf("service row %v", rows["service"])
	}
}

// --json keeps every key agents read today, with today's values, and adds
// the message's: a check gains code and steps, a finding code, title and
// steps.
func TestDoctorJSONKeepsOldFields(t *testing.T) {
	shopAndBlog(t)
	f := healthy()
	f.hosts = shopAndBlogHosts
	f.trust.nss = proxy.NSSStatus{State: proxy.NSSMissing}
	f.portErr = map[string]error{"tcp 80": syscall.EADDRINUSE}
	var opened []string
	rows, findings := doctorRows(t, withProjects(f.backend(), nil, &opened))

	port := rows["port 80"]
	if port["name"] != "port 80" || port["level"] != "fail" || port["detail"] != "address already in use" ||
		port["fix"] != "Stop the process using it (expected while `nearhost up` is running), or give `nearhost up` another --http-port" {
		t.Errorf("port 80 lost a field's value: %v", port)
	}
	if port["code"] != string(report.PortInUse) {
		t.Errorf("port 80 code %v", port["code"])
	}
	if got := steps(t, port["steps"]); len(got) != 1 || got[0].Text != port["fix"] {
		t.Errorf("port 80 steps %+v", got)
	}
	ok := rows["port 443"]
	if ok["level"] != "ok" || ok["detail"] != "free" || ok["fix"] != "" {
		t.Errorf("port 443 %v", ok)
	}
	if _, has := ok["code"]; has {
		t.Errorf("a passing check has a code: %v", ok)
	}
	if rows["browser stores"]["code"] != string(report.CABrowsersNotTrusted) {
		t.Errorf("browser stores %v", rows["browser stores"])
	}

	if len(findings) != 1 {
		t.Fatalf("findings %v", findings)
	}
	want := map[string]any{
		"id": "nearhostrc.invalid", "severity": "error", "project": "shop",
		"message": `.nearhostrc: line 3: key "extra" is not understood by this version of Nearhost; update Nearhost, or remove it`,
		"file":    ".nearhostrc", "line": float64(3),
		"fix":   map[string]any{"kind": "project", "text": "Correct .nearhostrc."},
		"code":  "nearhostrc.invalid",
		"title": `.nearhostrc: line 3: key "extra" is not understood by this version of Nearhost; update Nearhost, or remove it.`,
		"steps": []any{map[string]any{"text": "Correct .nearhostrc."}},
	}
	if !reflect.DeepEqual(findings[0], want) {
		t.Errorf("finding %v\nwant %v", findings[0], want)
	}
	catalogued := map[string]bool{}
	for _, e := range report.Catalog() {
		catalogued[string(e.Code)] = true
	}
	for _, c := range rows {
		for _, k := range []string{"name", "level", "detail", "fix"} {
			if _, has := c[k]; !has {
				t.Errorf("check %v has no %q", c["name"], k)
			}
		}
		if strings.ToLower(c["level"].(string)) != c["level"] {
			t.Errorf("level %v is not lower case", c["level"])
		}
		// Every row that did not pass is a message, with a code of the
		// catalog.
		if code, _ := c["code"].(string); c["level"] != "ok" && !catalogued[code] {
			t.Errorf("check %v has the code %q, which is not in the catalog", c["name"], code)
		}
	}
}
