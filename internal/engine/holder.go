package engine

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// holderTimeout bounds one lookup, however many tools it tries. The lookup
// only adds detail to an error that is reported anyway, so it gives up
// quickly.
const holderTimeout = 2 * time.Second

// holderGrace is how long a tool that was killed at the deadline may keep its
// output pipe open before the lookup stops waiting for it.
const holderGrace = 250 * time.Millisecond

// holderTool is one way of asking the system which process listens on a port.
type holderTool struct {
	name  string
	args  func(port int) []string
	parse func(out string, port int) string
}

var (
	lsofTool = holderTool{
		name: "lsof",
		args: func(port int) []string {
			return []string{"-nP", "-iTCP:" + strconv.Itoa(port), "-sTCP:LISTEN", "-Fpc"}
		},
		parse: parseLsof,
	}
	ssTool = holderTool{
		name:  "ss",
		args:  func(int) []string { return []string{"-H", "-ltnp"} },
		parse: parseSS,
	}
)

// PortHolder names the process listening on a local TCP port, like
// "node (pid 4242)". It is best effort: it returns "" when no tool is
// installed, the tool is slow or fails, or the process belongs to another
// user and the tool will not name it. It gives up when ctx ends.
func PortHolder(ctx context.Context, port int) string {
	return findHolder(ctx, holderTools(), port, holderTimeout)
}

// heldBy is the clause naming the process that listens on port: " by node
// (pid 4242)", or nothing when it is not known.
func (e *Engine) heldBy(ctx context.Context, port int) string {
	if holder := e.holderOf(ctx, port); holder != "" {
		return " by " + holder
	}
	return ""
}

// findHolder asks each tool in turn and returns the first answer. All tools
// together get at most timeout, and less when ctx ends first: one that uses
// the time up leaves none for the next.
func findHolder(ctx context.Context, tools []holderTool, port int, timeout time.Duration) string {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for _, tool := range tools {
		if ctx.Err() != nil {
			break
		}
		path, err := exec.LookPath(tool.name)
		if err != nil {
			continue
		}
		cmd := exec.CommandContext(ctx, path, tool.args(port)...)
		cmd.WaitDelay = holderGrace // do not wait on a child that keeps the pipe open
		// lsof exits non-zero when it finds nothing; what it printed decides.
		out, _ := cmd.Output()
		if ctx.Err() != nil || len(out) == 0 {
			continue
		}
		if holder := tool.parse(string(out), port); holder != "" {
			return holder
		}
	}
	return ""
}

func describeHolder(command string, pid int) string {
	if command == "" {
		return fmt.Sprintf("pid %d", pid)
	}
	return fmt.Sprintf("%s (pid %d)", command, pid)
}

// parseLsof reads `lsof -Fpc` output: one field per line, "p<pid>" starting a
// process and "c<command>" naming it. It reports the first process.
func parseLsof(out string, _ int) string {
	pid, command := 0, ""
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			if pid != 0 {
				return describeHolder(command, pid) // a second process begins
			}
			n, err := strconv.Atoi(line[1:])
			if err != nil || n <= 0 {
				return ""
			}
			pid = n
		case 'c':
			if pid != 0 && command == "" {
				command = line[1:]
			}
		}
	}
	if pid == 0 {
		return ""
	}
	return describeHolder(command, pid)
}

var ssUser = regexp.MustCompile(`users:\(\("([^"]*)",pid=(\d+)`)

// parseSS reads `ss -H -ltnp` output and reports the first process of the
// line whose local address ends in ":<port>". ss leaves the process out for
// sockets of other users.
func parseSS(out string, port int) string {
	suffix := ":" + strconv.Itoa(port)
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || !strings.HasSuffix(fields[3], suffix) {
			continue
		}
		m := ssUser.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		pid, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		return describeHolder(m[1], pid)
	}
	return ""
}

// parseNetstat reads `netstat -ano` output on Windows and returns the pid of
// the TCP socket listening on port, or 0. A socket listens when its remote
// address is all zeros: the State column and the headings are translated, so
// neither is read.
func parseNetstat(out string, port int) int {
	suffix := ":" + strconv.Itoa(port)
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "TCP" || !strings.HasSuffix(fields[1], suffix) {
			continue
		}
		if fields[2] != "0.0.0.0:0" && fields[2] != "[::]:0" {
			continue
		}
		if pid, err := strconv.Atoi(fields[len(fields)-1]); err == nil && pid > 0 {
			return pid
		}
	}
	return 0
}
