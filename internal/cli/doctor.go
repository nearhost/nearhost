package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

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
	// levelInfo is a note that needs no action. It sits last so the other
	// levels keep their values.
	levelInfo
)

func (l level) String() string {
	return [...]string{"ok", "warn", "FAIL", "info"}[l]
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
	// running reports whether a Nearhost is serving on this machine. nil
	// means none is.
	running func() bool
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
	// stderrTTY reports whether stderr is a terminal, where a slow check
	// says it is running. nil means it is not.
	stderrTTY func() bool
	// slowAfter is how long a check runs before doctor says so. Zero means
	// slowCheckAfter.
	slowAfter time.Duration
	// exe is the path of the running binary, or "" when it is not known.
	exe string
	// lowPorts reports whether this process may bind port on Linux.
	lowPorts func(port int) (level, string)
	// sshAgentService reports the state of the OpenSSH Authentication Agent
	// service: "running", "stopped", "disabled" or "missing". It is nil
	// except on Windows.
	sshAgentService func() (state string, err error)
	// sshVersion runs `ssh -V` and returns its first line.
	sshVersion func(ctx context.Context) (string, error)
	// sshConfig returns the output of `ssh -G` for a host.
	sshConfig func(ctx context.Context, host string) (string, error)
	// lookPath finds a program on PATH as the ssh Nearhost runs is found
	// (exec.Command("ssh") does exactly this). nil skips the ssh vendor
	// check.
	lookPath func(name string) (string, error)
	// getenv reads the environment for the ssh vendor check.
	getenv func(key string) string
	// firefoxRootsOff reports whether Firefox is set not to read Windows'
	// trusted roots, and where that is set. It is nil except on Windows.
	firefoxRootsOff func() (where string, off bool)
}

// systemDoctor is the production backend. Only NewRoot wires it in, with
// the lookup it shares with the engine and the interface.
func systemDoctor(lookup hosts.Lookup) doctorBackend {
	return doctorBackend{
		trust:     systemTrust(),
		probePort: probePort,
		holderOf:  engine.PortHolder,
		running:   nearhostRunning,
		// --no-hosts is a flag of the run, so it is applied then. A config
		// directory that can't be found leaves the CA check out of the
		// project section; the machine checks report it.
		env: systemDoctorEnv(lookup, false, caddyDirOrEmpty()),
		openFiles: func(ctx context.Context, srv config.Server) (serverFiles, error) {
			return openFiles(ctx, sshClient{}, srv, "run `nearhost doctor` again")
		},
		checkServer:     sshClient{}.check,
		checkAgents:     sshClient{}.checkAgents,
		goos:            runtime.GOOS,
		stderrTTY:       func() bool { return term.IsTerminal(int(os.Stderr.Fd())) },
		exe:             realExecutable(),
		sshAgentService: sshAgentService,
		sshVersion:      sshClient{}.version,
		sshConfig:       sshClient{}.config,
		lookPath:        exec.LookPath,
		getenv:          os.Getenv,
		firefoxRootsOff: firefoxRootsOff,
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
			// Text mode prints each check as it finishes. --json prints one
			// document at the end, so it streams nothing.
			st := newDoctorStream(cmd, b, asJSON)
			m := runChecks(cmd, b, flags, st)
			var projects []projectReport
			if m.cfg != nil && b.openFiles != nil {
				env := b.env
				env.noHosts = env.noHosts || flags.noHosts
				projects = checkProjectsStreaming(cmd.Context(), b, env, m.cfg, m.cfgPath, m.reachable, st.projectHooks(m.hostsMissing))
				projects = withoutReported(projects, m.hostsMissing)
			}
			if asJSON {
				return reportJSON(cmd, m.checks, projects)
			}
			return st.summary(m.checks, projects)
		},
	}
	flags.bind(cmd)
	cmd.Flags().Lookup("no-hosts").Usage = "do not check the hosts file"
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

func runChecks(cmd *cobra.Command, b doctorBackend, flags serveFlags, st *doctorStream) machine {
	var checks []check
	// add records checks and, in text mode, prints them.
	add := func(cs ...check) {
		checks = append(checks, cs...)
		st.printChecks(cs...)
	}
	cfg, path, err := loadConfig(cmd)
	switch {
	case err != nil && path == "":
		add(check{"config", levelFail, err.Error(), "set HOME or XDG_CONFIG_HOME, or pass --config"})
	case err != nil:
		add(check{"config", levelFail, err.Error(), "edit " + path})
	default:
		if msg := missingConfigMessage(cmd); msg != "" {
			// The file named with --config is not there: say so, as ls
			// and up do, rather than call a file that is not there ok.
			add(check{"config", levelWarn, msg, "check the path; `nearhost add` with the same --config creates the file"})
			break
		}
		add(checkUnknownKeys(path))
	}

	ctx := cmd.Context()
	add(st.slow(fmt.Sprintf("port %d", flags.httpsPort), func() check { return checkPort(ctx, b, flags.httpsPort, "--https-port", true) }))
	add(st.slow(fmt.Sprintf("port %d", flags.httpPort), func() check { return checkPort(ctx, b, flags.httpPort, "--http-port", false) }))
	if flags.webPort != 0 {
		c := st.slow(fmt.Sprintf("port %d", flags.webPort), func() check { return checkPort(ctx, b, flags.webPort, "--web-port", false) })
		if c.level == levelFail {
			// A page port Nearhost cannot bind costs only the page:
			// `nearhost up` runs without it. checkPort's fix stands for a
			// permission failure, since nothing holds the port then.
			c.level = levelWarn
			if c.detail != "permission denied" {
				c.fix = "Nearhost serves without its page while this port is taken (expected while Nearhost is running); pass --web-port to choose another"
			}
		}
		add(c)
	}
	if low := min(flags.httpsPort, flags.httpPort); b.goos == "linux" && low < 1024 {
		lv, detail := b.lowPorts(low)
		c := check{name: "privileged ports", level: lv, detail: detail}
		if lv != levelOK {
			c.fix = "`" + doctor.SetcapFix(b.exe) + "`"
		}
		add(c)
	}
	st.slowMany("local CA", func() []check { return checkCA(b.trust) }, add)
	if b.goos == "windows" && b.sshAgentService != nil {
		add(checkSSHAgentService(b))
	}
	var sshVer sshVersion
	if b.goos == "windows" && b.sshVersion != nil {
		var c check
		st.slowMany("ssh", func() []check {
			c, sshVer = checkSSHVersion(ctx, b)
			return []check{c}
		}, add)
	}
	if b.goos == "windows" && b.lookPath != nil && b.getenv != nil {
		// Nearhost runs plain "ssh", which the system finds on PATH; this
		// finds the same one. When it is not found, the check above has
		// said so.
		if path, err := b.lookPath("ssh"); err == nil {
			add(checkSSHVendor(path, b.getenv("SystemRoot"), b.getenv("ProgramFiles")))
		}
	}
	if b.goos == "windows" && b.firefoxRootsOff != nil {
		if where, off := b.firefoxRootsOff(); off {
			add(firefoxRootsCheck(where))
		}
	}
	if cfg == nil {
		// The checks above do not need the config. These do, and comparing
		// with an empty one would report what is there as wrong.
		unread := func(name string) check {
			return check{name, levelWarn, "not checked: the config could not be read", "correct the config (see above), then run `nearhost doctor` again"}
		}
		if !flags.noHosts {
			add(unread("hosts block"))
		}
		add(unread("servers"))
		return machine{checks: checks, cfgPath: path}
	}
	add(checkAliases(cfg, path)...)
	add(checkPatternConflicts(cfg, path)...)
	add(checkOwnLoopbacks(cfg, path, flags)...)
	var missing []string
	if !flags.noHosts {
		st.slowMany("hosts block", func() []check {
			var c check
			c, missing = checkHosts(ctx, b, cfg)
			return []check{c}
		}, add)
	}
	reachable := map[string]bool{config.LocalServer: true}
	for _, srv := range cfg.Servers {
		c := st.slow("server "+srv.Name, func() check { return checkServer(ctx, b, srv) })
		// ssh 9.5 for Windows does not resolve an alias named as a ProxyJump
		// hop. When the server check fails and its jump names one, that is
		// the likely cause, so the fix says so; ssh's own words stay as the
		// detail. A server that answers needs no advice.
		if c.level == levelFail && b.goos == "windows" && sshVer.known && sshVer.major == 9 && sshVer.minor == 5 {
			if alias, ok := jumpAlias(ctx, b, srv); ok {
				c.fix = "ProxyJump names the alias " + alias.name + ", which ssh 9.5 for Windows does not resolve: in ~/.ssh/config, give ProxyJump " +
					alias.name + "'s address (" + alias.address + ") or use a ProxyCommand, then run `nearhost doctor` again"
			}
		}
		add(c)
		reachable[srv.Name] = c.level == levelOK
		if c.level == levelOK && b.checkAgents != nil {
			add(st.slow("agents on "+srv.Name, func() check { return checkAgents(ctx, b, srv) }))
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
// nil when none did. The report says it, so Main does not (see exitError).
func failure(failed int) error {
	if failed == 0 {
		return nil
	}
	return exitError{code: 1, msg: plural(failed, "check") + " failed"}
}

// printCheck prints one check: its line, and its fix under it.
func printCheck(out io.Writer, c check) {
	// A detail of several lines (ssh can print more than one) keeps its
	// lines, aligned under the first. What ssh printed comes from the
	// server too, and a check's name can hold a server's, so neither
	// keeps its control characters.
	detail := strings.ReplaceAll(strings.TrimRight(printableLines(c.detail), "\n"), "\n", "\n"+strings.Repeat(" ", 23))
	fmt.Fprintf(out, "%-4s  %-16s %s\n", c.level, printable(c.name), detail)
	if c.fix != "" {
		fmt.Fprintf(out, "      fix: %s\n", upperFirst(printable(c.fix)))
	}
}

// printSummary prints the blank line and the closing sentence, and returns
// the error doctor exits with when a check failed or a project has an error.
func printSummary(out io.Writer, checks []check, projects []projectReport) error {
	failed, warned := tally(checks, projects)
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
	denied, httpSys, who := false, false, "the process using it"
	// mine: Nearhost holds the TCP port. unsure: it is taken while a
	// Nearhost runs, but nothing names the holder.
	mine, unsure := false, false
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
		inUse := isAddrInUse(err, b.goos)
		if network == "udp" {
			// Nothing names a UDP holder. Nearhost holds UDP beside its TCP
			// port for HTTP/3, so a UDP port taken beside a TCP port that
			// Nearhost holds, or may hold, follows the TCP result.
			if inUse && (mine || unsure) {
				continue
			}
			what = "UDP (for HTTP/3): " + what
		} else if holder := b.holderOf(ctx, port); holder != "" {
			if inUse && b.running != nil && b.running() && isNearhostHolder(holder) {
				mine = true
				continue
			}
			what, who = "in use by "+holder, "that process"
			// On Windows pid 4 is the kernel, which holds a port on behalf
			// of HTTP.sys, so no process can be stopped to free it.
			if b.goos == "windows" && holder == "System (pid 4)" {
				what, httpSys = "in use by HTTP.sys (System, pid 4)", true
			}
		} else if inUse && b.running != nil && b.running() {
			unsure = true
			continue
		}
		problems = append(problems, what)
	}
	switch {
	case denied:
		c.level, c.detail = levelFail, "permission denied"
		c.fix = "run nearhost as a user that may bind this port, or pass another " + flag
		if b.goos == "linux" {
			c.fix = "`" + doctor.SetcapFix(b.exe) + "`"
		}
	case len(problems) == 0 && mine:
		c.detail = "in use by the running Nearhost"
	case len(problems) == 0 && unsure:
		// A taken port while Nearhost runs is most likely Nearhost's own,
		// but with no holder named that is not proven. A warning keeps the
		// exit status zero and still shows the line.
		c.level, c.detail = levelWarn, "Nearhost is running, but the holder of this port could not be named; probably Nearhost"
		c.fix = "check with `" + holderCommand(b.goos, port) + "` that the holder is nearhost"
	case len(problems) > 0:
		c.level, c.detail = levelFail, strings.Join(problems, "; ")
		c.fix = fmt.Sprintf("stop %s (expected while `nearhost up` is running), or give `nearhost up` another %s", who, flag)
		if httpSys {
			c.fix = fmt.Sprintf("run `netsh http show servicestate` in an elevated terminal to see which service registered port %d, and stop it (often IIS: `net stop w3svc`), or give `nearhost up` another %s", port, flag)
		}
	}
	return c
}

// isAddrInUse reports whether err says the address is taken. Windows
// reports WSAEADDRINUSE, which is not syscall.EADDRINUSE there.
func isAddrInUse(err error, goos string) bool {
	const wsaEADDRINUSE = syscall.Errno(10048)
	var errno syscall.Errno
	return errors.Is(err, syscall.EADDRINUSE) || (goos == "windows" && errors.As(err, &errno) && errno == wsaEADDRINUSE)
}

// isNearhostHolder reports whether a holder named by engine.PortHolder, as
// "nearhost (pid 41)", is the nearhost program. The running Nearhost's pid
// is not recorded anywhere doctor can read (it knows a Nearhost runs from
// the agent socket answering), so the name decides: the whole name
// "nearhost", which PortHolder gives without a directory or ".exe" on every
// system and which fits ss's 15 and lsof's 9 character limits.
func isNearhostHolder(holder string) bool {
	name, _, ok := strings.Cut(holder, " (pid ")
	return ok && name == "nearhost"
}

// holderCommand is the command that shows which process listens on port.
func holderCommand(goos string, port int) string {
	if goos == "windows" {
		return fmt.Sprintf("netstat -ano | findstr :%d", port)
	}
	if goos == "linux" {
		// lsof is often missing there; ss is what PortHolder falls back to.
		return fmt.Sprintf("ss -ltnp 'sport = :%d'", port)
	}
	return fmt.Sprintf("lsof -nP -iTCP:%d -sTCP:LISTEN", port)
}

// checkSSHAgentService reports the OpenSSH Authentication Agent service on
// Windows. Nearhost's own keys may need no agent, so anything but running
// is only a note.
func checkSSHAgentService(b doctorBackend) check {
	c := check{name: "ssh agent"}
	state, err := b.sshAgentService()
	switch {
	case err != nil:
		c.level, c.detail = levelInfo, "could not query the service: "+err.Error()
	case state == "missing":
		c.level, c.detail = levelInfo, "the OpenSSH Authentication Agent is not installed"
	case state == "running":
		c.detail = state
	default:
		c.level, c.detail = levelInfo, state
		c.fix = "only needed for keys with a passphrase: in an elevated PowerShell, `Get-Service ssh-agent | Set-Service -StartupType Automatic; Start-Service ssh-agent`, then `ssh-add`"
	}
	return c
}

// checkCA reports the local CA against the system trust store and, on its
// own line, against the NSS stores some browsers use instead.
func checkCA(b trustBackend) []check {
	const name, fix = "local CA", "run `nearhost trust`"
	dir, err := config.CaddyDir()
	if err != nil {
		return []check{{name, levelFail, err.Error(), "set HOME"}}
	}
	checks := checkCAAt(b, dir)
	if old, ok := config.LeftoverCaddyDir(); ok {
		checks = append(checks, check{"old CA copy", levelWarn,
			old + " still holds a copy of the local CA's private key; Nearhost now keeps it in " + dir,
			"delete " + old})
	}
	return checks
}

// checkCAAt is checkCA for the CA in dir.
func checkCAAt(b trustBackend, dir string) []check {
	const name, fix = "local CA", "run `nearhost trust`"
	cert, err := b.rootCert(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return []check{{name, levelFail, "not created yet", fix}}
	case err != nil:
		return []check{{name, levelFail, err.Error(), "remove " + proxy.RootCertPath(dir) + " if it is damaged, then " + fix}}
	}
	ca := check{name: name, detail: "trusted by " + trustStore}
	if !b.isTrusted(cert) {
		ca = check{name, levelFail, "exists, but " + trustStore + " does not trust it", fix}
	}
	if !cert.NotAfter.IsZero() {
		ca.detail += "; expires " + cert.NotAfter.Format("2006-01-02")
	}
	nss := check{name: "browser stores"}
	switch st := b.nss(cert); st.State {
	case proxy.NSSCovered:
		nss.detail = "the NSS stores trust the CA"
	case proxy.NSSNotNeeded:
		nss.detail = "not needed: " + st.Detail
	case proxy.NSSMissing:
		nss.level, nss.detail, nss.fix = levelWarn, "the NSS stores (Firefox, and on Linux the shared database Chromium-family browsers use) do not trust the CA", fix
	default:
		nss.level, nss.detail = levelWarn, "not checked: "+st.Detail
		nss.fix = "only Firefox (and on Linux Chromium-family browsers) need this; set them up, then " + fix
	}
	return []check{ca, nss}
}

// checkUnknownKeys is the config check for a config that loads: it passes
// with the file's path, or warns about each key in it Nearhost does not know,
// since the next save is refused until the key is fixed or removed.
func checkUnknownKeys(path string) check {
	unknown, err := config.UnknownKeys(path)
	if err != nil {
		return check{"config", levelWarn, "could not look for keys Nearhost does not know: " + err.Error(), "edit " + path}
	}
	if len(unknown) == 0 {
		return check{name: "config", detail: path}
	}
	lines := make([]string, len(unknown))
	for i, u := range unknown {
		lines[i] = u.Describe(path)
	}
	fix := "fix or remove it; saving now would delete it, so Nearhost refuses to save until then"
	if len(unknown) > 1 {
		fix = "fix or remove them; saving now would delete them, so Nearhost refuses to save until then"
	}
	return check{"config", levelWarn, strings.Join(lines, "\n"), fix}
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

// checkPatternConflicts warns about each exact host of an enabled project
// under another enabled project's pattern. Nearhost refuses a change that
// brings in such a pair, but a config saved before then may hold one, and it
// still serves: the proxy sends the exact host to its own project. It adds no
// line when there is none.
func checkPatternConflicts(cfg *config.Config, path string) []check {
	var lines []string
	for _, c := range cfg.PatternConflicts() {
		lines = append(lines, fmt.Sprintf("project %q serves %s, under the pattern %s of project %q: requests for %s go to %q, not %q",
			c.Project, c.Host, c.OtherHost, c.Other, c.Host, c.Project, c.Other))
	}
	if len(lines) == 0 {
		return nil
	}
	return []check{{"host patterns", levelWarn, strings.Join(lines, "\n"),
		"to end the overlap, remove the exact host from the project that gets its requests, or move it to the pattern's project, in " + path}}
}

// checkOwnLoopbacks reports each enabled project with a loopback route on a
// port Nearhost itself listens on, as flags set them: `nearhost up` does not
// forward such a route, which on this machine would take Nearhost's own
// port. It adds no line when there is none.
func checkOwnLoopbacks(cfg *config.Config, path string, flags serveFlags) []check {
	var lines []string
	for _, p := range cfg.Projects {
		if !p.Enabled {
			continue
		}
		if port, ok := p.LoopbackOnOwnPort(flags.httpsPort, flags.httpPort, flags.webPort); ok {
			lines = append(lines, fmt.Sprintf("project %q: loopback port %d is Nearhost's own port", p.Name, port))
		}
	}
	if len(lines) == 0 {
		return nil
	}
	return []check{{"loopback ports", levelFail, strings.Join(lines, "\n"),
		"give the route another port or role in " + path + ", or start Nearhost with other ports; `nearhost up` does not forward a loopback route on its own port"}}
}

// checkHosts compares the managed block with the names `nearhost up` would
// put there: the config's hosts file names, less the names DNS already sends
// to this machine. It returns too the names it reports missing from the
// block, none unless it fails.
func checkHosts(ctx context.Context, b doctorBackend, cfg *config.Config) (check, []string) {
	const name, fix = "hosts block", hostsFix
	want := hosts.Wanted(ctx, cfg.HostsFileNames(), b.env.lookup)
	existing, err := b.env.readHosts()
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return check{name, levelFail, err.Error(), "make " + hosts.DefaultPath + " readable"}, nil
	}
	if _, err := hosts.Rewrite(existing, want); err != nil {
		return check{name, levelFail, err.Error(), fix}, nil
	}
	if hosts.UpToDate(existing, want) {
		if len(want) == 0 {
			return check{name: name, detail: "no names configured"}, nil
		}
		if len(want) == 1 {
			return check{name: name, detail: "1 name"}, nil
		}
		return check{name: name, detail: fmt.Sprintf("%d names", len(want))}, nil
	}
	if len(want) > 0 && hosts.IsIPv4Only(existing, want) {
		return check{name, levelWarn, "the block was written by an older Nearhost and has no IPv6 (::1) entries", fix}, nil
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

// sshVersion is the version of the ssh on PATH, parsed from `ssh -V`.
type sshVersion struct {
	known        bool
	major, minor int
}

var sshVersionPattern = regexp.MustCompile(`OpenSSH_(?:for_Windows_)?(\d+)\.(\d+)`)

// parseSSHVersion reads the version from a line such as
// "OpenSSH_for_Windows_9.5p2, LibreSSL 3.8.2" or "OpenSSH_9.6p1 ...".
func parseSSHVersion(line string) sshVersion {
	m := sshVersionPattern.FindStringSubmatch(line)
	if m == nil {
		return sshVersion{}
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	return sshVersion{known: true, major: major, minor: minor}
}

// checkSSHVersion reports the ssh on PATH on Windows, where Nearhost needs
// the one Windows 11 ships. A version it cannot read is only a note.
func checkSSHVersion(ctx context.Context, b doctorBackend) (check, sshVersion) {
	c := check{name: "ssh"}
	line, err := b.sshVersion(ctx)
	if err != nil {
		c.level, c.detail = levelFail, "could not run ssh: "+err.Error()
		c.fix = "install the OpenSSH Client in Settings > System > Optional features"
		return c, sshVersion{}
	}
	c.detail = line
	v := parseSSHVersion(line)
	switch {
	case !v.known:
		c.level = levelInfo
	case v.major < 9 || (v.major == 9 && v.minor < 5):
		c.level = levelWarn
		c.detail = line + ": Windows 11's ssh (9.5 or later) is needed"
		c.fix = "update Windows, or install the current OpenSSH for Windows and put it first on PATH"
	}
	return c, v
}

// firefoxRootsCheck is the warning for Firefox set not to read Windows'
// trusted roots, which is where the local CA is trusted. where is "policy"
// or names a profile.
func firefoxRootsCheck(where string) check {
	fix := "set ImportEnterpriseRoots to 1, or remove that policy"
	if where != "policy" {
		fix = "set security.enterprise_roots.enabled to true in about:config"
	}
	return check{"firefox", levelWarn, "Firefox does not read Windows' trusted roots (" + where + ")", fix}
}

// jumpTarget is a ProxyJump hop that names an alias of ssh's configuration.
type jumpTarget struct {
	name    string // the alias
	address string // user@host:port the alias stands for
}

// jumpAlias reports the first ProxyJump hop of srv when it names an alias,
// that is when `ssh -G` resolves the hop to another hostname. A hop given as
// an address resolves to itself.
func jumpAlias(ctx context.Context, b doctorBackend, srv config.Server) (jumpTarget, bool) {
	if b.sshConfig == nil {
		return jumpTarget{}, false
	}
	out, err := b.sshConfig(ctx, srv.SSHHost)
	if err != nil {
		return jumpTarget{}, false
	}
	jump := transport.ConfigValue(out, "proxyjump")
	if jump == "" || strings.EqualFold(jump, "none") {
		return jumpTarget{}, false
	}
	hop, _, _ := strings.Cut(jump, ",")
	user, host, hopPort := splitHop(hop)
	query := host
	if user != "" {
		query = user + "@" + host
	}
	hopOut, err := b.sshConfig(ctx, query)
	if err != nil {
		return jumpTarget{}, false
	}
	hostname := transport.ConfigValue(hopOut, "hostname")
	if hostname == "" || strings.EqualFold(hostname, host) {
		return jumpTarget{}, false
	}
	// ssh -G reports the local user when none is configured, which cannot be
	// told from one configured, so the suggestion names a user only when the
	// hop does.
	addr := hostname
	if user != "" {
		addr = user + "@" + addr
	}
	port := hopPort
	if port == "" {
		if p := transport.ConfigValue(hopOut, "port"); p != "22" {
			port = p
		}
	}
	if port != "" {
		addr += ":" + port
	}
	return jumpTarget{name: host, address: addr}, true
}

// splitHop splits a ProxyJump hop, [ssh://][user@]host[:port], where host may
// be a bracketed IPv6 address.
func splitHop(hop string) (user, host, port string) {
	hop = strings.TrimPrefix(hop, "ssh://")
	if i := strings.LastIndex(hop, "@"); i >= 0 {
		user, hop = hop[:i], hop[i+1:]
	}
	if strings.HasPrefix(hop, "[") {
		if end := strings.Index(hop, "]"); end > 0 {
			return user, hop[1:end], strings.TrimPrefix(hop[end+1:], ":")
		}
	}
	if strings.Count(hop, ":") == 1 {
		hop, port, _ = strings.Cut(hop, ":")
	}
	return user, hop, port
}
