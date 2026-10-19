package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/hostname"
	"github.com/nearhost/nearhost/internal/nearhostrc"
)

// The steps of the add-project form.
const (
	projectStepServer = iota
	projectStepPath
	projectStepRegistry
	projectStepPresets
	projectStepRoutes
	projectStepDetails
	projectStepConfirm
)

var projectStepTitles = map[int]string{
	projectStepServer:   "server",
	projectStepPath:     "path",
	projectStepRegistry: "presets",
	projectStepPresets:  "presets",
	projectStepRoutes:   "routes",
	projectStepDetails:  "name",
	projectStepConfirm:  "confirm",
}

// registryChoices are the answers to the registry question, in order.
var registryChoices = []struct {
	label  string
	answer RegistryAnswer
}{{"Yes", RegistryYes}, {"No", RegistryNo}, {"Don't ask again", RegistryNever}}

// projectForm adds a project: the server, the path there, what discovery
// finds (with the presets the registries suggest), the routes, the name and
// alias, and a last look before it is saved.
type projectForm struct {
	b   Backend
	cfg *config.Config
	dir string // where nearhost was started

	step    int
	servers []config.Server // Local, then the configured servers
	cursor  int             // the server step's row; len(servers) is "New server…"
	sub     *serverForm     // the add-server form, while open from here

	srv      config.Server
	files    ServerFiles // open from the path step on; nil before, or when connecting failed
	path     textField
	resolved string
	listNote string             // why completion found nothing
	conn     int                // numbers the files open, so a listing for files since closed is dropped
	listings map[string]listing // the directories listed, by parent as typed
	chosen   int                // the suggestion chosen among the candidates

	choice   int             // the registry step's row
	draft    Draft           // what discovery found last
	offered  map[string]bool // presets offered once already
	ticked   []bool
	tickRow  int
	routes   []config.Route
	routeRow int
	webPort  textField // asked for when discovery found no routes
	// hosts edits the selected web route's hosts while editingHosts; what
	// is wrong with them is hostsErr.
	hosts        textField
	editingHosts bool
	hostsErr     string
	typed        map[string]bool // hosts typed on the routes step, approved by being typed

	name, alias       textField
	aliasEdited       bool // the alias was typed, so it no longer follows the name
	aliasFromRc       bool // the alias is the .nearhostrc's, so it does not follow the name either
	focus             int  // 0 name, 1 alias
	nameErr, aliasErr string
	advice            []Advisory     // for the routes and alias chosen, shown at the end
	approval          []approvalItem // the hosts to approve on the last look

	edit *config.Project // the project being edited; nil when adding
	// proposal is the agent's proposal the form was opened with; nil when
	// the user opened it. proposed is set once its path has been checked.
	proposal *Proposal
	proposed bool
	// lastErr is the last error the form showed, kept for the decline:
	// going back a step clears err.
	lastErr string
	missing map[int]bool // saved routes no longer found, by port; shown, not saved

	enabled []string // the presets enabled here
	notes   []string // what the registry, discovery and presets had to say
	busy    string   // what a background step is doing; empty when none is
	err     string   // the current step's error
	seq     int      // the background step whose answer is awaited
	w       *worker
}

// approvalItem is a host the user approves by saving, and the address it
// has on the internet, if any.
type approvalItem struct {
	nearhostrc.Item
	public string
}

// listing is a parent directory's listing: out while not done, then its
// directories or why it failed.
type listing struct {
	done bool
	dirs []string
	err  error
}

// Answers of the add-project form's background steps.
type (
	// listedMsg is a listing made ahead, as the path is typed.
	listedMsg struct {
		conn   int
		parent string
		dirs   []string
		err    error
	}
	connectedMsg struct {
		seq   int
		files ServerFiles
		err   error
	}
	completedMsg struct {
		seq            int
		parent, prefix string
		dirs           []string
		err            error
	}
	resolvedMsg struct {
		seq  int
		path string
		ask  bool // whether to ask the registry question
		err  error
	}
	registryAnsweredMsg struct {
		seq  int
		note string
		err  error
	}
	discoveredMsg struct {
		seq   int
		draft Draft
		err   error
	}
	presetsEnabledMsg struct {
		seq   int
		notes []string
	}
	namesCheckedMsg struct {
		seq               int
		nameErr, aliasErr error
		advice            []Advisory
		notes             []string
		approval          []approvalItem
	}
	projectSavedMsg struct {
		seq  int
		name string
		err  error
	}
)

// newProjectEditForm opens the form to edit p: its server and path stay,
// discovery runs again, and its routes, name and alias can change. Run the
// command it returns: it connects to the server.
func newProjectEditForm(b Backend, cfg *config.Config, p config.Project, w *worker) (*projectForm, tea.Cmd) {
	f := newProjectForm(b, cfg, "", p.Server, w)
	f.edit = &p
	f.srv, _ = cfg.Server(p.Server)
	f.step, f.resolved, f.path.value = projectStepPath, p.Path, p.Path
	f.name.value, f.alias.value = p.Name, p.Alias
	f.aliasEdited = p.Alias != config.AliasFor(p.Name)
	return f, f.connect()
}

// newProposalForm opens the add-project form with an agent's proposal: on
// srv, the proposal's origin, at its path, which is checked and read at
// once. The name and web port it gives are taken when they fit. Run the
// command it returns: it connects to the server.
func newProposalForm(b Backend, cfg *config.Config, srv config.Server, p Proposal, w *worker) (*projectForm, tea.Cmd) {
	f := newProjectForm(b, cfg, "", srv.Name, w)
	f.proposal = &p
	f.srv, f.step, f.path.value = srv, projectStepPath, p.Path
	if p.Name != "" {
		f.name.value, f.alias.value = p.Name, config.AliasFor(p.Name)
	}
	return f, f.connect()
}

// newProjectForm opens the add-project form with the cursor on the server
// called selected.
func newProjectForm(b Backend, cfg *config.Config, dir, selected string, w *worker) *projectForm {
	f := &projectForm{b: b, cfg: cfg, dir: dir, offered: map[string]bool{}, w: w}
	f.servers = append([]config.Server{{Name: config.LocalServer}}, cfg.Servers...)
	for i, s := range f.servers {
		if s.Name == selected {
			f.cursor = i
		}
	}
	return f
}

func (f *projectForm) update(msg tea.Msg) tea.Cmd {
	if f.sub != nil {
		switch msg := msg.(type) {
		case formClosedMsg:
			f.sub = nil
			return nil
		case serverAddedMsg:
			f.sub = nil
			f.servers = append(f.servers, msg.srv)
			f.cursor = len(f.servers) - 1
			return f.pick(msg.srv)
		}
		return f.sub.update(msg)
	}
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return f.key(msg)
	case tea.PasteMsg:
		return f.paste(msg.Content)
	case connectedMsg:
		if msg.seq != f.seq {
			if msg.files != nil {
				return closeFiles(msg.files) // a connection nobody waits for
			}
			return nil
		}
		f.busy = ""
		if msg.err != nil {
			f.err = msg.err.Error()
			return nil
		}
		f.files = msg.files
		f.conn, f.listings = nextSeq(), map[string]listing{}
		if f.edit != nil {
			return f.discover() // the path is the project's
		}
		if f.proposal != nil && !f.proposed {
			f.proposed = true
			return f.resolve() // the agent gave the path
		}
		return f.listAhead()
	case listedMsg:
		if msg.conn != f.conn || f.files == nil || f.listings[msg.parent].done {
			return nil // tab listed it since, or the files closed
		}
		f.listings[msg.parent] = listing{true, msg.dirs, msg.err}
	case completedMsg:
		if msg.seq != f.seq {
			return nil
		}
		f.busy = ""
		return f.complete(msg)
	case resolvedMsg:
		if msg.seq != f.seq {
			return nil
		}
		f.busy = ""
		if msg.err != nil {
			f.err = msg.err.Error()
			return nil
		}
		f.resolved = msg.path
		if msg.ask {
			f.step, f.choice = projectStepRegistry, 0
			return nil
		}
		return f.discover()
	case registryAnsweredMsg:
		if msg.seq != f.seq {
			return nil
		}
		f.busy = ""
		if msg.err != nil {
			f.err = msg.err.Error()
			return nil
		}
		if msg.note != "" {
			f.notes = append(f.notes, msg.note)
		}
		return f.discover()
	case discoveredMsg:
		if msg.seq != f.seq {
			return nil
		}
		f.busy = ""
		if msg.err != nil {
			f.step, f.err = projectStepPath, msg.err.Error()
			return nil
		}
		f.draft = msg.draft
		var fresh []SuggestedPreset
		for _, p := range msg.draft.Presets {
			if !f.offered[p.Name] {
				fresh = append(fresh, p)
			}
		}
		if len(fresh) > 0 {
			f.draft.Presets = fresh
			f.ticked = make([]bool, len(fresh))
			for i, p := range fresh {
				f.ticked[i] = true
				f.offered[p.Name] = true
			}
			f.step, f.tickRow = projectStepPresets, 0
			return nil
		}
		f.draft.Presets = nil
		f.toRoutes()
	case presetsEnabledMsg:
		if msg.seq != f.seq {
			return nil
		}
		f.busy = ""
		f.notes = append(f.notes, msg.notes...)
		return f.discover()
	case namesCheckedMsg:
		if msg.seq != f.seq {
			return nil
		}
		f.busy = ""
		f.nameErr, f.aliasErr = errText(msg.nameErr), errText(msg.aliasErr)
		if f.nameErr == "" && f.aliasErr == "" {
			f.advice, f.approval = msg.advice, msg.approval
			f.notes = append(f.notes, msg.notes...)
			f.step = projectStepConfirm
		}
	case projectSavedMsg:
		if msg.seq != f.seq {
			return nil
		}
		f.busy = ""
		if msg.err != nil {
			f.err = msg.err.Error()
			return nil
		}
		name, edited := msg.name, f.edit != nil
		return tea.Batch(f.closeFiles(), func() tea.Msg { return projectAddedMsg{name, edited} })
	}
	return nil
}

// release closes a connection nobody took.
func (m connectedMsg) release() {
	if m.files != nil {
		m.files.Close()
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// closeFiles closes files on the side; nothing waits for it.
func closeFiles(files ServerFiles) tea.Cmd {
	return func() tea.Msg {
		files.Close()
		return nil
	}
}

// closeFiles closes the form's server files, if open.
func (f *projectForm) closeFiles() tea.Cmd {
	if f.files == nil {
		return nil
	}
	files := f.files
	f.files, f.listings = nil, nil
	return closeFiles(files)
}

func (f *projectForm) key(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	if s == "esc" && f.editingHosts {
		f.editingHosts, f.hostsErr = false, ""
		return nil
	}
	if s == "esc" {
		return f.back()
	}
	if f.busy != "" {
		return nil
	}
	switch f.step {
	case projectStepServer:
		switch s {
		case "up":
			f.cursor = max(f.cursor-1, 0)
		case "down":
			f.cursor = min(f.cursor+1, len(f.servers))
		case "enter":
			if f.cursor == len(f.servers) {
				var load tea.Cmd
				f.sub, load = newServerForm(f.b, f.cfg, f.w)
				return load
			}
			return f.pick(f.servers[f.cursor])
		}
	case projectStepPath:
		if s == "ctrl+r" && f.srv.Name != config.LocalServer {
			return tea.Batch(f.closeFiles(), f.connect()) // the connection dropped
		}
		if f.edit != nil {
			// The path is the project's: this step only reads it again.
			switch {
			case f.files == nil && s == "r":
				return f.connect()
			case f.files != nil && s == "enter":
				return f.discover()
			}
			return nil
		}
		if f.files == nil { // connecting failed
			if s == "r" {
				return f.connect()
			}
			return nil
		}
		switch s {
		case "tab":
			return f.tab()
		case "right":
			if f.suggestion() != "" {
				return f.accept()
			}
			return nil
		case "down", "up":
			if n := len(f.candidates()); n > 1 {
				step := 1
				if s == "up" {
					step = n - 1
				}
				f.chosen = (f.chosen + step) % n
			}
			return nil
		case "enter":
			return f.resolve()
		}
		if f.path.key(k) {
			return f.edited()
		}
	case projectStepRegistry:
		switch s {
		case "up":
			f.choice = max(f.choice-1, 0)
		case "down":
			f.choice = min(f.choice+1, len(registryChoices)-1)
		case "enter":
			f.seq = nextSeq()
			f.busy, f.err = "Answering…", ""
			seq, b, answer := f.seq, f.b, registryChoices[f.choice].answer
			if answer == RegistryYes {
				f.busy = "Trusting the official presets…"
			}
			return f.w.do(func(ctx context.Context) tea.Msg {
				note, err := b.AnswerRegistry(ctx, answer)
				return registryAnsweredMsg{seq, note, err}
			})
		}
	case projectStepPresets:
		switch s {
		case "up":
			f.tickRow = max(f.tickRow-1, 0)
		case "down":
			f.tickRow = min(f.tickRow+1, len(f.ticked)-1)
		case "space":
			f.ticked[f.tickRow] = !f.ticked[f.tickRow]
		case "enter":
			var names []string
			for i, p := range f.draft.Presets {
				if f.ticked[i] {
					names = append(names, p.Name)
				}
			}
			if len(names) == 0 {
				f.toRoutes()
				return nil
			}
			f.enabled = names
			f.seq = nextSeq()
			f.busy = "Enabling " + strings.Join(names, ", ") + "…"
			seq, b := f.seq, f.b
			return f.w.do(func(ctx context.Context) tea.Msg {
				return presetsEnabledMsg{seq, b.EnablePresets(ctx, names)}
			})
		}
	case projectStepRoutes:
		return f.routesKey(k)
	case projectStepDetails:
		switch s {
		case "tab", "up", "down":
			f.focus = 1 - f.focus
		case "enter":
			f.seq = nextSeq()
			f.busy, f.nameErr, f.aliasErr = "Checking…", "", ""
			f.name.value, f.alias.value = strings.TrimSpace(f.name.value), strings.TrimSpace(f.alias.value)
			seq, b, name, alias := f.seq, f.b, f.name.value, f.alias.value
			files, dir, routes := f.files, f.resolved, append([]config.Route(nil), f.routes...)
			items, edit := f.needsApproval(), f.edit
			return f.w.do(func(ctx context.Context) tea.Msg {
				nameErr, aliasErr := b.CheckNames(name, alias)
				if edit != nil {
					// The project being edited does not clash with itself.
					// This is settled before the advice and the approval
					// list are worked out, which keeping the name must not
					// skip.
					if name == edit.Name {
						nameErr = nil
					}
					if strings.EqualFold(alias, edit.Alias) {
						aliasErr = nil
					}
				}
				msg := namesCheckedMsg{seq: seq, nameErr: nameErr, aliasErr: aliasErr}
				if nameErr == nil && aliasErr == nil && files != nil {
					var err error
					if msg.advice, msg.notes, err = files.Advise(ctx, dir, routes, alias); err != nil {
						msg.notes = append(msg.notes, err.Error())
					}
				}
				if nameErr == nil && aliasErr == nil {
					for _, it := range items {
						var addr string
						if it.Host != "" {
							addr, _ = b.PublicAddress(ctx, it.Host)
						}
						msg.approval = append(msg.approval, approvalItem{it, addr})
					}
				}
				return msg
			})
		default:
			if f.focus == 1 {
				if f.alias.key(k) {
					f.aliasEdited, f.nameErr, f.aliasErr = true, "", ""
				}
			} else if f.name.key(k) {
				f.nameChanged()
			}
		}
	case projectStepConfirm:
		if s == "enter" {
			f.seq = nextSeq()
			f.busy, f.err = "Saving…", ""
			seq, b, p := f.seq, f.b, f.project()
			if f.edit != nil {
				was := f.edit.Name
				p.Enabled = f.edit.Enabled
				return f.w.do(func(context.Context) tea.Msg {
					return projectSavedMsg{seq, p.Name, b.EditConfig(func(cfg *config.Config) error {
						for i := range cfg.Projects {
							if cfg.Projects[i].Name == was {
								cfg.Projects[i] = p
								return hostClash(cfg, p)
							}
						}
						return errNoLongerConfigured(was)
					})}
				})
			}
			return f.w.do(func(context.Context) tea.Msg { return projectSavedMsg{seq, p.Name, b.SaveProject(p)} })
		}
	}
	return nil
}

// paste puts pasted text in the field of the current step, if it has one.
func (f *projectForm) paste(s string) tea.Cmd {
	if f.busy != "" {
		return nil
	}
	switch f.step {
	case projectStepPath:
		if f.files != nil && f.edit == nil {
			f.path.paste(s)
			return f.edited()
		}
	case projectStepRoutes:
		if f.editingHosts {
			f.hosts.paste(s)
			f.hostsErr = ""
			return nil
		}
		if len(f.routes) == 0 && strings.Trim(s, "0123456789") == "" {
			f.webPort.paste(s)
			f.err = ""
		}
	case projectStepDetails:
		if f.focus == 1 {
			f.alias.paste(s)
			f.aliasEdited, f.nameErr, f.aliasErr = true, "", ""
		} else {
			f.name.paste(s)
			f.nameChanged()
		}
	}
	return nil
}

// nameChanged keeps the alias following the name until it is edited, or
// while it is the one the project's .nearhostrc gives.
func (f *projectForm) nameChanged() {
	f.nameErr, f.aliasErr = "", ""
	if !f.aliasEdited && !f.aliasFromRc {
		f.alias.value = config.AliasFor(strings.TrimSpace(f.name.value))
	}
}

// routesKey handles a key on the routes step.
func (f *projectForm) routesKey(k tea.KeyPressMsg) tea.Cmd {
	if len(f.routes) == 0 {
		if k.String() == "enter" {
			port, err := strconv.Atoi(strings.TrimSpace(f.webPort.value))
			if err != nil || port < 1 || port > 65535 {
				f.err = "the web port is a number from 1 to 65535"
				return nil
			}
			f.routes = []config.Route{{Port: port, Role: config.RoleWeb, Label: "web"}}
			f.err, f.step = "", projectStepDetails
			return nil
		}
		if k.Text != "" && strings.Trim(k.Text, "0123456789") != "" {
			return nil // digits only
		}
		if f.webPort.key(k) {
			f.err = ""
		}
		return nil
	}
	if f.editingHosts {
		if k.String() == "enter" {
			f.setHosts()
		} else if f.hosts.key(k) {
			f.hostsErr = ""
		}
		return nil
	}
	switch k.String() {
	case "h":
		if f.routes[f.routeRow].Role != config.RoleWeb {
			f.err = "only a web route has hosts; make this one web with ← or → first"
			return nil
		}
		f.hosts.value = strings.Join(f.routes[f.routeRow].Hosts, " ")
		f.editingHosts, f.hostsErr, f.err = true, "", ""
	case "up":
		f.routeRow = max(f.routeRow-1, 0)
	case "down":
		f.routeRow = min(f.routeRow+1, len(f.routes)-1)
	case "space":
		f.toggleRoute(f.routeRow)
	case "right":
		f.cycleRole(f.routeRow, 1)
		f.err = ""
	case "left":
		f.cycleRole(f.routeRow, -1)
		f.err = ""
	case "enter":
		web := 0
		for _, r := range f.routes {
			if r.Role == config.RoleWeb {
				web++
			}
		}
		// More than one web route is allowed: a second one is kept when it
		// has hosts of its own, and Validate on save says what is amiss.
		if web == 0 {
			f.err = "choose the web route with ← or →"
			return nil
		}
		f.err, f.step = "", projectStepDetails
	}
	return nil
}

// setHosts gives the selected route the hosts typed for it, separated by
// spaces, as `nearhost add --host` takes them: lower-cased, each checked, and
// each served once, so one on another route, or typed twice, is dropped.
// Hosts new to the route count as approved, since the user typed them. A
// host that is not valid keeps the field open and says why.
func (f *projectForm) setHosts() {
	elsewhere := map[string]bool{}
	for j, r := range f.routes {
		if j != f.routeRow {
			for _, h := range r.Hosts {
				elsewhere[strings.ToLower(h)] = true
			}
		}
	}
	had := map[string]bool{}
	for _, h := range f.routes[f.routeRow].Hosts {
		had[strings.ToLower(h)] = true
	}
	var hosts []string
	seen := map[string]bool{}
	for _, h := range strings.Fields(f.hosts.value) {
		h = strings.ToLower(h)
		if err := hostname.Valid(h); err != nil {
			f.hostsErr = err.Error()
			return
		}
		if !seen[h] && !elsewhere[h] {
			seen[h] = true
			hosts = append(hosts, h)
		}
	}
	if f.typed == nil {
		f.typed = map[string]bool{}
	}
	for _, h := range hosts {
		if !had[h] {
			f.typed[h] = true
		}
	}
	f.routes[f.routeRow].Hosts = hosts
	f.editingHosts, f.hostsErr = false, ""
}

// needsApproval is what saving the project approves: the hosts and options
// that need approval and were not approved before, leaving out hosts typed
// here and a main name typed in the alias field.
func (f *projectForm) needsApproval() []nearhostrc.Item {
	var old config.Project
	if f.edit != nil {
		old = *f.edit
	}
	// The alias is checked against what is typed now, not remembered in
	// f.typed, so a name typed and then changed is asked about again.
	typedAlias := ""
	if f.aliasEdited {
		typedAlias = strings.ToLower(strings.TrimSpace(f.alias.value))
	}
	var items []nearhostrc.Item
	for _, it := range nearhostrc.NeedsApproval(old, f.project()) {
		if it.Host == "" || !(f.typed[strings.ToLower(it.Host)] || strings.ToLower(it.Host) == typedAlias) {
			items = append(items, it)
		}
	}
	return items
}

// hostClash refuses p, as it is in cfg, when it is enabled and serves a host
// another enabled project serves. A disabled project serves nothing, as
// `nearhost add` has it.
func hostClash(cfg *config.Config, p config.Project) error {
	if host, other, clash := cfg.HostClash(p); clash && p.Enabled {
		return fmt.Errorf("host %s is already served by project %q", host, other)
	}
	return nil
}

// fromRc reports whether the project's .nearhostrc lists port.
func (f *projectForm) fromRc(port int) bool {
	if f.draft.Nearhostrc == nil {
		return false
	}
	for _, r := range f.draft.Nearhostrc.Routes {
		if r.Port == port {
			return true
		}
	}
	return false
}

// roles are the roles a route can take on the form's server, in the order
// ← and → move through them. Loopback means nothing on this machine.
func (f *projectForm) roles() []config.Role {
	if f.srv.Name == config.LocalServer {
		return []config.Role{config.RoleWeb, config.RoleIgnore}
	}
	return []config.Role{config.RoleWeb, config.RoleLoopback, config.RoleIgnore}
}

// cycleRole moves route i by step through the roles. A route made web
// takes the role from the one that was, which is then ignored.
func (f *projectForm) cycleRole(i, step int) {
	roles := f.roles()
	at := 0
	for j, r := range roles {
		if r == f.routes[i].Role {
			at = j
		}
	}
	f.setRole(i, roles[(at+step+len(roles))%len(roles)])
}

// toggleRoute serves an ignored route, as loopback, or web on this
// machine, and ignores one that is served. The web route stays: a project
// needs one, so another is made web first.
func (f *projectForm) toggleRoute(i int) {
	switch f.routes[i].Role {
	case config.RoleWeb:
		f.err = "a project needs its web route; make another route web with ← or → first"
		return
	case config.RoleIgnore:
		f.setRole(i, f.roles()[len(f.roles())-2]) // loopback, or web on this machine
	default:
		f.setRole(i, config.RoleIgnore)
	}
	f.err = ""
}

// setRole gives route i role; a route made web takes the place of the web
// route without hosts. A web route with hosts of its own is a site the user
// wrote by hand, so it stays web; Validate on save reports a project that
// ends up inconsistent.
func (f *projectForm) setRole(i int, role config.Role) {
	if role == config.RoleWeb {
		for j := range f.routes {
			if j != i && f.routes[j].Role == config.RoleWeb && len(f.routes[j].Hosts) == 0 {
				f.routes[j].Role = config.RoleIgnore
			}
		}
	}
	f.routes[i].Role = role
}

// pick goes on with srv: to the path step, connecting to the server.
func (f *projectForm) pick(srv config.Server) tea.Cmd {
	f.srv, f.step, f.err = srv, projectStepPath, ""
	f.path.value = "~/"
	if srv.Name == config.LocalServer {
		f.path.value = f.dir
	}
	return f.connect()
}

// connect opens the server's files.
func (f *projectForm) connect() tea.Cmd {
	f.seq = nextSeq()
	f.busy, f.err = "Connecting to "+clean(f.srv.Name)+"…", ""
	if f.srv.Name == config.LocalServer {
		f.busy = "Opening this machine…"
	}
	seq, b, srv := f.seq, f.b, f.srv
	return f.w.do(func(ctx context.Context) tea.Msg {
		files, err := b.OpenServer(ctx, srv)
		return connectedMsg{seq, files, err}
	})
}

// splitPath splits a path at its last slash: the parent, kept as typed with
// its slash, and the part being typed after it.
func splitPath(v string) (parent, part string) {
	if i := strings.LastIndex(v, "/"); i >= 0 {
		return v[:i+1], v[i+1:]
	}
	return "", v
}

// isDirName reports whether part is a directory already: ~ at the start,
// . or .., which only need their slash.
func isDirName(parent, part string) bool {
	switch part {
	case "~":
		return parent == ""
	case ".", "..":
		return true
	}
	return false
}

// listAhead lists the path's parent in the background, if it has not been
// already, so suggestions are there as the rest is typed.
func (f *projectForm) listAhead() tea.Cmd {
	if f.files == nil || f.edit != nil {
		return nil
	}
	parent, _ := splitPath(f.path.value)
	if _, ok := f.listings[parent]; ok {
		return nil
	}
	f.listings[parent] = listing{}
	conn, files, list := f.conn, f.files, parent
	if list == "" {
		list = "."
	}
	return f.w.do(func(ctx context.Context) tea.Msg {
		dirs, err := files.ListDirs(ctx, list)
		return listedMsg{conn, parent, dirs, err}
	})
}

// edited follows an edit to the path: what was said about the last one
// goes, the first candidate is chosen, and the new parent is listed ahead.
func (f *projectForm) edited() tea.Cmd {
	f.err, f.listNote, f.chosen = "", "", 0
	return f.listAhead()
}

// candidates are the parent's directories that start with the part being
// typed, in the listing's order. Hidden ones wait for a dot.
func (f *projectForm) candidates() []string {
	parent, part := splitPath(f.path.value)
	l := f.listings[parent]
	if !l.done || l.err != nil || part == "~" && parent == "" {
		return nil
	}
	var found []string
	for _, d := range l.dirs {
		hidden := strings.HasPrefix(d, ".") && !strings.HasPrefix(part, ".")
		if strings.HasPrefix(d, part) && !hidden {
			found = append(found, d)
		}
	}
	return found
}

// suggestion is the candidate chosen, or "" when there is none.
func (f *projectForm) suggestion() string {
	c := f.candidates()
	if len(c) == 0 {
		return ""
	}
	return c[f.chosen%len(c)]
}

// accept puts the suggestion in the path, as a directory.
func (f *projectForm) accept() tea.Cmd {
	parent, _ := splitPath(f.path.value)
	f.path.value = parent + f.suggestion() + "/"
	return f.edited()
}

// tab completes the path: it accepts the suggestion, else gives ~, . and ..
// their slash, else lists the parent again, as it may have changed since.
func (f *projectForm) tab() tea.Cmd {
	parent, part := splitPath(f.path.value)
	switch {
	case f.suggestion() != "":
		return f.accept()
	case isDirName(parent, part):
		f.path.value += "/"
		return f.edited()
	}
	f.seq = nextSeq()
	f.busy, f.listNote = "Listing…", ""
	seq, files, list := f.seq, f.files, parent
	if list == "" {
		list = "."
	}
	return f.w.do(func(ctx context.Context) tea.Msg {
		dirs, err := files.ListDirs(ctx, list)
		return completedMsg{seq, parent, part, dirs, err}
	})
}

// complete follows a listing tab waited for: the only match is taken,
// several are offered, and none, or a failure, is said.
func (f *projectForm) complete(msg completedMsg) tea.Cmd {
	f.listings[msg.parent] = listing{true, msg.dirs, msg.err}
	switch c := f.candidates(); {
	case msg.err != nil:
		f.listNote = msg.err.Error()
	case len(c) == 0:
		f.listNote = "no directories match"
	case len(c) == 1:
		return f.accept()
	}
	return nil
}

// resolve checks the path typed and goes on to the registry question or
// discovery.
func (f *projectForm) resolve() tea.Cmd {
	f.seq = nextSeq()
	f.busy, f.err, f.listNote = "Checking "+clean(f.path.value)+"…", "", ""
	seq, files, p, b := f.seq, f.files, strings.TrimSpace(f.path.value), f.b
	return f.w.do(func(ctx context.Context) tea.Msg {
		resolved, err := files.Resolve(ctx, p)
		return resolvedMsg{seq, resolved, err == nil && b.RegistryQuestion(), err}
	})
}

// discover reads the project.
func (f *projectForm) discover() tea.Cmd {
	f.seq = nextSeq()
	where := "on " + clean(f.srv.Name)
	if f.srv.Name == config.LocalServer {
		where = "on this machine"
	}
	f.busy, f.err = "Reading "+clean(f.resolved)+" "+where+"…", ""
	seq, files, p := f.seq, f.files, f.resolved
	return f.w.do(func(ctx context.Context) tea.Msg {
		d, err := files.Discover(ctx, p)
		return discoveredMsg{seq, d, err}
	})
}

// toRoutes goes on to the routes step with what discovery found. When
// editing, each port still found keeps its saved role, label and hosts, and
// a saved web route also gets the hosts discovery infers that are not on any
// route of the form or the alias yet; a saved route no longer found is kept
// and marked; and a new port found does not take the web route from the
// saved one, unless it has hosts, when it is web but after the saved primary.
func (f *projectForm) toRoutes() {
	f.routes = append([]config.Route(nil), f.draft.Routes...)
	if f.edit != nil {
		saved, wasSaved := map[int]config.Route{}, map[int]bool{}
		savedWeb, primary := false, 0
		for _, r := range f.edit.Routes {
			saved[r.Port], wasSaved[r.Port] = r, true
			if r.Role == config.RoleWeb && !savedWeb {
				savedWeb, primary = true, r.Port
			}
		}
		inferred := make([][]string, len(f.routes))
		for i, r := range f.routes {
			inferred[i] = r.Hosts
			if s, ok := saved[r.Port]; ok {
				f.routes[i].Role, f.routes[i].Label = s.Role, s.Label
				f.routes[i].Hosts = append([]string(nil), s.Hosts...)
				delete(saved, r.Port)
			} else if r.Role == config.RoleWeb && savedWeb && len(r.Hosts) == 0 {
				f.routes[i].Role = config.RoleIgnore
			}
		}
		// Hosts discovery infers are added to a saved web route, unless
		// they are already on a route of the form or are the alias.
		have := map[string]bool{strings.ToLower(strings.TrimSpace(f.alias.value)): true}
		for _, rs := range [][]config.Route{f.routes, f.edit.Routes} { // saved routes no longer found too
			for _, r := range rs {
				for _, h := range r.Hosts {
					have[strings.ToLower(h)] = true
				}
			}
		}
		for i, r := range f.routes {
			if !wasSaved[r.Port] || r.Role != config.RoleWeb {
				continue
			}
			for _, h := range inferred[i] {
				if !have[strings.ToLower(h)] {
					have[strings.ToLower(h)] = true
					f.routes[i].Hosts = append(f.routes[i].Hosts, h)
				}
			}
		}
		f.missing = map[int]bool{}
		for _, r := range f.edit.Routes {
			if _, gone := saved[r.Port]; gone {
				f.missing[r.Port] = true
				f.routes = append(f.routes, r)
			}
		}
		if savedWeb {
			f.routes = config.MovePrimary(f.routes, primary)
		}
		f.mergeRc()
	}
	if f.proposal != nil && f.proposal.Web != 0 {
		f.proposeWeb(f.proposal.Web)
	}
	f.routeRow, f.webPort.value, f.editingHosts = 0, "", false
	if f.name.value == "" {
		f.name.value, f.alias.value = f.draft.Name, f.draft.Alias
	}
	// When adding, the main name the project's .nearhostrc gives fills the
	// alias field unless one was typed, as `nearhost add` lets --alias win.
	// It is not counted as typed, so a real domain is still asked about. An
	// alias the file gave before and no longer does goes back to following
	// the name. Editing takes the file's alias in mergeRc instead.
	if f.edit == nil && !f.aliasEdited {
		switch rc := f.draft.Nearhostrc; {
		case rc != nil && rc.Alias != "":
			f.alias.value, f.aliasFromRc = rc.Alias, true
		case f.aliasFromRc:
			f.alias.value, f.aliasFromRc = config.AliasFor(strings.TrimSpace(f.name.value)), false
		}
	}
	f.step, f.err = projectStepRoutes, ""
}

// mergeRc applies the project's .nearhostrc to the routes of the project
// being edited as nearhost does when it starts: the file's routes come first
// with its hosts, hosts added by hand to a port it lists stay, and the rest
// keep their saved roles, but are not web. Discovery merged the file without
// the saved project, so it could not tell which hosts were added by hand. A
// merge that fails is said like a file that cannot be read.
func (f *projectForm) mergeRc() {
	snap := f.draft.Nearhostrc
	if snap == nil {
		return
	}
	file := &nearhostrc.File{SHA256: snap.SHA256, DockerClients: snap.DockerClients, Alias: snap.Alias}
	for _, r := range snap.Routes {
		file.Routes = append(file.Routes, nearhostrc.Route{Port: r.Port, Role: r.Role, Label: r.Label, Hosts: r.Hosts})
	}
	merged, err := nearhostrc.Merge(*f.edit, file, f.routes)
	if err != nil {
		f.draft.RcError, f.draft.Nearhostrc = err.Error(), nil
		return
	}
	f.routes = merged.Routes
	for _, r := range snap.Routes {
		delete(f.missing, r.Port) // the file has it
	}
	// A main name the file moved since it was approved is taken, as a
	// start would take it, unless the field was changed here already. It
	// came from the file, not from typing, so a real domain is asked about.
	if merged.Alias != f.edit.Alias && f.alias.value == f.edit.Alias {
		f.alias.value, f.aliasEdited, f.aliasFromRc = merged.Alias, false, true
	}
}

// proposeWeb makes port the web route, as the agent proposed, when
// discovery found it; the route that was the web route is then ignored.
func (f *projectForm) proposeWeb(port int) {
	at := -1
	for i, r := range f.routes {
		if r.Port == port {
			at = i
		}
	}
	if at < 0 {
		return
	}
	for i := range f.routes {
		if f.routes[i].Role == config.RoleWeb {
			f.routes[i].Role = config.RoleIgnore
		}
	}
	f.routes[at].Role = config.RoleWeb
}

// routeLabel is how a route's label is shown: a saved route no longer found
// says so.
func (f *projectForm) routeLabel(r config.Route) string {
	if f.missing[r.Port] {
		return strings.TrimSpace(r.Label + " (not found now)")
	}
	return r.Label
}

// project is the project the form adds.
func (f *projectForm) project() config.Project {
	return config.Project{
		Name: f.name.value, Server: f.srv.Name, Path: f.resolved, Alias: f.alias.value,
		Enabled: true, Routes: append([]config.Route(nil), f.routes...),
		// The snapshot is copied so needsApproval sees the opt-in, and saving
		// approves what .nearhostrc asks for, so it is saved with the
		// project. A file that cannot be read now has no snapshot, and the
		// one approved before is dropped: the routes saved are the form's,
		// not that snapshot's, so the next read that works must be reviewed
		// in full.
		Nearhostrc: f.draft.Nearhostrc.Clone(),
	}
}

// back goes back a step: from the first it closes the form, and a step that
// was waiting stops waiting.
func (f *projectForm) back() tea.Cmd {
	// A step that saves runs to its end, so what is saved is what the form
	// shows: answering the registry, enabling presets, saving the project.
	if f.busy != "" && (f.step == projectStepRegistry || f.step == projectStepPresets || f.step == projectStepConfirm) {
		return nil
	}
	f.seq = nextSeq() // what a background step says now is not wanted
	waiting := f.busy != ""
	if f.err != "" {
		f.lastErr = f.err
	}
	if f.proposal != nil && f.step == projectStepPath && !(waiting && f.files != nil) {
		// A proposal starts at its path: leaving it dismisses the
		// proposal, with what was wrong with the path, if anything.
		return tea.Batch(f.closeFiles(), closeForm)
	}
	f.busy, f.err, f.listNote = "", "", ""
	if f.edit != nil {
		// Editing starts at discovery: leaving it, or the routes when no
		// presets were offered, closes the form.
		switch {
		case f.step == projectStepPath,
			f.step == projectStepRoutes && len(f.draft.Presets) == 0,
			f.step == projectStepPresets && !waiting:
			return tea.Batch(f.closeFiles(), closeForm)
		}
	}
	switch f.step {
	case projectStepServer:
		return closeForm
	case projectStepPath:
		if waiting && f.files != nil {
			return nil // a check or a listing stopped; the path stays
		}
		f.step = projectStepServer
		return f.closeFiles()
	case projectStepRegistry:
		f.step = projectStepPath
	case projectStepPresets:
		if waiting {
			return nil
		}
		f.step = projectStepPath
	case projectStepRoutes:
		// Back to the presets when they were offered and none taken;
		// otherwise to the path.
		f.step = projectStepPath
		if len(f.draft.Presets) > 0 {
			f.step = projectStepPresets
		}
	case projectStepDetails:
		if !waiting {
			f.step = projectStepRoutes
		}
	case projectStepConfirm:
		if !waiting {
			f.step = projectStepDetails
		}
	}
	return nil
}

func (f *projectForm) lines(width int) []string {
	if f.sub != nil {
		return f.sub.lines(width)
	}
	lines := f.summary(width)
	if f.proposal != nil {
		lines = append([]string{styleAmber.Render("Proposed by an agent " + f.proposal.where()), ""}, lines...)
	}
	switch f.step {
	case projectStepServer:
		for i, s := range f.servers {
			mark := "  "
			if i == f.cursor {
				mark = styleBeacon.Render("▸ ")
			}
			where := styleDim.Render("this machine")
			if s.Name != config.LocalServer {
				st, _ := f.b.ServerStatus(s.Name)
				where = serverMark(st).String() + " " + styleDim.Render(clean(s.SSHHost))
			}
			title := "local"
			if s.Name != config.LocalServer {
				title = clean(s.Name)
			}
			lines = append(lines, mark+fit(title, 16)+where)
		}
		mark := "  "
		if f.cursor == len(f.servers) {
			mark = styleBeacon.Render("▸ ")
		}
		lines = append(lines, mark+"New server…")
	case projectStepPath:
		if f.edit != nil {
			lines = append(lines, field("server", clean(f.srv.Name), width)...)
			lines = append(lines, field("path", clean(f.resolved), width)...)
			break
		}
		focused := f.busy == "" && f.files != nil
		view := f.path.view(focused)
		if s := f.suggestion(); focused && s != "" {
			_, part := splitPath(f.path.value)
			view += styleDim.Render(clean(s[len(part):] + "/"))
		}
		lines = append(lines, field("path", view, width)...)
		if c := f.candidates(); focused && len(c) > 1 {
			names := make([]string, len(c))
			for i, d := range c {
				names[i] = styleDim.Render(clean(d))
				if i == f.chosen%len(c) {
					names[i] = styleBeacon.Render(clean(d))
				}
			}
			lines = append(lines, field("", ansi.Truncate(strings.Join(names, "  "), width-labelWidth, "…"), width)...)
		}
		if f.listNote != "" {
			lines = append(lines, field("", styleDim.Render(clean(sentence(f.listNote))), width)...)
		}
	case projectStepRegistry:
		lines = append(lines,
			"No preset registries are trusted, so Nearhost will read ports from .env.",
			"You can also set routes yourself in the config.",
			"Trust the official presets (nearhost/presets) for framework-specific setups?", "")
		for i, c := range registryChoices {
			mark := "  "
			if i == f.choice {
				mark = styleBeacon.Render("▸ ")
			}
			lines = append(lines, mark+c.label)
		}
	case projectStepPresets:
		lines = append(lines, "Presets that suit this project:", "")
		for i, p := range f.draft.Presets {
			mark := "  "
			if i == f.tickRow {
				mark = styleBeacon.Render("▸ ")
			}
			box := "[ ]"
			if f.ticked[i] {
				box = styleBeacon.Render("[x]")
			}
			lines = append(lines, mark+box+" "+clean(p.Name)+"  "+clean(p.Title)+styleDim.Render(" · "+clean(p.Repo)))
		}
	case projectStepRoutes:
		lines = append(lines, f.routeLines(width)...)
	case projectStepDetails:
		lines = append(lines, field("name", f.name.view(f.focus == 0 && f.busy == ""), width)...)
		if f.nameErr != "" {
			lines = append(lines, errorLines(f.nameErr, width)...)
		}
		lines = append(lines, field("alias", f.alias.view(f.focus == 1 && f.busy == ""), width)...)
		if f.aliasErr != "" {
			lines = append(lines, errorLines(f.aliasErr, width)...)
		}
	case projectStepConfirm:
		server := "local (this machine)"
		if f.srv.Name != config.LocalServer {
			server = clean(f.srv.Name) + styleDim.Render(" (ssh "+clean(f.srv.SSHHost)+")")
		}
		lines = append(lines, field("server", server, width)...)
		lines = append(lines, field("path", clean(f.resolved), width)...)
		lines = append(lines, field("name", clean(f.name.value), width)...)
		lines = append(lines, field("alias", styleBeacon.Render("https://"+clean(f.alias.value)), width)...)
		for i, r := range f.routes {
			label := ""
			if i == 0 {
				label = "routes"
			}
			route := fmt.Sprintf("%-6d %-9s %s", r.Port, r.Role, clean(f.routeLabel(r)))
			if len(r.Hosts) > 0 {
				route = fmt.Sprintf("%-6d %-9s %s  %s", r.Port, r.Role, clean(strings.Join(r.Hosts, " ")), clean(f.routeLabel(r)))
			}
			lines = append(lines, field(label, route, width)...)
		}
		lines = append(lines, approvalLines(f.approval, width)...)
		lines = append(lines, adviceLines(f.advice, width)...)
	}
	if f.busy != "" {
		lines = append(lines, "", busyLine(f.busy))
	}
	if f.err != "" {
		lines = append(lines, "")
		lines = append(lines, errorLines(f.err, width)...)
	}
	if f.step >= projectStepRoutes && len(f.notes)+len(f.draft.Notes) > 0 {
		lines = append(lines, "")
		for _, n := range append(append([]string(nil), f.notes...), f.draft.Notes...) {
			for _, l := range wrap(clean(n), width) {
				lines = append(lines, styleDim.Render(l))
			}
		}
	}
	return lines
}

// summary is what was chosen on the steps before this one, shown above the
// steps between the path and the last look.
func (f *projectForm) summary(width int) []string {
	if f.step <= projectStepPath || f.step == projectStepConfirm {
		return nil
	}
	server := "local"
	if f.srv.Name != config.LocalServer {
		server = clean(f.srv.Name)
	}
	lines := field("server", server, width)
	lines = append(lines, field("path", clean(f.resolved), width)...)
	if f.step > projectStepPresets && len(f.enabled) > 0 {
		lines = append(lines, field("presets", clean(strings.Join(f.enabled, ", ")), width)...)
	}
	return append(lines, "")
}

// routeLines draws the routes step: a row a route, with a box ticked when
// it is served and its role between arrows on the selected row.
func (f *projectForm) routeLines(width int) []string {
	if len(f.routes) == 0 {
		lines := []string{"No ports were found in this project's files.", ""}
		return append(lines, field("web port", f.webPort.view(true), width)...)
	}
	found := fmt.Sprintf("Found %d ports. Pick what to serve.", len(f.routes))
	if len(f.routes) == 1 {
		found = "Found 1 port. Pick what to serve."
	}
	lines := []string{found, "", styleDim.Render(fit("      port    role          label", width))}
	for i, r := range f.routes {
		selected := i == f.routeRow
		text, box := styleText, seg{styleBeacon, "[x]"}
		if r.Role == config.RoleIgnore {
			text, box = styleDim, seg{styleDim, "[ ]"}
		}
		role := seg{text, fit(string(r.Role), 14)}
		switch {
		case selected:
			role = seg{styleBeacon, fit("‹ "+string(r.Role)+" ›", 14)}
		case r.Role == config.RoleWeb:
			role.style = styleBeacon
		}
		cells := []seg{{text, " "}, box, {text, " " + fit(strconv.Itoa(r.Port), 8)}, role}
		label := clean(f.routeLabel(r))
		var right []seg
		if f.fromRc(r.Port) {
			right = []seg{{styleDim, "from .nearhostrc"}}
		}
		if len(r.Hosts) > 0 {
			// The hosts are cut short to leave room for the label, or the
			// first 24 cells of it.
			room := width - 28 - min(ansi.StringWidth(label), 24) - 2 - segsWidth(right) - 1
			hosts := ansi.Truncate(clean(strings.Join(r.Hosts, " ")), max(room, 8), "…")
			cells = append(cells, seg{text, hosts + "  "})
		}
		cells = append(cells, seg{text, label})
		lines = append(lines, renderRow(cells, right, width, selected))
	}
	if f.editingHosts {
		lines = append(lines, "")
		lines = append(lines, field("hosts", f.hosts.view(true), width)...)
		if f.hostsErr != "" {
			lines = append(lines, errorLines(f.hostsErr, width)...)
		}
	}
	if f.draft.RcError != "" {
		lines = append(lines, "")
		for _, l := range wrap(strings.TrimSuffix(clean(sentence(f.draft.RcError)), ".")+". These routes leave it out.", width) {
			lines = append(lines, styleAmber.Render(l))
		}
	}
	alias := f.draft.Alias
	if alias == "" || f.proposal != nil && f.alias.value != "" {
		alias = f.alias.value // none found, or the agent named the project
	}
	lines = append(lines, "")
	explain := styleDim.Render("web is what ") + styleBeacon.Render("https://"+clean(alias)) + styleDim.Render(" serves.")
	if f.srv.Name != config.LocalServer {
		explain += styleDim.Render(" loopback ports open on 127.0.0.1 here too, so the browser reaches them.")
	}
	lines = append(lines, wrap(explain, width)...)
	return append(lines, adviceLines(f.draft.Advisories, width)...)
}

// approvalLines lists the hosts and options saving approves, each with why
// it needs approval and, for a host the internet has, what serving it here
// hides.
func approvalLines(items []approvalItem, width int) []string {
	if len(items) == 0 {
		return nil
	}
	lines := []string{"", styleAmber.Render("Needs your approval:")}
	for _, it := range items {
		if it.Host == "" {
			lines = append(lines, wrap(fmt.Sprintf("  %s (%s)", clean(it.Name()), it.Why), width)...)
			continue
		}
		lines = append(lines, wrap(fmt.Sprintf("  %s on port %d (%s)", clean(it.Host), it.Port, it.Why), width)...)
		if it.public != "" {
			for _, l := range wrap("resolves to "+clean(it.public)+" on the internet; serving it here hides that site on this machine", width-4) {
				lines = append(lines, styleDim.Render("    "+l))
			}
		}
	}
	return lines
}

// adviceLines draws advisories, each with its snippet.
func adviceLines(advice []Advisory, width int) []string {
	var lines []string
	for _, a := range advice {
		lines = append(lines, "")
		for _, l := range wrap(clean("Advisory ("+a.Code+"): "+a.Message), width) {
			lines = append(lines, styleAmber.Render(l))
		}
		for _, l := range strings.Split(a.Snippet, "\n") {
			if l != "" {
				lines = append(lines, styleDim.Render("  "+clean(l)))
			}
		}
	}
	return lines
}

func (f *projectForm) heading() heading {
	if f.sub != nil {
		return f.sub.heading()
	}
	title := "Add project"
	if f.edit != nil {
		title = "Edit " + clean(f.edit.Name)
	}
	return heading{title: title, step: projectStepTitles[f.step], at: f.step + 1, of: projectStepConfirm + 1}
}

func (f *projectForm) keys() []footerItem {
	if f.sub != nil {
		return f.sub.keys()
	}
	if f.busy != "" {
		return []footerItem{{"esc", "back"}}
	}
	switch f.step {
	case projectStepServer:
		return []footerItem{{"enter", "choose"}, {"↑↓", "move"}, {"esc", "cancel"}}
	case projectStepPath:
		if f.files == nil {
			return []footerItem{{"r", "connect again"}, {"esc", "back"}}
		}
		if f.edit != nil {
			items := []footerItem{{"enter", "read again"}}
			if f.srv.Name != config.LocalServer {
				items = append(items, footerItem{"ctrl+r", "reconnect"})
			}
			return append(items, footerItem{"esc", "back"})
		}
		items := []footerItem{{"tab", "complete"}}
		if len(f.candidates()) > 1 {
			items = append(items, footerItem{"↑↓", "choose"})
		}
		items = append(items, footerItem{"enter", "next"})
		if f.srv.Name != config.LocalServer {
			items = append(items, footerItem{"ctrl+r", "reconnect"})
		}
		return append(items, footerItem{"esc", "back"})
	case projectStepRegistry:
		return []footerItem{{"enter", "answer"}, {"↑↓", "choose"}, {"esc", "back"}}
	case projectStepPresets:
		return []footerItem{{"space", "tick"}, {"enter", "next"}, {"esc", "back"}}
	case projectStepRoutes:
		if len(f.routes) == 0 {
			return []footerItem{{"enter", "next"}, {"esc", "back"}}
		}
		if f.editingHosts {
			return []footerItem{{"enter", "set hosts"}, {"esc", "cancel"}}
		}
		items := []footerItem{{"space", "toggle"}, {"←/→", "role"}}
		if f.routes[f.routeRow].Role == config.RoleWeb {
			items = append(items, footerItem{"h", "hosts"})
		}
		return append(items, footerItem{"enter", "next"}, footerItem{"esc", "back"})
	case projectStepDetails:
		return []footerItem{{"tab", "switch"}, {"enter", "next"}, {"esc", "back"}}
	}
	if len(f.approval) > 0 {
		return []footerItem{{"enter", "approve and save"}, {"esc", "back"}}
	}
	if f.edit != nil {
		return []footerItem{{"enter", "save"}, {"esc", "back"}}
	}
	return []footerItem{{"enter", "add"}, {"esc", "back"}}
}
