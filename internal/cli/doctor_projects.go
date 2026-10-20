package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/discovery"
	"github.com/nearhost/nearhost/internal/doctor"
	"github.com/nearhost/nearhost/internal/hosts"
	"github.com/nearhost/nearhost/internal/nearhostrc"
	"github.com/nearhost/nearhost/internal/proxy"
)

// doctorEnv is what the project checks need of the machine.
type doctorEnv struct {
	lookup    hosts.Lookup
	readHosts func() (string, error)
	noHosts   bool
	listening func(ctx context.Context, port int) bool
	caCert    string // "" when the CA's directory can't be found
	goos      string
	// environ is the environment a project on this machine runs Compose
	// in, as os.Environ gives it; nil when unknown.
	environ func() []string
}

// listenTimeout bounds the dial that tells whether a dev server listens. A
// listener on loopback answers at once, so waiting longer finds nothing more.
const listenTimeout = 500 * time.Millisecond

// systemDoctorEnv is the production doctorEnv. storage is the proxy's Caddy
// directory, which holds the root certificate, or "" when it can't be found.
func systemDoctorEnv(lookup hosts.Lookup, noHosts bool, storage string) doctorEnv {
	env := doctorEnv{
		lookup: lookup,
		readHosts: func() (string, error) {
			data, err := os.ReadFile(hosts.DefaultPath)
			return string(data), err
		},
		noHosts: noHosts,
		listening: func(ctx context.Context, port int) bool {
			d := net.Dialer{Timeout: listenTimeout}
			conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
			if err != nil {
				return false
			}
			conn.Close()
			return true
		},
		goos:    runtime.GOOS,
		environ: os.Environ,
	}
	if storage != "" {
		env.caCert = proxy.RootCertPath(storage)
	}
	return env
}

// caddyDirOrEmpty is config.CaddyDir, or "" when it can't be worked out.
func caddyDirOrEmpty() string {
	dir, err := config.CaddyDir()
	if err != nil {
		return ""
	}
	return dir
}

// projectFindings checks p, a configured project when added and a draft
// from draftProject otherwise, reading its files through files.
func projectFindings(ctx context.Context, env doctorEnv, cfg *config.Config, cfgPath string, p config.Project, added, running bool, files discovery.FileReader) []doctor.Finding {
	return doctor.Check(ctx, doctorInput(env, cfg, cfgPath, p, added, running, files))
}

// doctorInput gathers what the checks need to know of p and the machine.
func doctorInput(env doctorEnv, cfg *config.Config, cfgPath string, p config.Project, added, running bool, files discovery.FileReader) doctor.Input {
	in := doctor.Input{
		Project:   p,
		Added:     added,
		Files:     files,
		Lookup:    env.lookup,
		Running:   running,
		Listening: env.listening,
		CACert:    env.caCert,
		GOOS:      env.goos,
	}
	// Compose fills in a project's file from the shell it runs in. That is
	// unknown for a project on a server; for one here, Nearhost's own
	// environment stands in for the user's shell, which is what it is when
	// they run nearhost doctor, and close to it under the interface or up.
	if p.Server == config.LocalServer && env.environ != nil {
		in.Environ = environMap(env.environ())
	}
	for _, o := range cfg.Projects {
		if !added || o.Name != p.Name {
			in.Others = append(in.Others, o)
		}
	}

	res, notes, err := discoverOnce(cfg, cfgPath, files, nil, nil)
	if err != nil {
		// The checks that need discovery stay quiet; this says why.
		in.Notes = []string{"Discovery failed: " + err.Error()}
	} else {
		in.Discovered = res
		in.Notes = notes
		in.Advisories, notes = discovery.Advise(adviceResult(res, p.Routes), p.Alias, p.AllHosts(), in.Others)
		in.Notes = append(in.Notes, notes...)
	}

	// A hosts file that doesn't exist yet is an empty one, as `nearhost up`
	// takes it. One that can't be read says nothing about what is in the
	// block, so the checks that compare with it stay quiet.
	if !env.noHosts && env.readHosts != nil {
		content, err := env.readHosts()
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			in.ManagesHosts = true
			in.Block = hosts.Aliases(content)
		}
	}
	return in
}

// environMap turns os.Environ's "KEY=value" entries into a map.
func environMap(environ []string) map[string]string {
	out := make(map[string]string, len(environ))
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			out[k] = v
		}
	}
	return out
}

// adviceResult is res with each route found taking the role the project
// gives its port, as the interface's advice does: the user's choice of web
// or ignore decides which ports clash. res itself is left as discovery made
// it, since the drift check compares the project with that.
func adviceResult(res *discovery.Result, routes []config.Route) *discovery.Result {
	roles := map[int]config.Role{}
	for _, r := range routes {
		roles[r.Port] = r.Role
	}
	adv := *res
	adv.Routes = slices.Clone(res.Routes)
	for i := range adv.Routes {
		if role, ok := roles[adv.Routes[i].Port]; ok {
			adv.Routes[i].Role = role
		}
	}
	return &adv
}

// draftProject is the project nearhost add would make of dir on srv:
// defaultName, the alias add gives it, discoveredRoutes, then the
// .nearhostrc merged in when it parses.
func draftProject(cfg *config.Config, cfgPath string, srv config.Server, dir string, files discovery.FileReader) config.Project {
	p, err := newProject(cfg, cfgPath, srv, addFlags{}, []string{dir})
	if err != nil {
		// add refuses a name or an alias another project has. The draft is
		// still checked, under the name and alias add would have tried.
		name := defaultName(srv, dir)
		p = config.Project{Name: name, Server: srv.Name, Path: dir, Alias: config.AliasFor(name), Enabled: true}
	}
	if res, _, err := discoverOnce(cfg, cfgPath, files, nil, nil); err == nil {
		p.Routes = discoveredRoutes(res)
	}
	// A file that can't be read or merged is reported by the checks; the
	// draft keeps what discovery found.
	if rc, found, err := nearhostrc.Read(files); err == nil && found {
		// The file wins over hosts discovery inferred.
		bare := p
		bare.Routes = withoutHosts(p.Routes)
		if merged, err := nearhostrc.Merge(bare, rc, bare.Routes); err == nil {
			p = merged
		}
	}
	return p
}

// projectTimeout bounds the checks of one project, so that a server that
// stops answering can't hang doctor. A variable so tests can shorten it.
var projectTimeout = 30 * time.Second

// projectReport is the findings of one project.
type projectReport struct {
	name     string
	findings []doctor.Finding
}

// unchecked is the finding of a project whose checks could not run.
func unchecked(project, message, fix string) doctor.Finding {
	return doctor.Finding{
		ID: "projects.unchecked", Severity: doctor.SeverityInfo, Project: project,
		Message: message, Fix: doctor.Fix{Kind: doctor.FixManual, Text: fix},
	}
}

// checkProjects checks the enabled projects in config order. Each server's
// files are opened once, when its first project needs them, and closed at
// the end. A project on a server ssh does not reach, or whose files can't be
// opened, is reported unchecked.
func checkProjects(ctx context.Context, b doctorBackend, env doctorEnv, cfg *config.Config, cfgPath string, reachable map[string]bool) []projectReport {
	const sshFix = "Make ssh to the server work (see the server check above), then run `nearhost doctor` again."
	opened := map[string]serverFiles{}
	// skip says why the projects of a server are not checked.
	skip := map[string]doctor.Finding{}
	defer func() {
		for _, f := range opened {
			f.close()
		}
	}()

	var reports []projectReport
	for _, p := range cfg.Projects {
		if !p.Enabled {
			continue
		}
		add := func(findings ...doctor.Finding) {
			reports = append(reports, projectReport{name: p.Name, findings: findings})
		}
		if !reachable[p.Server] {
			add(unchecked(p.Name, "not checked: server "+p.Server+" can't be reached", sshFix))
			continue
		}
		if why, ok := skip[p.Server]; ok {
			why.Project = p.Name
			add(why)
			continue
		}
		files, ok := opened[p.Server]
		if !ok {
			srv, _ := cfg.Server(p.Server)
			var err error
			if files, err = b.openFiles(ctx, srv); err != nil {
				skip[p.Server] = unchecked(p.Name, "not checked: server "+p.Server+" can't be reached: "+err.Error(), sshFix)
				add(skip[p.Server])
				continue
			}
			opened[p.Server] = files
		}
		findings, ok := checkProject(ctx, env, cfg, cfgPath, p, files)
		if !ok {
			// The server's files stopped answering; its other projects
			// would wait as long.
			skip[p.Server] = findings[0]
		}
		add(findings...)
	}
	return reports
}

// checkProject runs projectFindings on p within projectTimeout. ok is false
// when the checks did not finish in time. They are then left running, with
// nothing waiting for them: closing the server's files ends their reads.
func checkProject(ctx context.Context, env doctorEnv, cfg *config.Config, cfgPath string, p config.Project, files serverFiles) (findings []doctor.Finding, ok bool) {
	where := "server " + p.Server
	if p.Server == config.LocalServer {
		where = "this machine"
	}
	ctx, cancel := context.WithTimeout(ctx, projectTimeout)
	defer cancel()
	done := make(chan []doctor.Finding, 1)
	go func() {
		dir, err := files.resolve(p.Path)
		if err != nil {
			done <- []doctor.Finding{unchecked(p.Name, "not checked: "+err.Error(),
				"Check that the project's directory exists on "+where+", or correct its path in Nearhost.")}
			return
		}
		// Doctor doesn't know whether a Nearhost is running, so a port
		// nothing listens on is only worth knowing.
		done <- projectFindings(ctx, env, cfg, cfgPath, p, true, false, files.reader(dir))
	}()
	select {
	case findings := <-done:
		return findings, true
	case <-ctx.Done():
		return []doctor.Finding{unchecked(p.Name, "not checked: the checks did not finish in time",
			"Check the connection to "+where+", then run `nearhost doctor` again.")}, false
	}
}

// printProjects prints the project section: a line for each finding, with
// its fix and any snippet under it, or one line for a project with none. It
// prints nothing when no project is enabled. Findings quote the project's
// files, so what they say is made safe to print first.
func printProjects(out io.Writer, projects []projectReport) {
	if len(projects) == 0 {
		return
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Projects")
	for _, p := range projects {
		if len(p.findings) == 0 {
			fmt.Fprintf(out, "%-4s  %-16s %s\n", levelOK, p.name, "no problems found")
			continue
		}
		for _, f := range p.findings {
			message := strings.ReplaceAll(strings.TrimRight(printableLines(f.Message), "\n"), "\n", "\n"+strings.Repeat(" ", 23))
			fmt.Fprintf(out, "%-4s  %-16s %s\n", severityLabel(f.Severity), p.name, message)
			fmt.Fprintf(out, "      fix: %s\n", printable(f.Fix.Text))
			if f.Fix.Snippet != "" {
				for _, line := range strings.Split(strings.TrimRight(printableLines(f.Fix.Snippet), "\n"), "\n") {
					fmt.Fprintf(out, "          %s\n", line)
				}
			}
		}
	}
}

// severityLabel is how a finding's severity reads beside the machine checks'
// levels.
func severityLabel(s doctor.Severity) string {
	switch s {
	case doctor.SeverityError:
		return levelFail.String()
	case doctor.SeverityWarning:
		return levelWarn.String()
	}
	return "info"
}

type jsonCheck struct {
	Name   string `json:"name"`
	Level  string `json:"level"`
	Detail string `json:"detail"`
	Fix    string `json:"fix"`
}

type jsonReport struct {
	Checks   []jsonCheck      `json:"checks"`
	Findings []doctor.Finding `json:"findings"`
}

// reportJSON prints the checks and the findings as one JSON object, and
// returns the error report would.
func reportJSON(cmd *cobra.Command, checks []check, projects []projectReport) error {
	r := jsonReport{Checks: []jsonCheck{}, Findings: []doctor.Finding{}}
	for _, c := range checks {
		r.Checks = append(r.Checks, jsonCheck{Name: c.name, Level: strings.ToLower(c.level.String()), Detail: c.detail, Fix: c.fix})
	}
	for _, p := range projects {
		r.Findings = append(r.Findings, p.findings...)
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return err
	}
	failed, _ := tally(checks, projects)
	return failure(failed)
}
