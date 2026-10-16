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
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/discovery"
	"github.com/nearhost/nearhost/internal/hostname"
	"github.com/nearhost/nearhost/internal/hosts"
	"github.com/nearhost/nearhost/internal/nearhostrc"
)

// addBackend is the seam between `add` and what lies outside the process:
// a remote server, the trusted preset registries and the terminal.
type addBackend struct {
	// openRemote opens the project directory on a server for reading, and
	// returns the directory it opened: a path under ~ is resolved on the
	// server. The returned function ends the connection.
	openRemote func(ctx context.Context, srv config.Server, projectPath string) (discovery.FileReader, string, func() error, error)
	// presets reaches the trusted preset registries.
	presets presetBackend
	// interactive reports whether a person can be asked a question. Nil
	// means no.
	interactive func() bool
	// lookup finds the addresses DNS gives a host, so that approving a real
	// domain can show where it points today. Nil means no lookup, and no
	// warning.
	lookup hosts.Lookup
}

// systemAdd is the production backend. Only NewRoot wires it in.
func systemAdd() addBackend {
	return addBackend{
		openRemote: sshClient{}.openRemote,
		presets:    systemPresets(),
		lookup:     hosts.DNSLookup,
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
	hosts               []string
	noPresets           bool
}

func newAddCmd(b addBackend) *cobra.Command {
	var flags addFlags
	cmd := &cobra.Command{
		Use:   "add [path]",
		Short: "Add a project, discovering its ports from its files",
		Long: "Add the project in path (default: the current directory) to the config,\n" +
			"with routes proposed from the port variables in its .env and from the\n" +
			"presets that are enabled, which can also ask for Compose published ports.\n" +
			"When a preset from a trusted registry suits the project and is not\n" +
			"enabled, add offers to enable it; while no registry is trusted and add\n" +
			"runs at a terminal, it first asks whether to trust the official one.\n" +
			"With --server the path is on that server and its files are read over\n" +
			"SFTP; a server not in the config is offered to be added, at a terminal.\n" +
			"Nothing is started and no project file is changed.",
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
	cmd.Flags().StringArrayVar(&flags.hosts, "host", nil, "add this host to the web route (repeatable); typing it approves it")
	cmd.Flags().BoolVar(&flags.noPresets, "no-presets", false, "use no presets and suggest none")
	return cmd
}

func runAdd(cmd *cobra.Command, b addBackend, flags addFlags, args []string) error {
	cfg, cfgPath, err := loadConfig(cmd)
	if err != nil {
		return fmt.Errorf("%w; fix the config before adding to it", err)
	}
	// Every question reads its answer from the same input, so that lines
	// typed ahead reach the questions in the order they are asked.
	var answers *bufio.Reader
	if b.interactive != nil && b.interactive() {
		answers = bufio.NewReader(cmd.InOrStdin())
	}
	out := cmd.OutOrStdout()
	// A server the config does not have can be added when someone can
	// answer, once the project itself is known to be one the config can
	// hold. It is saved with the project, so that a failure on the way, or a
	// save made for another reason such as enabling a preset, leaves it out.
	srv, known := cfg.Server(flags.server)
	unknown := fmt.Errorf("unknown server %q; add it under `servers:` in %s (a name and an ssh_host) and try again", flags.server, cfgPath)
	if !known && (answers == nil || strings.TrimSpace(flags.server) == "") {
		return unknown
	}
	if !known {
		srv = config.Server{Name: flags.server}
	}
	project, err := newProject(cfg, cfgPath, srv, flags, args)
	if err != nil {
		return err
	}
	newServer := !known
	if newServer {
		host, ok := askToAddServer(answers, out, srv.Name)
		if !ok {
			return unknown
		}
		srv.SSHHost = host
	}

	reader, opened, done, err := openProject(cmd.Context(), b, srv, project.Path)
	if err != nil {
		return err
	}
	if opened != project.Path && !strings.HasPrefix(project.Path, "~") {
		fmt.Fprintf(out, "Read %s as %s on %s, since the shell turned ~ into this machine's home.\n\n", project.Path, opened, srv.Name)
	}
	project.Path = opened
	res, warnings, err := discoverWithPresets(cmd, b, flags, cfg, cfgPath, reader, answers)
	if err != nil {
		return errors.Join(err, done())
	}
	project.Routes = discoveredRoutes(res)
	// Several routes were found and none says it is the web route: ask,
	// when someone can answer. Otherwise the error below says how to choose.
	if _, found := project.WebRoute(); !found && flags.web == 0 && len(project.Routes) > 1 && answers != nil {
		if i := chooseWeb(answers, out, project.Routes); i >= 0 {
			flags.web = project.Routes[i].Port
		}
	}
	if flags.web > 0 {
		project.Routes = forceWeb(project.Routes, flags.web)
	}
	if err := applyNearhostrc(cmd, b, reader, answers, &project, flags.hosts); err != nil {
		return errors.Join(err, done())
	}
	// The advisories are about the roles the project ends up with. Merging
	// and forceWeb keep the discovered routes' ports, so the roles are
	// matched by port.
	roles := map[int]config.Role{}
	for _, r := range project.Routes {
		roles[r.Port] = r.Role
	}
	for i := range res.Routes {
		if role, ok := roles[res.Routes[i].Port]; ok {
			res.Routes[i].Role = role
		}
	}
	// Advisories may read the project's files, so they are worked out
	// before the reader is closed.
	advisories, advisoryWarnings := discovery.Advise(res, project.Alias, cfg.Projects)
	if err := done(); err != nil {
		return err
	}
	printWarnings(cmd.ErrOrStderr(), warnings)

	if err := printRoutes(out, project, res.ComposeUnread()); err != nil {
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

	if newServer {
		cfg.Servers = append(cfg.Servers, srv)
	}
	if err := saveProject(cfg, cfgPath, project); err != nil {
		return err
	}
	fmt.Fprintln(out)
	if newServer {
		fmt.Fprintf(out, "Added server %s, reached with `ssh %s`.\n", printable(srv.Name), printable(srv.SSHHost))
	}
	fmt.Fprintf(out, "Added %s on %s at https://%s\n", project.Name, project.Server, project.Alias)
	start := "nearhost up"
	if !project.Enabled {
		start = upCommand(project.Name)
	}
	_, err = fmt.Fprintf(out, "Start it with `%s`.\n", start)
	return err
}

// applyNearhostrc applies the project's .nearhostrc, if it has one, and the
// --host flags to project, and asks before serving any host that would take
// over traffic meant for the internet. A host typed with --host counts as
// approved by being typed. Nothing is saved here; an error stops the add.
func applyNearhostrc(cmd *cobra.Command, b addBackend, reader discovery.FileReader, answers *bufio.Reader, project *config.Project, typed []string) error {
	out := cmd.OutOrStdout()
	f, found, err := nearhostrc.Read(reader)
	if err != nil {
		return err
	}
	if found {
		merged, err := nearhostrc.Merge(*project, f, project.Routes)
		if err != nil {
			return err
		}
		*project = merged
	}
	approved := map[string]bool{}
	for _, h := range typed {
		if err := hostname.Valid(h); err != nil {
			return err
		}
		approved[strings.ToLower(h)] = true
	}
	if len(typed) > 0 {
		// The routes are copied first: they may share an array with the
		// discovery result.
		routes := append([]config.Route(nil), project.Routes...)
		for i := range routes {
			if routes[i].Role == config.RoleWeb {
				routes[i].Hosts = append(append([]string(nil), routes[i].Hosts...), typed...)
				break
			}
		}
		project.Routes = routes
	}

	var items []nearhostrc.Item
	for _, it := range nearhostrc.NeedsApproval(config.Project{}, *project) {
		if !approved[strings.ToLower(it.Host)] {
			items = append(items, it)
		}
	}
	if len(items) == 0 {
		return nil
	}
	if answers == nil {
		names := make([]string, len(items))
		for i, it := range items {
			names[i] = it.Host
		}
		return fmt.Errorf("not added: .nearhostrc asks for hosts that need your approval (%s); run nearhost add in a terminal, or open nearhost", strings.Join(names, ", "))
	}
	fmt.Fprintln(out, ".nearhostrc asks Nearhost to serve:")
	for _, it := range items {
		fmt.Fprintf(out, "  %s on port %d (%s%s)\n", printable(it.Host), it.Port, it.Why, publicAddress(cmd.Context(), b.lookup, it.Host))
	}
	fmt.Fprint(out, "Serve these? [y/N] ")
	if !saidYesDefaultNo(answers, out) {
		return errors.New("not added: the hosts in .nearhostrc were not approved")
	}
	return nil
}

// publicAddress is ", resolves to <addr> on the internet" when DNS gives host
// an address that is not loopback, which is what the user most needs to know
// before shadowing it. It is empty for a pattern, when lookup is nil and
// when the lookup fails, since the warning is a courtesy.
func publicAddress(ctx context.Context, lookup hosts.Lookup, host string) string {
	if lookup == nil || hostname.IsPattern(host) {
		return ""
	}
	addrs, err := lookup(ctx, host)
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if !a.IsLoopback() {
			return ", resolves to " + a.String() + " on the internet"
		}
	}
	return ""
}

// discoverWithPresets runs discovery with the enabled presets, then offers
// the presets of the trusted registries that suit the project and runs it
// again with those the user takes. A preset is enabled, and the config
// saved, the moment the user says yes, so the answer holds even if the
// project is not added.
//
// answers is nil when no one can be asked.
func discoverWithPresets(cmd *cobra.Command, b addBackend, flags addFlags, cfg *config.Config, cfgPath string, reader discovery.FileReader, answers *bufio.Reader) (*discovery.Result, []string, error) {
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
	asked := false
	if answers != nil && len(flags.presets) == 0 && len(cfg.Registries) == 0 && !cfg.RegistryPromptOff() {
		asked = true
		note, err := askToTrust(ctx, out, answers, b.presets, cfg, cfgPath)
		if err != nil {
			return nil, nil, err
		}
		if note != "" {
			notes = append(notes, note)
		}
	}
	sources := b.presets.indexes(ctx, cfg, cfgPath)
	notes = append(notes, registryNotes(sources)...)
	interactive := answers != nil
	passed := map[string]bool{} // presets declined, or accepted and not installable
	for {
		res, found, err := discoverOnce(cfg, cfgPath, reader, sources, passed)
		if err != nil {
			return nil, nil, err
		}
		if !asked && len(cfg.Registries) == 0 && len(res.Applied) == 0 && !cfg.RegistryPromptOff() {
			fmt.Fprintf(out, "%s\n\n", noRegistriesHint)
		}
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
			if note := enableOffered(ctx, b.presets, cfg, cfgPath, o.Name); note != "" {
				passed[o.Name] = true
				notes = append(notes, note)
				continue
			}
			enabled = true
		}
		if !enabled {
			return res, append(notes, found...), nil
		}
	}
}

// askToTrust asks whether to trust the official registry and does what the
// answer says. An answer it does not know is asked again; the end of input
// is a no. It returns a note when the registry could not be trusted, and an
// error only when "don't ask again" could not be saved.
func askToTrust(ctx context.Context, out io.Writer, answers *bufio.Reader, b presetBackend, cfg *config.Config, cfgPath string) (string, error) {
	fmt.Fprintf(out, "No preset registries are trusted, so Nearhost will read ports from .env.\n"+
		"You can also set routes yourself in the config.\n"+
		"Trust the official presets (%s) for framework-specific setups?\n", officialRepo)
	for {
		fmt.Fprint(out, "[Y]es / [n]o / [d]on't ask again: ")
		line, err := answers.ReadString('\n')
		if err != nil && line == "" {
			fmt.Fprintln(out)
			return "", nil
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "", "y", "yes":
			return answerRegistry(ctx, out, b, cfg, cfgPath, registryYes)
		case "n", "no":
			return answerRegistry(ctx, out, b, cfg, cfgPath, registryNo)
		case "d", "don't", "dont":
			return answerRegistry(ctx, out, b, cfg, cfgPath, registryNever)
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

// saidYesDefaultNo reads one line as the answer to a yes-or-no question whose
// default is no: serving a host is only approved by saying so. An empty
// answer and the end of input are both a no.
func saidYesDefaultNo(answers *bufio.Reader, out io.Writer) bool {
	line, err := answers.ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(out)
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// chooseWeb lists the routes and asks which one is the web app. It returns
// the index of the route chosen, or -1 for none. An answer out of range is
// asked again; the end of input means none.
func chooseWeb(answers *bufio.Reader, out io.Writer, routes []config.Route) int {
	fmt.Fprintln(out, "Which one is the web app?")
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	for i, r := range routes {
		fmt.Fprintf(tw, "  %d) %s\t%d\n", i+1, r.Label, r.Port)
	}
	tw.Flush()
	fmt.Fprintln(out, "  0) none")
	for {
		fmt.Fprintf(out, "Choose [1-%d, 0]: ", len(routes))
		line, err := answers.ReadString('\n')
		if err != nil && line == "" {
			fmt.Fprintln(out)
			return -1
		}
		if n, convErr := strconv.Atoi(strings.TrimSpace(line)); convErr == nil && n >= 0 && n <= len(routes) {
			return n - 1
		}
	}
}

// askToAddServer offers to add the server called name and asks how ssh
// reaches it, defaulting to its name. A host that ssh would take for an
// option, or that is not one word, is asked again. No, or the end of input,
// means the server is not added.
func askToAddServer(answers *bufio.Reader, out io.Writer, name string) (string, bool) {
	fmt.Fprintf(out, "Server %s is not in the config. Add it? [Y/n]: ", printable(name))
	if !saidYes(answers, out) {
		return "", false
	}
	for {
		fmt.Fprintf(out, "SSH host for %s [%s]: ", printable(name), printable(name))
		line, err := answers.ReadString('\n')
		if err != nil && line == "" {
			fmt.Fprintln(out)
			return "", false
		}
		host := strings.TrimSpace(line)
		if host == "" {
			host = name
		}
		if hostProblem(host) != nil {
			fmt.Fprintln(out, "An SSH host cannot start with - or contain spaces.")
			continue
		}
		return host, true
	}
}

// newProject works out the project's path, name and alias on srv, and
// refuses what the config could not hold. Its routes are still to be found.
func newProject(cfg *config.Config, cfgPath string, srv config.Server, flags addFlags, args []string) (config.Project, error) {
	fail := func(format string, a ...any) (config.Project, error) {
		return config.Project{}, fmt.Errorf(format, a...)
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
		if !path.IsAbs(projectPath) && projectPath != "~" && !strings.HasPrefix(projectPath, "~/") {
			return fail("a project on a server needs its absolute path there, or one under ~/: nearhost add /path/on/server --server %s", srv.Name)
		}
		projectPath = path.Clean(projectPath)
		if name == "" {
			name = path.Base(projectPath)
		}
	}
	var taken *nameTakenError
	switch err := nameProblem(cfg, name); {
	case errors.As(err, &taken):
		return fail("project %q already exists (%s:%s); pass --name to add this one under another name, or remove the old one from %s", name, taken.other.Server, taken.other.Path, cfgPath)
	case err != nil:
		return fail("cannot name a project after %s; pass --name", projectPath)
	}
	// The hosts file gets the alias without blanks around it; the config
	// must hold the same, as the proxy serves the alias as configured.
	alias := strings.TrimSpace(flags.alias)
	if alias == "" {
		alias = config.AliasFor(name)
	}
	var aliasTaken *aliasTakenError
	switch err := aliasProblem(cfg, alias); {
	case errors.As(err, &aliasTaken):
		return fail("alias %s is already used by project %q; pass another --alias", alias, aliasTaken.other.Name)
	case err != nil:
		return fail("%w; pass another --alias", err)
	}
	return config.Project{Name: name, Server: srv.Name, Path: projectPath, Alias: alias, Enabled: !flags.disabled}, nil
}

// openProject returns a reader for the project's files and a function to call
// when done with it.
func openProject(ctx context.Context, b addBackend, srv config.Server, projectPath string) (discovery.FileReader, string, func() error, error) {
	if srv.Name != config.LocalServer {
		return b.openRemote(ctx, srv, projectPath)
	}
	info, err := os.Stat(projectPath)
	if err != nil {
		return nil, "", nil, err
	}
	if !info.IsDir() {
		return nil, "", nil, fmt.Errorf("%s is not a directory", projectPath)
	}
	return discovery.DirReader{Root: projectPath}, projectPath, func() error { return nil }, nil
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

func printRoutes(out io.Writer, p config.Project, composeUnread bool) error {
	if len(p.Routes) == 0 && composeUnread {
		_, err := fmt.Fprintf(out, "No port variables found in the .env files of %s (%s:%s).\n"+
			"Its Compose file is read only when a preset asks for it with compose: ports.\n", p.Name, p.Server, p.Path)
		return err
	}
	if len(p.Routes) == 0 {
		_, err := fmt.Fprintf(out, "No ports found in the files of %s (%s:%s).\n", p.Name, p.Server, p.Path)
		return err
	}
	fmt.Fprintf(out, "Routes found for %s (%s:%s):\n", p.Name, p.Server, p.Path)
	withHosts := false
	for _, r := range p.Routes {
		withHosts = withHosts || len(r.Hosts) > 0
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if withHosts {
		fmt.Fprintln(tw, "  PORT\tROLE\tHOSTS\tLABEL")
	} else {
		fmt.Fprintln(tw, "  PORT\tROLE\tLABEL")
	}
	for _, r := range p.Routes {
		if withHosts {
			fmt.Fprintf(tw, "  %d\t%s\t%s\t%s\n", r.Port, r.Role, printable(strings.Join(r.Hosts, ", ")), r.Label)
		} else {
			fmt.Fprintf(tw, "  %d\t%s\t%s\n", r.Port, r.Role, r.Label)
		}
	}
	return tw.Flush()
}

// loopbackClashError is a loopback port another enabled project forwards.
// Its text is the one `nearhost add` prints.
type loopbackClashError struct {
	port  int
	other string
}

func (e *loopbackClashError) Error() string {
	return fmt.Sprintf("loopback port %d is already forwarded for project %q, and only one project can bind it; nothing was saved. Give this project its own port (see the advisory above) or add it with --disabled", e.port, e.other)
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
					return &loopbackClashError{port: r.Port, other: other.Name}
				}
			}
		}
	}
	return nil
}
