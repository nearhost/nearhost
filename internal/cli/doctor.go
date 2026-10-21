package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/doctor"
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
	// env is what the hosts check and the project checks need of the
	// machine. Its lookup may be nil, which keeps every name in the hosts
	// check.
	env doctorEnv
	// openFiles opens the files of a server's projects. nil skips the
	// project section.
	openFiles func(ctx context.Context, srv config.Server) (serverFiles, error)
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
		// --no-hosts is a flag of the run, so it is applied then. A config
		// directory that can't be found leaves the CA check out of the
		// project section; the machine checks report it.
		env: systemDoctorEnv(lookup, false, caddyDirOrEmpty()),
		openFiles: func(ctx context.Context, srv config.Server) (serverFiles, error) {
			return openFiles(ctx, sshClient{}, srv, "run `nearhost doctor` again")
		},
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
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check ports, the local CA, the hosts block and server access",
		Long: "Check what `nearhost up` needs: free proxy ports, a trusted local CA, the\n" +
			"hosts block and access to every configured server, and whether coding agents\n" +
			"there can reach Nearhost. Each check is reported as ok, warn or FAIL with what\n" +
			"to do about it. Only a FAIL makes the exit status non-zero. Nothing is changed,\n" +
			"apart from ~/.nearhost on each server, which the agents check creates as\n" +
			"`nearhost` would. Then each enabled project is checked for problems in its\n" +
			"files and setup, and --json prints all of it as one JSON object instead.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.validate(); err != nil {
				return err
			}
			m := runChecks(cmd, b, flags)
			var projects []projectReport
			if m.cfg != nil && b.openFiles != nil {
				env := b.env
				env.noHosts = env.noHosts || flags.noHosts
				projects = checkProjects(cmd.Context(), b, env, m.cfg, m.cfgPath, m.reachable)
				projects = withoutReported(projects, m.hostsMissing)
			}
			if asJSON {
				return reportJSON(cmd, m.checks, projects)
			}
			return report(cmd, m.checks, projects)
		},
	}
	flags.bind(cmd)
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the checks and the projects' findings as JSON")
	return cmd
}

// machine is the outcome of the machine checks, with what the project
// section needs from them.
type machine struct {
	checks  []check
	cfg     *config.Config // nil when the config could not be read
	cfgPath string
	// reachable holds the servers ssh reaches, and this machine.
	reachable map[string]bool
	// hostsMissing holds the names the hosts block check failed for
	// lacking. The projects' hosts.unresolved findings for them would
	// repeat it with another fix.
	hostsMissing []string
}

// withoutReported is projects without their hosts.unresolved findings for
// the names in missing, which the hosts block check reported. A finding for
// any other name stays: when the check fails for stale names only, or for a
// hosts file it could not read, the finding is all that names it. Names
// compare without regard to case, as DNS compares them.
func withoutReported(projects []projectReport, missing []string) []projectReport {
	out := make([]projectReport, len(projects))
	for i, p := range projects {
		out[i] = projectReport{name: p.name, findings: slices.DeleteFunc(slices.Clone(p.findings),
			func(f doctor.Finding) bool {
				return f.ID == "hosts.unresolved" &&
					slices.ContainsFunc(missing, func(n string) bool { return strings.EqualFold(n, f.Host) })
			})}
	}
	return out
}

func runChecks(cmd *cobra.Command, b doctorBackend, flags serveFlags) machine {
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
	if flags.webPort != 0 {
		checks = append(checks, checkPort(ctx, b, flags.webPort, "--web-port", false))
	}
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
		return machine{checks: append(checks, unread("servers")), cfgPath: path}
	}
	checks = append(checks, checkAliases(cfg, path)...)
	var missing []string
	if !flags.noHosts {
		var c check
		c, missing = checkHosts(ctx, b, cfg)
		checks = append(checks, c)
	}
	reachable := map[string]bool{config.LocalServer: true}
	for _, srv := range cfg.Servers {
		c := checkServer(ctx, b, srv)
		checks = append(checks, c)
		reachable[srv.Name] = c.level == levelOK
		if c.level == levelOK && b.checkAgents != nil {
			checks = append(checks, checkAgents(ctx, b, srv))
		}
	}
	return machine{checks: checks, cfg: cfg, cfgPath: path, reachable: reachable, hostsMissing: missing}
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

// tally counts the failures and warnings among the checks and the
// projects' findings. An error finding counts as a failed check; info
// counts as nothing, since it needs no action.
func tally(checks []check, projects []projectReport) (failed, warned int) {
	for _, c := range checks {
		switch c.level {
		case levelFail:
			failed++
		case levelWarn:
			warned++
		}
	}
	for _, p := range projects {
		for _, f := range p.findings {
			switch f.Severity {
			case doctor.SeverityError:
				failed++
			case doctor.SeverityWarning:
				warned++
			}
		}
	}
	return failed, warned
}

// failure is the error doctor exits with when failed checks failed, and
// nil when none did.
func failure(failed int) error {
	if failed == 0 {
		return nil
	}
	return errors.New(plural(failed, "check") + " failed")
}

// report prints the checks, then the project section, and returns an error
// when a check failed or a project has an error.
func report(cmd *cobra.Command, checks []check, projects []projectReport) error {
	out := cmd.OutOrStdout()
	failed, warned := tally(checks, projects)
	for _, c := range checks {
		// A detail of several lines (ssh can print more than one) keeps its
		// lines, aligned under the first.
		detail := strings.ReplaceAll(strings.TrimRight(c.detail, "\n"), "\n", "\n"+strings.Repeat(" ", 23))
		fmt.Fprintf(out, "%-4s  %-16s %s\n", c.level, c.name, detail)
		if c.fix != "" {
			fmt.Fprintf(out, "      fix: %s\n", c.fix)
		}
	}
	printProjects(out, projects)
	fmt.Fprintln(out)
	switch {
	case failed > 0:
		err := failure(failed)
		fmt.Fprintln(out, err.Error()+".")
		return err
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

// checkAliases reports the projects whose main name is not a valid domain,
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
// to this machine. It returns too the names it reports missing from the
// block, none unless it fails.
func checkHosts(ctx context.Context, b doctorBackend, cfg *config.Config) (check, []string) {
	const name, fix = "hosts block", "run `nearhost up`; it updates the block and asks for your password"
	want := hosts.Wanted(ctx, cfg.HostsFileNames(), b.env.lookup)
	existing, err := b.env.readHosts()
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return check{name, levelFail, err.Error(), "make " + hosts.DefaultPath + " readable"}, nil
	}
	updated, err := hosts.Rewrite(existing, want)
	if err != nil {
		return check{name, levelFail, err.Error(), fix}, nil
	}
	if updated == existing || updated == existing+"\n" {
		if len(want) == 0 {
			return check{name: name, detail: "no names configured"}, nil
		}
		if len(want) == 1 {
			return check{name: name, detail: "1 name"}, nil
		}
		return check{name: name, detail: fmt.Sprintf("%d names", len(want))}, nil
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
	return check{name, levelFail, strings.Join(parts, "; "), fix}, missing
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
