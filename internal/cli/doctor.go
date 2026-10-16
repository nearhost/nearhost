package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/hosts"
	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/transport"
)

// level is the outcome of one check. Only a failure makes doctor exit
// non-zero.
type level int

const (
	levelOK level = iota
	levelWarn
	levelFail
)

func (l level) String() string {
	return [...]string{"ok", "warn", "FAIL"}[l]
}

// check is one line of the report.
type check struct {
	name   string
	level  level
	detail string
	fix    string // what to do; empty when the check passed
}

// doctorBackend is the seam between the checks and the machine: tests
// substitute every function, so no test probes the real system.
type doctorBackend struct {
	trust trustBackend
	// probePort reports whether port can be bound on every interface of the
	// given network, "tcp" or "udp".
	probePort func(network string, port int) error
	// holderOf names the process listening on a port, or returns "".
	holderOf func(ctx context.Context, port int) string
	// readHosts returns the content of the hosts file.
	readHosts func() (string, error)
	// lookup finds the addresses DNS gives a name; nil keeps every name in
	// the hosts check.
	lookup hosts.Lookup
	// checkServer reports whether ssh reaches a server without prompting.
	checkServer func(ctx context.Context, srv config.Server) error
	// checkAgents reports whether a server forwards the socket agents reach
	// Nearhost on. nil skips the check.
	checkAgents func(ctx context.Context, srv config.Server) error
	goos        string
	// exe is the path of the running binary, or "" when it is not known.
	exe string
	// lowPorts reports whether this process may bind port on Linux.
	lowPorts func(port int) (level, string)
}

// systemDoctor is the production backend. Only NewRoot wires it in, with
// the lookup it shares with the engine and the interface.
func systemDoctor(lookup hosts.Lookup) doctorBackend {
	return doctorBackend{
		trust:     systemTrust(),
		probePort: probePort,
		holderOf:  engine.PortHolder,
		readHosts: func() (string, error) {
			data, err := os.ReadFile(hosts.DefaultPath)
			return string(data), err
		},
		lookup:      lookup,
		checkServer: sshClient{}.check,
		checkAgents: sshClient{}.checkAgents,
		goos:        runtime.GOOS,
		exe:         realExecutable(),
		lowPorts: func(port int) (level, string) {
			return linuxLowPorts(port, os.Geteuid(), os.ReadFile)
		},
	}
}

func newDoctorCmd(b doctorBackend) *cobra.Command {
	var flags serveFlags
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check ports, the local CA, the hosts block and server access",
		Long: "Check what `nearhost up` needs: free proxy ports, a trusted local CA, the\n" +
			"hosts block and access to every configured server, and whether coding agents\n" +
			"there can reach Nearhost. Each check is reported as ok, warn or FAIL with what\n" +
			"to do about it. Only a FAIL makes the exit status non-zero. Nothing is changed,\n" +
			"apart from ~/.nearhost on each server, which the agents check creates as\n" +
			"`nearhost` would.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.validate(); err != nil {
				return err
			}
			return report(cmd, runChecks(cmd, b, flags))
		},
	}
	flags.bind(cmd)
	return cmd
}

func runChecks(cmd *cobra.Command, b doctorBackend, flags serveFlags) []check {
	var checks []check
	cfg, path, err := loadConfig(cmd)
	switch {
	case err != nil && path == "":
		checks = append(checks, check{"config", levelFail, err.Error(), "set HOME or XDG_CONFIG_HOME, or pass --config"})
	case err != nil:
		checks = append(checks, check{"config", levelFail, err.Error(), "edit " + path})
	default:
		checks = append(checks, check{name: "config", detail: path})
	}

	ctx := cmd.Context()
	checks = append(checks, checkPort(ctx, b, flags.httpsPort, "--https-port", true), checkPort(ctx, b, flags.httpPort, "--http-port", false))
	if low := min(flags.httpsPort, flags.httpPort); b.goos == "linux" && low < 1024 {
		lv, detail := b.lowPorts(low)
		c := check{name: "privileged ports", level: lv, detail: detail}
		if lv != levelOK {
			c.fix = setcapFix(b.exe)
		}
		checks = append(checks, c)
	}
	checks = append(checks, checkCA(b.trust)...)
	if cfg == nil {
		// The checks above do not need the config. These do, and comparing
		// with an empty one would report what is there as wrong.
		unread := func(name string) check {
			return check{name, levelWarn, "not checked: the config could not be read", "correct the config (see above), then run `nearhost doctor` again"}
		}
		if !flags.noHosts {
			checks = append(checks, unread("hosts block"))
		}
		return append(checks, unread("servers"))
	}
	checks = append(checks, checkAliases(cfg, path)...)
	if !flags.noHosts {
		checks = append(checks, checkHosts(ctx, b, cfg))
	}
	for _, srv := range cfg.Servers {
		c := checkServer(ctx, b, srv)
		checks = append(checks, c)
		if c.level == levelOK && b.checkAgents != nil {
			checks = append(checks, checkAgents(ctx, b, srv))
		}
	}
	return checks
}

// checkAgents reports whether coding agents on srv can reach Nearhost. They
// are a convenience, so a server that refuses is a warning.
func checkAgents(ctx context.Context, b doctorBackend, srv config.Server) check {
	name := "agents on " + srv.Name
	if err := b.checkAgents(ctx, srv); err != nil {
		fix := "set AllowStreamLocalForwarding yes in the sshd_config of " + srv.SSHHost + ", or leave it: only coding agents there need it"
		var agentErr *transport.AgentError
		if errors.As(err, &agentErr) {
			switch agentErr.Problem {
			case transport.AgentNotReady:
				fix = "check that the account " + srv.SSHHost + " logs in as can write ~/.nearhost, then run `nearhost doctor` again"
			case transport.AgentTooLong:
				fix = "use an account with a shorter home directory on " + srv.SSHHost + ", or leave it: only coding agents there need it"
			}
		}
		return check{name, levelWarn, err.Error(), fix}
	}
	return check{name: name, detail: "can reach Nearhost through ~/" + transport.AgentDir}
}

// report prints the checks and returns an error when one failed.
func report(cmd *cobra.Command, checks []check) error {
	out := cmd.OutOrStdout()
	failed, warned := 0, 0
	for _, c := range checks {
		switch c.level {
		case levelFail:
			failed++
		case levelWarn:
			warned++
		}
		// A detail of several lines (ssh can print more than one) keeps its
		// lines, aligned under the first.
		detail := strings.ReplaceAll(strings.TrimRight(c.detail, "\n"), "\n", "\n"+strings.Repeat(" ", 23))
		fmt.Fprintf(out, "%-4s  %-16s %s\n", c.level, c.name, detail)
		if c.fix != "" {
			fmt.Fprintf(out, "      fix: %s\n", c.fix)
		}
	}
	fmt.Fprintln(out)
	switch {
	case failed > 0:
		summary := plural(failed, "check") + " failed"
		fmt.Fprintln(out, summary+".")
		return errors.New(summary)
	case warned > 0:
		fmt.Fprintf(out, "No failures, %s.\n", plural(warned, "warning"))
	default:
		fmt.Fprintln(out, "All checks passed.")
	}
	return nil
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// setcapFix is the command that lets the binary at exe bind ports below 1024.
// It names the binary itself rather than `command -v nearhost`, which for an
// npm install is the Node launcher: a capability on a script does nothing.
func setcapFix(exe string) string {
	if exe == "" {
		return `sudo setcap cap_net_bind_service=+ep "$(command -v nearhost)"`
	}
	return "sudo setcap cap_net_bind_service=+ep '" + strings.ReplaceAll(exe, "'", `'\''`) + "'"
}

// realExecutable is the running binary's path with symlinks resolved, since
// setcap sets the capability on the file it is given, or "".
func realExecutable() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		return real
	}
	return exe
}

// checkPort reports whether the proxy could listen on port: on TCP, and with
// udp also on UDP. The two are probed separately, so a port taken on both is
// reported for both.
func checkPort(ctx context.Context, b doctorBackend, port int, flag string, udp bool) check {
	c := check{name: fmt.Sprintf("port %d", port), detail: "free"}
	networks := []string{"tcp"}
	if udp {
		networks = append(networks, "udp")
	}
	var problems []string
	denied, who := false, "the process using it"
	for _, network := range networks {
		err := b.probePort(network, port)
		if err == nil {
			continue
		}
		if errors.Is(err, fs.ErrPermission) {
			denied = true
			continue
		}
		what := err.Error()
		var errno syscall.Errno
		if errors.As(err, &errno) {
			what = errno.Error() // "address already in use", without the listen address
		}
		if network == "udp" {
			what = "UDP (for HTTP/3): " + what
		} else if holder := b.holderOf(ctx, port); holder != "" {
			what, who = "in use by "+holder, "that process"
		}
		problems = append(problems, what)
	}
	switch {
	case denied:
		c.level, c.detail = levelFail, "permission denied"
		c.fix = "run nearhost as a user that may bind this port, or pass another " + flag
		if b.goos == "linux" {
			c.fix = setcapFix(b.exe)
		}
	case len(problems) > 0:
		c.level, c.detail = levelFail, strings.Join(problems, "; ")
		c.fix = fmt.Sprintf("stop %s (expected while `nearhost up` is running), or give `nearhost up` another %s", who, flag)
	}
	return c
}

// checkCA reports the local CA against the system trust store and, on its
// own line, against the NSS stores some browsers use instead.
func checkCA(b trustBackend) []check {
	const name, fix = "local CA", "run `nearhost trust`"
	dir, err := config.CaddyDir()
	if err != nil {
		return []check{{name, levelFail, err.Error(), "set HOME or XDG_CONFIG_HOME"}}
	}
	cert, err := b.rootCert(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return []check{{name, levelFail, "not created yet", fix}}
	case err != nil:
		return []check{{name, levelFail, err.Error(), "remove " + proxy.RootCertPath(dir) + " if it is damaged, then " + fix}}
	}
	ca := check{name: name, detail: "trusted by the system trust store"}
	if !b.isTrusted(cert) {
		ca = check{name, levelFail, "exists, but the system trust store does not trust it", fix}
	}
	if !cert.NotAfter.IsZero() {
		ca.detail += "; expires " + cert.NotAfter.Format("2006-01-02")
	}
	nss := check{name: "browser stores"}
	switch st := b.nss(cert); st.State {
	case proxy.NSSCovered:
		nss.detail = "the NSS stores trust the CA"
	case proxy.NSSMissing:
		nss.level, nss.detail, nss.fix = levelWarn, "the NSS stores (Firefox, and on Linux the shared database Chromium-family browsers use) do not trust the CA", fix
	default:
		nss.level, nss.detail = levelWarn, "not checked: "+st.Detail
		nss.fix = "only Firefox (and on Linux Chromium-family browsers) need this; set them up, then " + fix
	}
	return []check{ca, nss}
}

// checkAliases reports the projects whose alias is not a valid .test name,
// which `nearhost up` does not start. It adds no line when every alias is
// valid; the hosts check leaves the invalid ones out.
func checkAliases(cfg *config.Config, path string) []check {
	var invalid []string
	for _, p := range cfg.Projects {
		if err := config.ValidProjectAlias(p.Alias); err != nil {
			invalid = append(invalid, fmt.Sprintf("project %q: %v", p.Name, err))
		}
	}
	if len(invalid) == 0 {
		return nil
	}
	return []check{{"aliases", levelFail, strings.Join(invalid, "\n"),
		"correct the alias in " + path + "; `nearhost up` does not start a project with such an alias"}}
}

// checkHosts compares the managed block with the names `nearhost up` would
// put there: the config's hosts file names, less the names DNS already sends
// to this machine.
func checkHosts(ctx context.Context, b doctorBackend, cfg *config.Config) check {
	const name, fix = "hosts block", "run `nearhost up`; it updates the block and asks for your password"
	want := hosts.Wanted(ctx, cfg.HostsFileNames(), b.lookup)
	existing, err := b.readHosts()
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return check{name, levelFail, err.Error(), "make " + hosts.DefaultPath + " readable"}
	}
	updated, err := hosts.Rewrite(existing, want)
	if err != nil {
		return check{name, levelFail, err.Error(), fix}
	}
	if updated == existing || updated == existing+"\n" {
		if len(want) == 0 {
			return check{name: name, detail: "no names configured"}
		}
		if len(want) == 1 {
			return check{name: name, detail: "1 name"}
		}
		return check{name: name, detail: fmt.Sprintf("%d names", len(want))}
	}
	missing, stale := diff(want, hosts.Aliases(existing))
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, "missing "+strings.Join(missing, ", "))
	}
	if len(stale) > 0 {
		parts = append(parts, "no longer configured: "+strings.Join(stale, ", "))
	}
	if len(parts) == 0 {
		parts = []string{"the block is not in the form nearhost writes"}
	}
	return check{name, levelFail, strings.Join(parts, "; "), fix}
}

// diff returns what is only in want and what is only in have, each sorted.
func diff(want, have []string) (missing, extra []string) {
	in := func(list []string) map[string]bool {
		m := map[string]bool{}
		for _, s := range list {
			m[s] = true
		}
		return m
	}
	wanted, had := in(want), in(have)
	for s := range wanted {
		if !had[s] {
			missing = append(missing, s)
		}
	}
	for s := range had {
		if !wanted[s] {
			extra = append(extra, s)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}

func checkServer(ctx context.Context, b doctorBackend, srv config.Server) check {
	name := "server " + srv.Name
	err := b.checkServer(ctx, srv)
	if err == nil {
		return check{name: name, detail: "ssh " + srv.SSHHost + " works without a prompt"}
	}
	detail := err.Error()
	var refused *transport.CheckError
	if errors.As(err, &refused) {
		detail = refused.Stderr // what OpenSSH printed, as it printed it
	}
	return check{name, levelFail, detail, sshHint(srv.SSHHost, "run `nearhost doctor` again")}
}
