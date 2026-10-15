package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/config"
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
	matches  []string // what completion found, when it found several
	listNote string   // why completion found nothing

	choice   int             // the registry step's row
	draft    Draft           // what discovery found last
	offered  map[string]bool // presets offered once already
	ticked   []bool
	tickRow  int
	routes   []config.Route
	routeRow int
	webPort  textField // asked for when discovery found no routes

	name, alias       textField
	aliasEdited       bool // the alias was typed, so it no longer follows the name
	focus             int  // 0 name, 1 alias
	nameErr, aliasErr string
	advice            []Advisory // for the routes and alias chosen, shown at the end

	edit    *config.Project // the project being edited; nil when adding
	missing map[int]bool    // saved routes no longer found, by port; shown, not saved

	notes []string // what the registry, discovery and presets had to say
	busy  string   // what a background step is doing; empty when none is
	err   string   // the current step's error
	seq   int      // the background step whose answer is awaited
	w     *worker
}

// Answers of the add-project form's background steps.
type (
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
		f.paste(msg.Content)
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
		if f.edit != nil {
			return f.discover() // the path is the project's
		}
	case completedMsg:
		if msg.seq != f.seq {
			return nil
		}
		f.busy = ""
		f.complete(msg)
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
		if f.edit != nil {
			// The project being edited does not clash with itself.
			if f.name.value == f.edit.Name {
				f.nameErr = ""
			}
			if strings.EqualFold(f.alias.value, f.edit.Alias) {
				f.aliasErr = ""
			}
		}
		if f.nameErr == "" && f.aliasErr == "" {
			f.advice = msg.advice
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
	f.files = nil
	return closeFiles(files)
}

func (f *projectForm) key(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
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
			return f.listDirs()
		case "enter":
			f.seq = nextSeq()
			f.busy, f.err, f.matches, f.listNote = "Checking "+clean(f.path.value)+"…", "", nil, ""
			seq, files, p, b := f.seq, f.files, strings.TrimSpace(f.path.value), f.b
			return f.w.do(func(ctx context.Context) tea.Msg {
				resolved, err := files.Resolve(ctx, p)
				return resolvedMsg{seq, resolved, err == nil && b.RegistryQuestion(), err}
			})
		}
		if f.path.key(k) {
			f.err, f.matches, f.listNote = "", nil, ""
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
			return f.w.do(func(ctx context.Context) tea.Msg {
				nameErr, aliasErr := b.CheckNames(name, alias)
				msg := namesCheckedMsg{seq: seq, nameErr: nameErr, aliasErr: aliasErr}
				if nameErr == nil && aliasErr == nil && files != nil {
					var err error
					if msg.advice, msg.notes, err = files.Advise(ctx, dir, routes, alias); err != nil {
						msg.notes = append(msg.notes, err.Error())
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
								return nil
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
func (f *projectForm) paste(s string) {
	if f.busy != "" {
		return
	}
	switch f.step {
	case projectStepPath:
		if f.files != nil && f.edit == nil {
			f.path.paste(s)
			f.err, f.matches, f.listNote = "", nil, ""
		}
	case projectStepRoutes:
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
}

// nameChanged keeps the alias following the name until it is edited.
func (f *projectForm) nameChanged() {
	f.nameErr, f.aliasErr = "", ""
	if !f.aliasEdited {
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
	switch k.String() {
	case "up":
		f.routeRow = max(f.routeRow-1, 0)
	case "down":
		f.routeRow = min(f.routeRow+1, len(f.routes)-1)
	case "space":
		f.cycleRole(f.routeRow)
		f.err = ""
	case "enter":
		web := 0
		for _, r := range f.routes {
			if r.Role == config.RoleWeb {
				web++
			}
		}
		if web != 1 {
			f.err = "choose the web route with space"
			return nil
		}
		f.err, f.step = "", projectStepDetails
	}
	return nil
}

// cycleRole moves route i to its next role: web, loopback, ignore. Loopback
// is skipped on this machine, where it means nothing. A route made web
// takes the role from the one that was.
func (f *projectForm) cycleRole(i int) {
	next := map[config.Role]config.Role{
		config.RoleWeb:      config.RoleLoopback,
		config.RoleLoopback: config.RoleIgnore,
		config.RoleIgnore:   config.RoleWeb,
	}
	role := next[f.routes[i].Role]
	if role == config.RoleLoopback && f.srv.Name == config.LocalServer {
		role = config.RoleIgnore
	}
	if role == config.RoleWeb {
		for j := range f.routes {
			if f.routes[j].Role == config.RoleWeb {
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

// listDirs lists the directories to complete the path's last part from.
// What comes before the last slash is kept as it was typed. ~, . and ..
// are directories already, and only gain their slash.
func (f *projectForm) listDirs() tea.Cmd {
	v := f.path.value
	parent, prefix := "", v
	if i := strings.LastIndex(v, "/"); i >= 0 {
		parent, prefix = v[:i+1], v[i+1:]
	}
	switch prefix {
	case "~", ".", "..":
		if parent == "" || prefix != "~" {
			f.path.value = v + "/"
			return nil
		}
	}
	f.seq = nextSeq()
	f.busy, f.matches, f.listNote = "Listing…", nil, ""
	seq, files, list := f.seq, f.files, parent
	if list == "" {
		list = "."
	}
	return f.w.do(func(ctx context.Context) tea.Msg {
		dirs, err := files.ListDirs(ctx, list)
		return completedMsg{seq, parent, prefix, dirs, err}
	})
}

// complete fills in what the directories listed make certain.
func (f *projectForm) complete(msg completedMsg) {
	if msg.err != nil {
		f.listNote = msg.err.Error()
		return
	}
	var found []string
	for _, d := range msg.dirs {
		hidden := strings.HasPrefix(d, ".") && !strings.HasPrefix(msg.prefix, ".")
		if strings.HasPrefix(d, msg.prefix) && !hidden {
			found = append(found, d)
		}
	}
	switch len(found) {
	case 0:
		f.listNote = "no directories match"
	case 1:
		f.path.value = msg.parent + found[0] + "/"
	default:
		f.path.value = msg.parent + commonPrefix(found)
		f.matches = found
	}
}

// commonPrefix is the longest start all of names share, in whole
// characters.
func commonPrefix(names []string) string {
	common := []rune(names[0])
	for _, n := range names[1:] {
		r := []rune(n)
		i := 0
		for i < len(common) && i < len(r) && common[i] == r[i] {
			i++
		}
		common = common[:i]
	}
	return string(common)
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
// editing, each port still found keeps its saved role and label, a saved
// route no longer found is kept and marked, and a new port found does not
// take the web route from the saved one.
func (f *projectForm) toRoutes() {
	f.routes = append([]config.Route(nil), f.draft.Routes...)
	if f.edit != nil {
		saved := map[int]config.Route{}
		savedWeb := false
		for _, r := range f.edit.Routes {
			saved[r.Port] = r
			savedWeb = savedWeb || r.Role == config.RoleWeb
		}
		for i, r := range f.routes {
			if s, ok := saved[r.Port]; ok {
				f.routes[i].Role, f.routes[i].Label = s.Role, s.Label
				delete(saved, r.Port)
			} else if r.Role == config.RoleWeb && savedWeb {
				f.routes[i].Role = config.RoleIgnore
			}
		}
		f.missing = map[int]bool{}
		for _, r := range f.edit.Routes {
			if _, gone := saved[r.Port]; gone {
				f.missing[r.Port] = true
				f.routes = append(f.routes, r)
			}
		}
	}
	f.routeRow, f.webPort.value = 0, ""
	if f.name.value == "" {
		f.name.value, f.alias.value = f.draft.Name, f.draft.Alias
	}
	f.step, f.err = projectStepRoutes, ""
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
	f.busy, f.err, f.matches, f.listNote = "", "", nil, ""
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
	var lines []string
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
			title := "Local"
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
		lines = append(lines, field("path", f.path.view(f.busy == "" && f.files != nil), width)...)
		if len(f.matches) > 0 {
			lines = append(lines, field("", styleDim.Render(strings.Join(f.matches, "  ")), width)...)
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
		server := "Local (this machine)"
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
			lines = append(lines, field(label, fmt.Sprintf("%-6d %-9s %s", r.Port, r.Role, clean(f.routeLabel(r))), width)...)
		}
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

// routeLines draws the routes step.
func (f *projectForm) routeLines(width int) []string {
	if len(f.routes) == 0 {
		lines := []string{"No ports were found in this project's files.", ""}
		return append(lines, field("web port", f.webPort.view(true), width)...)
	}
	lines := []string{styleDim.Render(fmt.Sprintf("  %-6s %-9s %s", "PORT", "ROLE", "LABEL"))}
	for i, r := range f.routes {
		mark := "  "
		if i == f.routeRow {
			mark = styleBeacon.Render("▸ ")
		}
		lines = append(lines, mark+fmt.Sprintf("%-6d %-9s %s", r.Port, r.Role, fit(clean(f.routeLabel(r)), width-20)))
	}
	return append(lines, adviceLines(f.draft.Advisories, width)...)
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
		items := []footerItem{{"tab", "complete"}, {"enter", "next"}}
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
		return []footerItem{{"space", "role"}, {"enter", "next"}, {"esc", "back"}}
	case projectStepDetails:
		return []footerItem{{"tab", "switch"}, {"enter", "next"}, {"esc", "back"}}
	}
	if f.edit != nil {
		return []footerItem{{"enter", "save"}, {"esc", "back"}}
	}
	return []footerItem{{"enter", "add"}, {"esc", "back"}}
}
