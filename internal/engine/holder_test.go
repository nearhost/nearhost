package engine

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseLsofFieldOutput(t *testing.T) {
	// lsof -nP -iTCP:5174 -sTCP:LISTEN -Fpc
	const out = "p4242\ncnode\nf23\nf24\np5000\ncother\nf7\n"
	if got := parseLsof(out, 5174); got != "node (pid 4242)" {
		t.Fatalf("parseLsof = %q", got)
	}
	for _, empty := range []string{"", "\n", "f23\n", "cnode\n", "pnotanumber\ncnode\n"} {
		if got := parseLsof(empty, 5174); got != "" {
			t.Fatalf("parseLsof(%q) = %q, want nothing", empty, got)
		}
	}
	if got := parseLsof("p77\n", 5174); got != "pid 77" {
		t.Fatalf("parseLsof without a command name = %q", got)
	}
}

func TestParseSSOutput(t *testing.T) {
	// ss -H -ltnp, as printed by iproute2.
	const out = `LISTEN 0      4096       127.0.0.53%lo:53         0.0.0.0:*    users:(("systemd-resolve",pid=612,fd=15))
LISTEN 0      511            127.0.0.1:5174       0.0.0.0:*    users:(("node",pid=4242,fd=23),("node",pid=4243,fd=23))
LISTEN 0      128                 [::]:51740         [::]:*    users:(("sshd",pid=900,fd=4))
LISTEN 0      128              0.0.0.0:22         0.0.0.0:*
`
	if got := parseSS(out, 5174); got != "node (pid 4242)" {
		t.Fatalf("parseSS = %q", got)
	}
	if got := parseSS(out, 51740); got != "sshd (pid 900)" {
		t.Fatalf("parseSS for an IPv6 listener = %q", got)
	}
	// Another user's process: ss prints no users column.
	if got := parseSS(out, 22); got != "" {
		t.Fatalf("parseSS without a process column = %q", got)
	}
	if got := parseSS(out, 517); got != "" {
		t.Fatalf("parseSS matched a port by prefix: %q", got)
	}
	if got := parseSS("", 5174); got != "" {
		t.Fatalf("parseSS of nothing = %q", got)
	}
}

func TestFindHolderFallsBackWhenToolsAreMissingSlowOrSilent(t *testing.T) {
	never := func(string, int) string {
		t.Error("parser called for a tool that produced nothing")
		return ""
	}
	missing := holderTool{name: "nearhost-no-such-tool", args: func(int) []string { return nil }, parse: never}
	slow := holderTool{name: "sleep", args: func(int) []string { return []string{"30"} }, parse: never}
	silent := holderTool{name: "true", args: func(int) []string { return nil }, parse: func(string, int) string { return "" }}
	begin := time.Now()
	if got := findHolder([]holderTool{missing, slow, silent}, 5174, 200*time.Millisecond); got != "" {
		t.Fatalf("findHolder = %q, want nothing", got)
	}
	if d := time.Since(begin); d > 5*time.Second {
		t.Fatalf("findHolder took %s; the lookup must be bounded", d)
	}
	// The first tool that names a holder wins.
	echo := holderTool{name: "echo", args: func(port int) []string { return []string{fmt.Sprint(port)} },
		parse: func(out string, port int) string { return "found " + strings.TrimSpace(out) }}
	if got := findHolder([]holderTool{missing, silent, echo}, 5174, time.Second); got != "found 5174" {
		t.Fatalf("findHolder = %q", got)
	}
}

func TestFindHolderSharesOneDeadlineAcrossTools(t *testing.T) {
	never := func(string, int) string {
		t.Error("parser called for a tool that produced nothing")
		return ""
	}
	marker := filepath.Join(t.TempDir(), "second-tool-ran")
	slow := holderTool{name: "sleep", args: func(int) []string { return []string{"30"} }, parse: never}
	second := holderTool{name: "touch", args: func(int) []string { return []string{marker} }, parse: never}
	if got := findHolder([]holderTool{slow, second}, 5174, 300*time.Millisecond); got != "" {
		t.Fatalf("findHolder = %q, want nothing", got)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a second tool was started after the first had used up the lookup's time")
	}
}

func TestPortHolderFindsThisProcess(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	got := portHolder(l.Addr().(*net.TCPAddr).Port)
	if got == "" {
		t.Skip("no lsof or ss that can name the listener on this machine")
	}
	if !strings.Contains(got, fmt.Sprintf("pid %d", os.Getpid())) {
		t.Fatalf("portHolder = %q, want this process (pid %d)", got, os.Getpid())
	}
}
