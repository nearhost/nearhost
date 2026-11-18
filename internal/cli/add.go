package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/nearhost/nearhost/internal/agentapi"
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
	// running reports which Nearhost, if any, is serving on this machine, so
	// that add can say whether it picks the new project up. Nil means none.
	running func() runMode
}

// runMode is which Nearhost, if any, answers on this machine's agent socket.
type runMode int

const (
	// modeNone is no Nearhost answering.
	modeNone runMode = iota
	// modeInterface is the interface, which watches the config and applies
	// a change to it.
	modeInterface
	// modeUp is `nearhost up`, which reads the config once.
	modeUp
	// modeUnknown is a Nearhost that answers without saying which it is, as
	// an older one does.
	modeUnknown
)

// agentRunMode asks the agent API on the socket at path which Nearhost
// serves it. A socket nothing answers on is modeNone; an answer that does not
// say whether the interface is running is modeUnknown.
func agentRunMode(path string) runMode {
	const timeout = 500 * time.Millisecond
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: timeout}).DialContext(ctx, "unix", path)
		}},
	}
	defer client.CloseIdleConnections()
	resp, err := client.Get("http://nearhost/v1")
	if err != nil {
		// A refused dial means nothing is there. A socket that accepts but
		// does not answer in time is still something running.
		if !agentapi.Answers(path) {
			return modeNone
		}
		return modeUnknown
	}
	defer resp.Body.Close()
	var hello struct {
		Interface *bool `json:"interface"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&hello) != nil || hello.Interface == nil {
		return modeUnknown
	}
	if *hello.Interface {
		return modeInterface
	}
	return modeUp
}

// systemAdd is the production backend. Only NewRoot wires it in.
func systemAdd() addBackend {
	return addBackend{
		openRemote: sshClient{}.openRemote,
		presets:    systemPresets(),
		lookup:     hosts.DNSLookup,
		running:    systemRunMode,
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

// rawPort keeps a port flag's text, so that RunE can refuse a word with a
// clear message. It shows as an int in help.
type rawPort string

func (r *rawPort) String() string { return string(*r) }

func (r *rawPort) Set(v string) error { *r = rawPort(v); return nil }

func (r *rawPort) Type() string { return "int" }

func newAddCmd(b addBackend) *cobra.Command {
	var flags addFlags
	// web is read as text so that a word gets a clear message, not the flag
	// parser's; flags.web stays an int for the rest of add.
	var web rawPort
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
			if cmd.Flags().Changed("web") {
				port, err := strconv.Atoi(strings.TrimSpace(string(web)))
				if err != nil {
					return fmt.Errorf("--web must be a port number, not %q", web)
				}
				if port < 1 || port > 65535 {
					return fmt.Errorf("--web must be a port between 1 and 65535, not %d", port)
				}
				flags.web = port
			}
			if flags.noPresets && len(flags.presets) > 0 {
				return errors.New("--no-presets and --preset contradict each other; pass one of them")
			}
			return runAdd(cmd, b, flags, args)
		},
	}
	cmd.Flags().StringVar(&flags.server, "server", config.LocalServer, "server the project runs on")
	cmd.Flags().StringVar(&flags.name, "name", "", "project name (default: directory name)")
	cmd.Flags().StringVar(&flags.alias, "alias", "", "main name (default: <name>.test); any domain, typing it approves it")
	cmd.Flags().Var((*rawPort)(&web), "web", "force this port to be the web route")
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
		fmt.Fprintf(out, "Read %s as %s on %s, since the shell turned ~ into this machine's home.\n\n", printable(project.Path), printable(opened), printable(srv.Name))
	}
	project.Path = opened
	// A name with characters a .test name can't hold, typed or from the
	// directory, would lose them in the alias made from it. It is refused
	// unless the main name comes from --alias or the project's .nearhostrc;
	// a file that can't be read is reported when it is applied.
	if strings.TrimSpace(flags.alias) == "" && config.LosesInAlias(project.Name) {
		if f, found, err := nearhostrc.Read(reader); err == nil && (!found || f.Alias == "") {
			return errors.Join(fmt.Errorf("the name %q holds characters a .test name can't use; pass --name with a-z, 0-9 and -, or --alias for the full domain", project.Name), done())
		}
	}
	res, warnings, err := discoverWithPresets(cmd, b, flags, cfg, cfgPath, reader, answers)
	if err != nil {
		return errors.Join(err, done())
	}
	project.Routes = discoveredRoutes(res)
	if flags.web == 0 && res.LonePortNote != "" {
		// With --web the user already said which port is the app. Export
		// and doctor read the same discovery, and --web advice is no use there.
		warnings = append(warnings, res.LonePortNote)
	}
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
	defaultAlias := project.Alias
	refused, err := applyNearhostrc(cmd, b, cfg, reader, answers, &project, flags.hosts, strings.TrimSpace(flags.alias) != "")
	if err != nil {
		return errors.Join(err, done())
	}
	// A main name from the .nearhostrc must be free, as a typed one must.
	if project.Alias != defaultAlias {
		var aliasTaken *aliasTakenError
		switch err := aliasProblem(cfg, project.Alias); {
		case errors.As(err, &aliasTaken):
			return errors.Join(fmt.Errorf("%s asks for alias %s, which is already used by project %q; pass another --alias", nearhostrc.FileName, project.Alias, aliasTaken.other.Name), done())
		case err != nil:
			return errors.Join(err, done())
		}
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
	advisories, advisoryWarnings := discovery.Advise(res, project.Alias, project.AllHosts(), cfg.Projects)
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

	err = config.Update(cfgPath, func(cfg *config.Config) error {
		// Another add may have saved the server meanwhile: the same server
		// is used as it is, and one reached another way is not the one the
		// project was read from.
		if saved, ok := cfg.Server(srv.Name); newServer && ok {
			if saved.SSHHost != srv.SSHHost {
				return fmt.Errorf("server %s was added meanwhile, reached with `ssh %s`, not `ssh %s` as typed; nothing was saved. Run the add again", printable(srv.Name), printable(saved.SSHHost), printable(srv.SSHHost))
			}
			newServer = false
		} else if newServer {
			cfg.Servers = append(cfg.Servers, srv)
		}
		return addProjectTo(cfg, project)
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(out)
	if newServer {
		fmt.Fprintf(out, "Added server %s, reached with `ssh %s`.\n", printable(srv.Name), printable(srv.SSHHost))
	}
	fmt.Fprintf(out, "Added %s on %s at https://%s\n", printable(project.Name), printable(project.Server), printable(project.Alias))
	if len(refused) > 0 {
		names := make([]string, len(refused))
		for i, n := range refused {
			names[i] = printable(n)
		}
		fmt.Fprintf(out, "Not approved, so left out: %s.\n", strings.Join(names, ", "))
	}
	start := "nearhost up"
	if !project.Enabled {
		start = config.UpCommand(project.Name)
	}
	_, err = fmt.Fprintln(out, runningAdvice(b.running, start))
	return err
}

// applyNearhostrc applies the project's .nearhostrc, if it has one, and the
// --host flags to project, and asks before serving any host that would take
// over traffic meant for the internet, whether the file, a preset or the
// project's files gave it. A port the file lists takes the file's hosts and
// none inferred; without a file, the inferred hosts stay. A host typed with
// --host counts as approved by being typed, and so is a main name given with
// --alias. The file's alias is the project's main name unless --alias gave
// one: the flag wins, and the snapshot still records the file's, so a later
// start keeps the flag's until the file moves its alias. An inferred host
// another project in cfg serves is left out, with a note on stderr. Each
// item that needs approval is asked about on its own, and one the user
// refuses is dropped from project; the names of those are returned. Nothing
// is saved here; an error stops the add.
func applyNearhostrc(cmd *cobra.Command, b addBackend, cfg *config.Config, reader discovery.FileReader, answers *bufio.Reader, project *config.Project, typed []string, aliasTyped bool) ([]string, error) {
	out := cmd.OutOrStdout()
	// The main name before the file is applied is the one a refused main
	// name from the file goes back to.
	defaultAlias := project.Alias
	f, found, err := nearhostrc.Read(reader)
	if err != nil {
		return nil, err
	}
	// Without a file, or with one that lists no routes, the hosts on the
	// routes now are the ones discovery inferred; with routes in the file,
	// every host comes from it.
	rcRoutes := found && len(f.Routes) > 0
	inferred := map[string]bool{}
	if !rcRoutes {
		for _, r := range project.Routes {
			for _, h := range r.Hosts {
				inferred[strings.ToLower(h)] = true
			}
		}
	}
	if found {
		typedAlias := project.Alias
		// The file's routes win over hosts inferred from presets and project
		// files: Merge would keep them as hand-added ones on the ports it
		// lists. A file without routes leaves them.
		stripped := *project
		stripped.Routes = rcBase(project.Routes, f)
		merged, err := nearhostrc.Merge(stripped, f, stripped.Routes)
		if err != nil {
			return nil, err
		}
		*project = merged
		if aliasTyped {
			project.Alias = typedAlias
		}
	}
	// The main name is final now, so a route other than the primary that
	// names it is dropped before anything is asked about it.
	project.Routes = config.WithoutAliasHosts(project.Routes, project.Alias)
	// A host typed twice, or already on a route, is served once: it still
	// counts as typed, so it is approved, but is not added again. Hosts are
	// matched without regard to case, and typed ones are lower-cased so that
	// the way they are written cannot make them differ.
	approved := map[string]bool{}
	var extra []string
	for _, h := range typed {
		h = strings.ToLower(strings.TrimSpace(h))
		if err := config.ValidProjectHost(h); err != nil {
			return nil, err
		}
		if !approved[h] {
			approved[h] = true
			extra = append(extra, h)
		}
	}
	if aliasTyped {
		approved[strings.ToLower(project.Alias)] = true
	}
	onRoute := map[string]bool{}
	for _, r := range project.Routes {
		for _, h := range r.Hosts {
			onRoute[strings.ToLower(h)] = true
		}
	}
	var add []string
	for _, h := range extra {
		if !onRoute[h] {
			add = append(add, h)
		}
	}
	if len(add) > 0 {
		// The routes are copied first: they may share an array with the
		// discovery result.
		routes := append([]config.Route(nil), project.Routes...)
		for i := range routes {
			if routes[i].Role == config.RoleWeb {
				routes[i].Hosts = append(append([]string(nil), routes[i].Hosts...), add...)
				break
			}
		}
		project.Routes = routes
	}
	// An inferred host another project serves is left out with a note, so
	// it is neither asked about nor fails the save; a typed one still fails.
	// A disabled project serves nothing, so it keeps them.
	if project.Enabled {
		for h := range approved {
			delete(inferred, h)
		}
		routes, notes := withoutTakenHosts(cfg, *project, inferred)
		project.Routes = routes
		for _, n := range notes {
			fmt.Fprintln(cmd.ErrOrStderr(), printable(n))
		}
	}

	var items []nearhostrc.Item
	for _, it := range nearhostrc.NeedsApproval(config.Project{}, *project) {
		// Only a host can be typed with --host; an option is always asked.
		if it.Host == "" || !approved[strings.ToLower(it.Host)] {
			items = append(items, it)
		}
	}
	if len(items) == 0 {
		return nil, nil
	}
	if answers == nil {
		names := make([]string, len(items))
		for i, it := range items {
			names[i] = it.Name()
		}
		return nil, fmt.Errorf("not added: this project asks for things that need your approval (%s); run nearhost add in a terminal, or open nearhost", strings.Join(names, ", "))
	}
	// The note is about names, so a prompt about only loopback ports and
	// options goes without it.
	for _, it := range items {
		if it.Host != "" {
			fmt.Fprintln(out, approvalNote)
			break
		}
	}
	// A host listed twice, as a route host and as the main name, is asked
	// about once, and the answer covers both.
	asked := map[string]bool{}
	var refused []string
	for _, it := range items {
		name := strings.ToLower(it.Name())
		if asked[name] {
			continue
		}
		asked[name] = true
		fmt.Fprintf(out, "%s: approve? [y/N] ", approvalItem(cmd.Context(), b.lookup, it))
		if saidYesDefaultNo(answers, out) {
			continue
		}
		refused = append(refused, it.Name())
		dropRefused(project, it, defaultAlias)
	}
	return refused, nil
}

// approvalNote is said once before the first approval question when a host
// or main name is among the items: what saying yes to a name does on this
// machine.
const approvalNote = "Approving a name puts it in your hosts file on this machine, has Nearhost's local CA sign a certificate for it, and makes your browser send that site's cookies to this project."

// approvalItem is how an approval question names it: a host with its port,
// why it needs approval and where DNS points it, or an option with why.
func approvalItem(ctx context.Context, lookup hosts.Lookup, it nearhostrc.Item) string {
	if it.Host == "" {
		return fmt.Sprintf("%s (%s)", printable(it.Name()), it.Why)
	}
	addr, err := lookupPublic(ctx, lookup, it.Host)
	switch {
	case err != nil:
		return fmt.Sprintf("%s on port %d (%s, could not check DNS)", printable(it.Host), it.Port, it.Why)
	case addr != "":
		return fmt.Sprintf("%s on port %d (%s, resolves to %s on the internet)", printable(it.Host), it.Port, it.Why, addr)
	}
	return fmt.Sprintf("%s on port %d (%s)", printable(it.Host), it.Port, it.Why)
}

// dropRefused takes the item the user refused out of project. A host leaves
// every route that lists it, and a main name it refused goes back to
// defaultAlias; a web route other than the primary left without hosts is
// ignored, as withoutTakenHosts does. A loopback port is ignored, as a new
// project has no approved role for it to go back to. docker_clients stays
// off, and a refused main name leaves the snapshot's alias empty. An item
// that came from the project's .nearhostrc also leaves the snapshot without
// the file's hash, so that the next review of the file does not take it as
// approved and asks about the item again.
func dropRefused(project *config.Project, it nearhostrc.Item, defaultAlias string) {
	if fromFile(project.Nearhostrc, it) {
		project.Nearhostrc = project.Nearhostrc.Clone()
		project.Nearhostrc.SHA256 = ""
	}
	switch {
	case it.Host != "":
		routes := make([]config.Route, len(project.Routes))
		primary := true
		for i, r := range project.Routes {
			isPrimary := primary && r.Role == config.RoleWeb
			if r.Role == config.RoleWeb {
				primary = false
			}
			var kept []string
			for _, h := range r.Hosts {
				if !strings.EqualFold(h, it.Host) {
					kept = append(kept, h)
				}
			}
			if len(kept) != len(r.Hosts) {
				r.Hosts = kept
				if len(kept) == 0 && r.Role == config.RoleWeb && !isPrimary {
					r.Role = config.RoleIgnore
				}
			}
			routes[i] = r
		}
		project.Routes = routes
		if strings.EqualFold(project.Alias, it.Host) {
			project.Alias = defaultAlias
			if project.Nearhostrc != nil {
				project.Nearhostrc = project.Nearhostrc.Clone()
				project.Nearhostrc.Alias = ""
			}
		}
	case it.Option == nearhostrc.OptionLoopback:
		routes := append([]config.Route(nil), project.Routes...)
		for i := range routes {
			if routes[i].Port == it.Port && routes[i].Role == config.RoleLoopback {
				routes[i].Role = config.RoleIgnore
			}
		}
		project.Routes = routes
	case it.Option == nearhostrc.OptionDockerClients:
		if project.Nearhostrc != nil {
			project.Nearhostrc = project.Nearhostrc.Clone()
			project.Nearhostrc.DockerClients = false
		}
	}
}

// fromFile reports whether the .nearhostrc that snap was taken from asked for
// it: a host one of its routes lists, its main name, a loopback port or
// docker_clients. A host a preset or the project's files gave is not.
func fromFile(snap *config.RcSnapshot, it nearhostrc.Item) bool {
	if snap == nil {
		return false
	}
	if it.Host == "" {
		return true
	}
	if strings.EqualFold(snap.Alias, it.Host) {
		return true
	}
	for _, r := range snap.Routes {
		for _, h := range r.Hosts {
			if strings.EqualFold(h, it.Host) {
				return true
			}
		}
	}
	return false
}

// firstPublic is the first address DNS gives host that is not loopback. ok
// is false for a pattern, when lookup is nil, when the lookup fails and when
// every address is loopback, since the warning it feeds is a courtesy.
func firstPublic(ctx context.Context, lookup hosts.Lookup, host string) (addr string, ok bool) {
	addr, err := lookupPublic(ctx, lookup, host)
	return addr, err == nil && addr != ""
}

// lookupPublic is the first address DNS gives host that is not loopback, or
// empty when there is none, when host is a pattern and when lookup is nil.
// The error is the lookup's: DNS could not be checked.
func lookupPublic(ctx context.Context, lookup hosts.Lookup, host string) (string, error) {
	if lookup == nil || hostname.IsPattern(host) {
		return "", nil
	}
	addrs, err := lookup(ctx, host)
	if err != nil {
		return "", err
	}
	for _, a := range addrs {
		if !a.IsLoopback() {
			return a.String(), nil
		}
	}
	return "", nil
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
			if err := savePresets(cfgPath, cfg, name); err != nil {
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
		fmt.Fprintf(tw, "  %d) %s\t%d\n", i+1, printable(r.Label), r.Port)
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
			name = defaultName(srv, projectPath)
		}
	} else {
		if !path.IsAbs(projectPath) && projectPath != "~" && !strings.HasPrefix(projectPath, "~/") {
			return fail("a project on a server needs its absolute path there, or one under ~/: nearhost add /path/on/server --server %s", srv.Name)
		}
		projectPath = path.Clean(projectPath)
		if name == "" {
			name = defaultName(srv, projectPath)
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
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", nil, fmt.Errorf("no directory at %s", projectPath)
	}
	if err != nil {
		return nil, "", nil, err
	}
	if !info.IsDir() {
		return nil, "", nil, fmt.Errorf("%s is not a directory", projectPath)
	}
	return discovery.DirReader{Root: projectPath}, projectPath, func() error { return nil }, nil
}

// forceWeb makes port the primary web route, adding it before the first web
// route if it was not discovered. Another web route with hosts stays web,
// since a route with hosts cannot be ignored; one without becomes ignore.
func forceWeb(routes []config.Route, port int) []config.Route {
	out := make([]config.Route, len(routes))
	found := false
	for i, r := range routes {
		if r.Port == port {
			r.Role = config.RoleWeb
			found = true
		} else if r.Role == config.RoleWeb && len(r.Hosts) == 0 {
			r.Role = config.RoleIgnore
		}
		out[i] = r
	}
	if !found {
		added := config.Route{Port: port, Role: config.RoleWeb, Label: "web (--web)"}
		at := len(out)
		for i, r := range out {
			if r.Role == config.RoleWeb {
				at = i
				break
			}
		}
		out = append(out[:at], append([]config.Route{added}, out[at:]...)...)
	}
	return config.MovePrimary(out, port)
}

// printRoutes prints the routes found for p. Its name and path can come
// from a server and its labels from the project's files, or from a config
// saved by a version that did not clean them, so all are made printable.
func printRoutes(out io.Writer, p config.Project, composeUnread bool) error {
	name, server, dir := printable(p.Name), printable(p.Server), printable(p.Path)
	if len(p.Routes) == 0 && composeUnread {
		_, err := fmt.Fprintf(out, "No port variables found in the .env files of %s (%s:%s).\n"+
			"Its Compose file is read only when a preset asks for it with compose: ports.\n", name, server, dir)
		return err
	}
	if len(p.Routes) == 0 {
		_, err := fmt.Fprintf(out, "No ports found in the files of %s (%s:%s).\n", name, server, dir)
		return err
	}
	fmt.Fprintf(out, "Routes found for %s (%s:%s):\n", name, server, dir)
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
			fmt.Fprintf(tw, "  %d\t%s\t%s\t%s\n", r.Port, r.Role, printable(strings.Join(r.Hosts, ", ")), printable(r.Label))
		} else {
			fmt.Fprintf(tw, "  %d\t%s\t%s\n", r.Port, r.Role, printable(r.Label))
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

// runningAdvice is the last line of add: what the user does next, given the
// Nearhost that is running, if any. start is the command that starts the
// project when nothing runs.
func runningAdvice(running func() runMode, start string) string {
	mode := modeNone
	if running != nil {
		mode = running()
	}
	switch mode {
	case modeInterface:
		return "The running Nearhost picks it up in a moment."
	case modeUp:
		return "nearhost up is running and does not reload. Stop it and start it again to serve this project."
	case modeUnknown:
		return "Nearhost is running. If it does not pick the project up in a moment, stop it and start it again."
	}
	return fmt.Sprintf("Start it with `%s`.", start)
}

// systemRunMode is the production probe: this machine's own agent socket.
// When the socket cannot be worked out it says nothing runs, which leaves
// the plain advice to start Nearhost.
func systemRunMode() runMode {
	id, err := config.InstallID()
	if err != nil {
		return modeNone
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return modeNone
	}
	return agentRunMode(agentSocket(home, id))
}
