package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
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
	// probePort reports whether the proxy could listen on port: on TCP, and
	// with udp also on UDP.
	probePort func(port int, udp bool) error
	// holderOf names the process listening on a port, or returns "".
	holderOf func(ctx context.Context, port int) string
	// readHosts returns the content of the hosts file.
	readHosts func() (string, error)
	// checkServer reports whether ssh reaches a server without prompting.
	checkServer func(ctx context.Context, srv config.Server) error
	goos        string
	// lowPorts reports whether this process may bind port on Linux.
	lowPorts func(port int) (level, string)
}

// systemDoctor is the production backend. Only NewRoot wires it in.
func systemDoctor() doctorBackend {
	return doctorBackend{
		trust:     systemTrust(),
		probePort: probePort,
		holderOf:  engine.PortHolder,
		readHosts: func() (string, error) {
			data, err := os.ReadFile(hosts.DefaultPath)
			return string(data), err
		},
		checkServer: sshClient{}.check,
		goos:        runtime.GOOS,
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
			"hosts block and access to every configured server. Each check is reported as\n" +
			"ok, warn or FAIL with what to do about it. Only a FAIL makes the exit status\n" +
			"non-zero. Nothing is changed.",
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
	if cfg == nil {
		cfg = &config.Config{} // the checks that do not need it still run
	}

	ctx := cmd.Context()
	checks = append(checks, checkPort(ctx, b, flags.httpsPort, "--https-port", true), checkPort(ctx, b, flags.httpPort, "--http-port", false))
	if low := min(flags.httpsPort, flags.httpPort); b.goos == "linux" && low < 1024 {
		lv, detail := b.lowPorts(low)
		c := check{name: "privileged ports", level: lv, detail: detail}
		if lv != levelOK {
			c.fix = setcapFix
		}
		checks = append(checks, c)
	}
	checks = append(checks, checkCA(b.trust)...)
	if !flags.noHosts {
		checks = append(checks, checkHosts(b, cfg))
	}
	for _, srv := range cfg.Servers {
		checks = append(checks, checkServer(ctx, b, srv))
	}
	return checks
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

const setcapFix = `sudo setcap cap_net_bind_service=+ep "$(command -v nearhost)"`

func checkPort(ctx context.Context, b doctorBackend, port int, flag string, udp bool) check {
	c := check{name: fmt.Sprintf("port %d", port), detail: "free"}
	err := b.probePort(port, udp)
	if err == nil {
		return c
	}
	c.level = levelFail
	if errors.Is(err, fs.ErrPermission) {
		c.detail, c.fix = "permission denied", "run nearhost as a user that may bind this port, or pass another "+flag
		if b.goos == "linux" {
			c.fix = setcapFix
		}
		return c
	}
	who := "the process using it"
	if holder := b.holderOf(ctx, port); holder != "" {
		c.detail, who = "in use by "+holder, "that process"
	} else {
		c.detail = err.Error()
		var errno syscall.Errno
		if errors.As(err, &errno) {
			c.detail = errno.Error() // "address already in use", without the listen address
		}
	}
	c.fix = fmt.Sprintf("stop %s (expected while `nearhost up` is running), or give `nearhost up` another %s", who, flag)
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

// checkHosts compares the managed block with the aliases `nearhost up` would
// put there: the valid alias of every configured project.
func checkHosts(b doctorBackend, cfg *config.Config) check {
	const name, fix = "hosts block", "run `nearhost up`; it updates the block and asks for your password"
	var want []string
	for _, p := range cfg.Projects {
		if _, err := hosts.Rewrite("", []string{p.Alias}); err == nil {
			want = append(want, p.Alias)
		}
	}
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
			return check{name: name, detail: "no aliases configured"}
		}
		if len(want) == 1 {
			return check{name: name, detail: "1 alias"}
		}
		return check{name: name, detail: fmt.Sprintf("%d aliases", len(want))}
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
