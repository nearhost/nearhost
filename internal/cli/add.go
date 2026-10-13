package cli

import (
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

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/discovery"
	"github.com/nearhost/nearhost/internal/hosts"
)

// addBackend is the seam between `add` and a remote server.
type addBackend struct {
	// openRemote opens the project directory on a server for reading. The
	// returned function ends the connection.
	openRemote func(ctx context.Context, srv config.Server, projectPath string) (discovery.FileReader, func() error, error)
}

// systemAdd is the production backend. Only NewRoot wires it in.
func systemAdd() addBackend {
	return addBackend{openRemote: sshClient{}.openRemote}
}

type addFlags struct {
	server, name, alias string
	web                 int
	disabled            bool
}

func newAddCmd(b addBackend) *cobra.Command {
	var flags addFlags
	cmd := &cobra.Command{
		Use:   "add [path]",
		Short: "Add a project, discovering its ports from .env, Compose and Vite config",
		Long: "Add the project in path (default: the current directory) to the config,\n" +
			"with routes proposed from its .env, Compose and Vite files. With --server the\n" +
			"path is on that server and its files are read over SFTP. Nothing is started\n" +
			"and nothing outside the config file is changed.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("web") && (flags.web < 1 || flags.web > 65535) {
				return fmt.Errorf("--web must be a port between 1 and 65535, not %d", flags.web)
			}
			return runAdd(cmd, b, flags, args)
		},
	}
	cmd.Flags().StringVar(&flags.server, "server", config.LocalServer, "server the project runs on")
	cmd.Flags().StringVar(&flags.name, "name", "", "project name (default: directory name)")
	cmd.Flags().StringVar(&flags.alias, "alias", "", "hostname (default: <name>.test)")
	cmd.Flags().IntVar(&flags.web, "web", 0, "force this port to be the web route")
	cmd.Flags().BoolVar(&flags.disabled, "disabled", false, "add it disabled: 'nearhost up' starts it only when named")
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
	res, err := discovery.Discover(reader)
	if err := errors.Join(err, done()); err != nil {
		return err
	}
	project.Routes = res.ConfigRoutes()
	if flags.web > 0 {
		project.Routes = forceWeb(project.Routes, flags.web)
		// The advisories are about the roles the project ends up with.
		// forceWeb keeps the discovered routes in place and only appends.
		for i := range res.Routes {
			res.Routes[i].Role = project.Routes[i].Role
		}
	}

	out := cmd.OutOrStdout()
	if err := printRoutes(out, project); err != nil {
		return err
	}
	if _, ok := project.WebRoute(); !ok {
		return fmt.Errorf("could not tell which port serves %s; nothing was saved. Run it again with --web <port>", project.Name)
	}
	for _, a := range discovery.Advise(res, project.Alias, cfg.Projects) {
		fmt.Fprintf(out, "\nAdvisory (%s): %s\n  %s\n", a.Code, a.Message, strings.ReplaceAll(a.Snippet, "\n", "\n  "))
	}

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
		_, err := fmt.Fprintf(out, "No ports found in the .env, Compose or Vite files of %s (%s:%s).\n", p.Name, p.Server, p.Path)
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
