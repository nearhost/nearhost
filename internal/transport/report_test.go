package transport

import (
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/report"
)

// Each kind of agent problem is a message with its own code and the step
// that fixes it.
func TestAgentErrorCodes(t *testing.T) {
	for _, tc := range []struct {
		problem AgentProblem
		code    report.Code
		step    string
	}{
		{AgentNotReady, report.AgentsNotReady, "Check that the server account can write ~/.nearhost, then press r on the server to reconnect."},
		{AgentRefused, report.AgentsRefused, "Set AllowStreamLocalForwarding yes in the server's sshd_config, then press r on the server to reconnect."},
		{AgentTooLong, report.AgentsPathTooLong, "The server account's home directory is too long for the socket, so agents there cannot reach Nearhost."},
	} {
		err := &AgentError{Problem: tc.problem, Msg: "it failed"}
		m, ok := report.As(err)
		if !ok {
			t.Fatalf("%v: no message", tc.problem)
		}
		if m.Code != tc.code || m.Title != "it failed" {
			t.Errorf("%v: message %q %q, want %q with the error's text", tc.problem, m.Code, m.Title, tc.code)
		}
		if len(m.Fix) != 1 || m.Fix[0].Text != tc.step {
			t.Errorf("%v: fix = %+v, want %q", tc.problem, m.Fix, tc.step)
		}
		if err.Error() != "it failed" {
			t.Errorf("%v: Error() = %q", tc.problem, err.Error())
		}
	}
}

// Health carries the agent problem as a message beside its text.
func TestHealthCarriesAgentReport(t *testing.T) {
	var v supervision
	v.init("mini", "mini.lan")
	v.status = StatusConnected
	v.agentErr, v.agentProblem = "make ~/.nearhost: permission denied", AgentNotReady
	h := v.Health()
	if h.AgentReport == nil || h.AgentReport.Code != report.AgentsNotReady || h.AgentReport.Title != h.AgentErr {
		t.Fatalf("AgentReport = %+v, want %q titled %q", h.AgentReport, report.AgentsNotReady, h.AgentErr)
	}
	if h.Problem != nil {
		t.Errorf("a connected session has the problem %+v", h.Problem)
	}
	v.agentErr, v.agentProblem = "", AgentOK
	if h := v.Health(); h.AgentReport != nil {
		t.Errorf("AgentReport = %+v with no agent problem", h.AgentReport)
	}
}

// A session that cannot connect has a problem whose code says why, and
// whose title is its last error.
func TestHealthCarriesConnectionProblem(t *testing.T) {
	var v supervision
	v.init("mini", "mini.lan")
	if h := v.Health(); h.Problem != nil {
		t.Fatalf("a session that has not tried has the problem %+v", h.Problem)
	}
	v.retrying("ssh: connect to host mini.lan port 22: Connection refused", 0)
	h := v.Health()
	if h.Problem == nil || h.Problem.Code != report.SSHUnreachable || h.Problem.Title != h.LastError {
		t.Fatalf("reconnecting: Problem = %+v, want %q titled %q", h.Problem, report.SSHUnreachable, h.LastError)
	}
	if h.Problem.Data["server"] != "mini" || h.Problem.Data["host"] != "mini.lan" {
		t.Errorf("data = %v", h.Problem.Data)
	}
	v.giveUp("Permission denied (publickey).")
	if h := v.Health(); h.Problem == nil || h.Problem.Code != report.SSHAuthFailed {
		t.Fatalf("auth failed: Problem = %+v, want %q", h.Problem, report.SSHAuthFailed)
	}
	conflict := &ConfigError{Host: "mini.lan", Options: []string{"localforward"}}
	v.giveUpWith(conflict.Error(), conflict.Report())
	h = v.Health()
	if h.Problem == nil || h.Problem.Code != report.SSHConfigConflict {
		t.Fatalf("config conflict: Problem = %+v, want %q", h.Problem, report.SSHConfigConflict)
	}
	if h.LastError != conflict.Error() || !strings.HasPrefix(h.LastError, h.Problem.Title) {
		t.Errorf("LastError %q, title %q", h.LastError, h.Problem.Title)
	}
	v.setStatus(StatusConnected, "")
	if h := v.Health(); h.Problem != nil {
		t.Errorf("connected: Problem = %+v", h.Problem)
	}
}

// The ssh configuration a per-process transport cannot work with is a
// message whose step is the advice its text ends with.
func TestConfigErrorReport(t *testing.T) {
	err := &ConfigError{Host: "mini", Options: []string{"localforward", "remoteforward"}}
	m, ok := report.As(err)
	if !ok {
		t.Fatal("no message")
	}
	if m.Code != report.SSHConfigConflict {
		t.Errorf("code = %q", m.Code)
	}
	step := "Move them to a Host entry Nearhost doesn't use."
	if len(m.Fix) != 1 || m.Fix[0].Text != step {
		t.Errorf("fix = %+v, want %q", m.Fix, step)
	}
	if err.Error() != m.Title+" "+step {
		t.Errorf("Error() = %q, want the title then the step", err.Error())
	}
}

// The socket path message has its own code, and the step that fixes it.
func TestSocketPathMessage(t *testing.T) {
	s := NewSession("mini", "mini", "/"+strings.Repeat("d", 120))
	err := s.Preflight()
	m, ok := report.As(err)
	if !ok || m.Code != report.SSHSocketPathTooLong {
		t.Fatalf("Preflight = %v, want %q", err, report.SSHSocketPathTooLong)
	}
	if m.Title != err.Error() || !IsSocketPathMessage(err.Error()) {
		t.Errorf("title %q, text %q", m.Title, err.Error())
	}
	step := "Set XDG_CONFIG_HOME to a shorter path and move the nearhost folder that holds your config there, then quit Nearhost and start it again."
	if len(m.Fix) != 1 || m.Fix[0].Text != step {
		t.Errorf("fix = %+v, want %q", m.Fix, step)
	}
}
