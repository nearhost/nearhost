package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/discovery"
	"github.com/nearhost/nearhost/internal/hosts"
	"github.com/nearhost/nearhost/internal/preset"
)

// addBackend is the seam between `add` and what lies outside the process:
// a remote server, the trusted preset registries and the terminal.
type addBackend struct {
	// openRemote opens the project directory on a server for reading. The
	// returned function ends the connection.
	openRemote func(ctx context.Context, srv config.Server, projectPath string) (discovery.FileReader, func() error, error)
	// presets reaches the trusted preset registries.
	presets presetBackend
	// interactive reports whether a person can be asked a question. Nil
	// means no.
	interactive func() bool
}

// systemAdd is the production backend. Only NewRoot wires it in.
func systemAdd() addBackend {
	return addBackend{
		openRemote: sshClient{}.openRemote,
		presets:    systemPresets(),
		interactive: func() bool {
			return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
		},
	}
}

type addFlags struct {
	server, name, alias string
	web                 int
	disabled            bool
	presets             []string
	noPresets           bool
}

func newAddCmd(b addBackend) *cobra.Command {
	var flags addFlags
	cmd := &cobra.Command{
		Use:   "add [path]",
		Short: "Add a project, discovering its ports from its files",
		Long: "Add the project in path (default: the current directory) to the config,\n" +
			"with routes proposed from its .env and Compose files and from the presets\n" +
			"that are enabled. When a preset from a trusted registry suits the project\n" +
			"and is not enabled, add offers to enable it. With --server the path is on\n" +
			"that server and its files are read over SFTP. Nothing is started and no\n" +
			"project file is changed.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("web") && (flags.web < 1 || flags.web > 65535) {
				return fmt.Errorf("--web must be a port between 1 and 65535, not %d", flags.web)
			}
			if flags.noPresets && len(flags.presets) > 0 {
				return errors.New("--no-presets and --preset contradict each other; pass one of them")
			}
			return runAdd(cmd, b, flags, args)
		},
	}
	cmd.Flags().StringVar(&flags.server, "server", config.LocalServer, "server the project runs on")
	cmd.Flags().StringVar(&flags.name, "name", "", "project name (default: directory name)")
	cmd.Flags().StringVar(&flags.alias, "alias", "", "hostname (default: <name>.test)")
	cmd.Flags().IntVar(&flags.web, "web", 0, "force this port to be the web route")
	cmd.Flags().BoolVar(&flags.disabled, "disabled", false, "add it disabled: 'nearhost up' starts it only when named")
	cmd.Flags().StringArrayVar(&flags.presets, "preset", nil, "enable this preset first, without asking (repeatable)")
	cmd.Flags().BoolVar(&flags.noPresets, "no-presets", false, "use no presets and suggest none")
	return cmd
}

func runAdd(cmd *cobra.Command, b addBackend, flags addFlags, args []string) error {
	cfg, cfgPath, err := loadConfig(cmd)
	if err != nil {
		return fmt.Errorf("%w; fix the config before adding to it", err)
	}
	project, srv, err := newProject(cfg, cfgPath, flags, args)
	if err != nil {
		return err
	}

	reader, done, err := openProject(cmd.Context(), b, srv, project.Path)
	if err != nil {
		return err
	}
	res, warnings, err := discoverWithPresets(cmd, b, flags, cfg, cfgPath, reader)
	if err != nil {
		return errors.Join(err, done())
	}
	project.Routes = res.ConfigRoutes()
	// Labels come from presets and the project's own files, and are both
	// printed and saved.
	for i := range project.Routes {
		project.Routes[i].Label = printable(project.Routes[i].Label)
	}
	if flags.web > 0 {
		project.Routes = forceWeb(project.Routes, flags.web)
		// The advisories are about the roles the project ends up with.
		// forceWeb keeps the discovered routes in place and only appends.
		for i := range res.Routes {
			res.Routes[i].Role = project.Routes[i].Role
		}
	}
	// Advisories may read the project's files, so they are worked out
	// before the reader is closed.
	advisories, advisoryWarnings := discovery.Advise(res, project.Alias, cfg.Projects)
	if err := done(); err != nil {
		return err
	}
	printWarnings(cmd.ErrOrStderr(), warnings)

	out := cmd.OutOrStdout()
	if err := printRoutes(out, project); err != nil {
		return err
	}
	if _, ok := project.WebRoute(); !ok {
		return fmt.Errorf("could not tell which port serves %s; nothing was saved. Run it again with --web <port>", project.Name)
	}
	for _, a := range advisories {
		fmt.Fprintf(out, "\nAdvisory (%s): %s\n", a.Code, printableLines(a.Message))
		if snippet := printableLines(a.Snippet); snippet != "" {
			fmt.Fprintf(out, "  %s\n", strings.ReplaceAll(snippet, "\n", "\n  "))
		}
	}
	printWarnings(cmd.ErrOrStderr(), advisoryWarnings)

	if err := loopbackClash(cfg, project); err != nil {
		return err
	}
	cfg.Projects = append(cfg.Projects, project)
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("%w; nothing was saved", err)
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		return err
	}
	fmt.Fprintf(out, "\nAdded %s on %s at https://%s\n", project.Name, project.Server, project.Alias)
	start := "nearhost up"
	if !project.Enabled {
		start = upCommand(project.Name)
	}
	_, err = fmt.Fprintf(out, "Start it with `%s`.\n", start)
	return err
}

// discoverWithPresets runs discovery with the enabled presets, then offers
// the presets of the trusted registries that suit the project and runs it
// again with those the user takes. A preset is enabled, and the config
// saved, the moment the user says yes, so the answer holds even if the
// project is not added.
func discoverWithPresets(cmd *cobra.Command, b addBackend, flags addFlags, cfg *config.Config, cfgPath string, reader discovery.FileReader) (*discovery.Result, []string, error) {
	if flags.noPresets {
		res, err := discovery.Discover(reader, discovery.Options{})
		return res, nil, err
	}
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	for _, name := range flags.presets {
		_, changed, err := b.presets.enable(ctx, cfg, cfgPath, name, false)
		if err != nil {
			return nil, nil, err
		}
		if changed {
			if err := config.Save(cfgPath, cfg); err != nil {
				return nil, nil, err
			}
		}
	}

	// notes last for the whole command. What one discovery pass finds is
	// replaced by the next pass.
	var notes []string
	sources := b.presets.indexes(ctx, cfg, cfgPath)
	for _, s := range sources {
		if s.err != nil {
			notes = append(notes, registryProblem(s.reg, s.err)+".")
		}
	}
	interactive := b.interactive != nil && b.interactive()
	answers := bufio.NewReader(cmd.InOrStdin())
	passed := map[string]bool{} // presets declined, or accepted and not installable
	for {
		// Every registry's presets are offered, so two registries that offer
		// the same name are both offered.
		var offers []discovery.Offer
		for _, s := range sources {
			if s.idx == nil {
				continue
			}
			for _, e := range s.idx.Presets {
				name := s.reg.Alias + "/" + e.Name
				_, enabled := cfg.Preset(name)
				if !enabled && !passed[name] && e.PresetSchema <= preset.SchemaVersion {
					offers = append(offers, discovery.Offer{Name: name, Title: e.Title, Detect: e.Detect})
				}
			}
		}
		presets, found := loadEnabled(cfg, cfgPath)
		res, err := discovery.Discover(reader, discovery.Options{Presets: presets, Offers: offers})
		if err != nil {
			return nil, nil, err
		}
		found = append(found, res.Warnings...)
		if !interactive {
			for _, o := range res.Suggested {
				fmt.Fprintf(out, "This looks like a %s project. Pass `--preset %s` to `nearhost add` to use its preset.\n", offerTitle(o), o.Name)
			}
			return res, append(notes, found...), nil
		}

		enabled := false
		for _, o := range res.Suggested {
			alias, _ := config.SplitPreset(o.Name)
			reg, _ := cfg.Trusted(alias)
			fmt.Fprintf(out, "This looks like a %s project. Enable %s from %s? [Y/n] ", offerTitle(o), o.Name, reg.Repo)
			if !saidYes(answers, out) {
				passed[o.Name] = true
				continue
			}
			_, _, err := b.presets.enable(ctx, cfg, cfgPath, o.Name, false)
			if err == nil {
				err = config.Save(cfgPath, cfg)
			}
			if err != nil {
				// A preset that cannot be installed must not stop the add.
				passed[o.Name] = true
				notes = append(notes, fmt.Sprintf("the %s preset was not enabled: %v", o.Name, err))
				continue
			}
			enabled = true
		}
		if !enabled {
			return res, append(notes, found...), nil
		}
	}
}

// offerTitle is what a suggestion calls the project: the preset's title, or
// its name when the title has nothing printable in it.
func offerTitle(o discovery.Offer) string {
	if title := printable(o.Title); title != "" {
		return title
	}
	return o.Name
}

// saidYes reads one line as the answer to a yes-or-no question whose default
// is yes. No answer at all, as when input ends, is a no: a preset is only
// enabled when someone agreed to it.
func saidYes(answers *bufio.Reader, out io.Writer) bool {
	line, err := answers.ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(out)
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "", "y", "yes":
		return true
	}
	return false
}

// newProject works out the project's server, path, name and alias, and
// refuses what the config could not hold. Its routes are still to be found.
func newProject(cfg *config.Config, cfgPath string, flags addFlags, args []string) (config.Project, config.Server, error) {
	fail := func(format string, a ...any) (config.Project, config.Server, error) {
		return config.Project{}, config.Server{}, fmt.Errorf(format, a...)
	}
	srv, ok := cfg.Server(flags.server)
	if !ok {
		return fail("unknown server %q; add it under `servers:` in %s (a name and an ssh_host) and try again", flags.server, cfgPath)
	}
	projectPath := ""
	if len(args) == 1 {
		projectPath = args[0]
	}
	name := flags.name
	if srv.Name == config.LocalServer {
		if projectPath == "" {
			projectPath = "."
		}
		abs, err := filepath.Abs(projectPath)
		if err != nil {
			return fail("resolve %s: %w", projectPath, err)
		}
		projectPath = abs
		if name == "" {
			name = filepath.Base(projectPath)
		}
	} else {
		if !path.IsAbs(projectPath) {
			return fail("a project on a server needs its absolute path there: nearhost add /path/on/server --server %s", srv.Name)
		}
		projectPath = path.Clean(projectPath)
		if name == "" {
			name = path.Base(projectPath)
		}
	}
	if name == "" || strings.ContainsAny(name, `/\`) || name == "." {
		return fail("cannot name a project after %s; pass --name", projectPath)
	}
	if other, ok := cfg.Project(name); ok {
		return fail("project %q already exists (%s:%s); pass --name to add this one under another name, or remove the old one from %s", name, other.Server, other.Path, cfgPath)
	}
	// The hosts file gets the alias without blanks around it; the config
	// must hold the same, as the proxy serves the alias as configured.
	alias := strings.TrimSpace(flags.alias)
	if alias == "" {
		alias = config.AliasFor(name)
	}
	if _, err := hosts.Rewrite("", []string{alias}); err != nil {
		return fail("%w; pass another --alias", err)
	}
	for _, other := range cfg.Projects {
		if strings.EqualFold(other.Alias, alias) {
			return fail("alias %s is already used by project %q; pass another --alias", alias, other.Name)
		}
	}
	return config.Project{Name: name, Server: srv.Name, Path: projectPath, Alias: alias, Enabled: !flags.disabled}, srv, nil
}

// openProject returns a reader for the project's files and a function to call
// when done with it.
func openProject(ctx context.Context, b addBackend, srv config.Server, projectPath string) (discovery.FileReader, func() error, error) {
	if srv.Name != config.LocalServer {
		return b.openRemote(ctx, srv, projectPath)
	}
	info, err := os.Stat(projectPath)
	if err != nil {
		return nil, nil, err
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("%s is not a directory", projectPath)
	}
	return discovery.DirReader{Root: projectPath}, func() error { return nil }, nil
}

// forceWeb makes port the only web route, adding it if it was not discovered.
func forceWeb(routes []config.Route, port int) []config.Route {
	found := false
	for i := range routes {
		if routes[i].Port == port {
			routes[i].Role = config.RoleWeb
			found = true
		} else if routes[i].Role == config.RoleWeb {
			routes[i].Role = config.RoleIgnore
		}
	}
	if !found {
		routes = append(routes, config.Route{Port: port, Role: config.RoleWeb, Label: "web (--web)"})
	}
	return routes
}

func printRoutes(out io.Writer, p config.Project) error {
	if len(p.Routes) == 0 {
		_, err := fmt.Fprintf(out, "No ports found in the files of %s (%s:%s).\n", p.Name, p.Server, p.Path)
		return err
	}
	fmt.Fprintf(out, "Routes found for %s (%s:%s):\n", p.Name, p.Server, p.Path)
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "  PORT\tROLE\tLABEL")
	for _, r := range p.Routes {
		fmt.Fprintf(tw, "  %d\t%s\t%s\n", r.Port, r.Role, r.Label)
	}
	return tw.Flush()
}

// loopbackClash refuses an enabled project that forwards a loopback port an
// enabled project already forwards: only one of them could bind it.
func loopbackClash(cfg *config.Config, p config.Project) error {
	if !p.Enabled {
		return nil
	}
	for _, r := range p.Routes {
		if r.Role != config.RoleLoopback {
			continue
		}
		for _, other := range cfg.Projects {
			if !other.Enabled {
				continue
			}
			for _, o := range other.Routes {
				if o.Role == config.RoleLoopback && o.Port == r.Port {
					return fmt.Errorf("loopback port %d is already forwarded for project %q, and only one project can bind it; nothing was saved. Give this project its own port (see the advisory above) or add it with --disabled", r.Port, other.Name)
				}
			}
		}
	}
	return nil
}
