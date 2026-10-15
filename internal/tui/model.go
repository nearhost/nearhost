package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
)

const (
	// minWidth and minHeight are the smallest window the interface draws in.
	minWidth, minHeight = 80, 20
	// eventBuffer is how many engine events may wait for the interface; the
	// bus drops what does not fit, and the next tick redraws from Status.
	eventBuffer = 1024
	// tickEvery is how often the config file is checked and the screen
	// redrawn from the engine's state.
	tickEvery = time.Second
	// shutdownTimeout bounds stopping everything on quit.
	shutdownTimeout = 30 * time.Second
)

// Model is the interface's state. Create it with New and run it with
// tea.NewProgram.
type Model struct {
	b   Backend
	dir string // where nearhost was started; n adds the project there

	cfg       *config.Config
	width     int
	height    int
	sel       string // key of the selected row
	collapsed map[string]bool
	logs      *logs
	filter    textinput.Model // the sidebar's filter by name
	filtering bool            // the filter is being typed
	showHelp  bool            // the footer lists every key
	help      help.Model      // draws the footer
	spinner   spinner.Model
	spinning  bool // the spinner is turning
	form      form // the open add form; nil when none is

	routeFocus bool   // the project panel's routes have the keys
	routeRow   int    // the route selected there
	panelErr   string // why the last change made in the panel was refused
	// adviceWaiting is the row whose advice is read when the selection has
	// rested on it; empty when none is.
	adviceWaiting string
	advice        map[string]advice    // by project name
	since         map[string]time.Time // when each running project started running
	scroll        int                  // log lines scrolled back from the newest

	started  bool  // the engine started
	proxyErr error // why the engine could not start
	quitting bool
	cutShort context.CancelFunc // ends the shutdown's context

	events      <-chan engine.Event
	unsubscribe func()
	stamp       fileStamp          // the config file as last applied, or refused
	seen        fileStamp          // the config file at the last tick
	applying    bool               // an Apply is running
	endApply    context.CancelFunc // ends the running Apply; quitting calls it

	// What Home shows about the machine. Reading it can be slow (the trust
	// store, /etc/hosts), so it is read on the clock, not on every redraw.
	caTrusted  bool
	hostsState string

	work *worker // runs the forms' background steps

	firstRunShown bool // the first-run screen was offered this session
	// editing holds role changes back from when one is saved until the
	// config with it is applied, so each starts from the project as it is.
	editing bool
	// reloadAfter applies the config again once the running Apply is done:
	// a change was saved after it had read the file.
	reloadAfter bool

	now func() time.Time // the clock; tests set their own
}

// New creates the interface for b. dir is where `nearhost` was started.
func New(b Backend, dir string) *Model {
	events, unsubscribe := b.Events().Subscribe(eventBuffer)
	stamp := stampOf(b.ConfigPath())
	m := &Model{
		b: b, dir: dir,
		cfg:       b.Config(),
		sel:       homeKey,
		collapsed: map[string]bool{},
		advice:    map[string]advice{},
		since:     map[string]time.Time{},
		now:       time.Now,
		events:    events, unsubscribe: unsubscribe,
		stamp: stamp, seen: stamp,
		caTrusted: b.CATrusted(), hostsState: b.HostsState(),
		work: newWorker(),
		help: newHelp(), spinner: newSpinner(), filter: newFilter(),
	}
	m.logs = newLogs(func() time.Time { return m.now() })
	return m
}

// Messages the model sends itself.
type (
	startedMsg     struct{ err error }
	eventMsg       engine.Event
	tickMsg        time.Time
	appliedMsg     struct{ err error }
	resumedMsg     struct{}
	shutdownMsg    struct{ err error }
	projectDoneMsg struct {
		name string
		err  error
	}
	machineMsg struct {
		caTrusted  bool
		hostsState string
	}
	openedMsg struct {
		url string
		err error
	}
)

// Init starts the engine, listens for its events and starts the clock.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(func() tea.Msg {
		return startedMsg{m.b.Start(context.Background())}
	}, m.listen(), tick())
}

func (m *Model) listen() tea.Cmd {
	events := m.events
	return func() tea.Msg {
		ev, ok := <-events
		if !ok {
			return nil
		}
		return eventMsg(ev)
	}
}

func tick() tea.Cmd {
	return tea.Tick(tickEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Update handles a message.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	model, cmd := m.update(msg)
	// The advice of the selected project is read when it is not known, once
	// the selection has rested on it.
	return model, tea.Batch(cmd, m.waitForAdvice(), m.spin())
}

func (m *Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		return m, m.turn(msg)
	case adviceWaitMsg:
		m.adviceWaiting = ""
		if msg.key == m.sel {
			return m, m.readAdvice()
		}
	case adviceMsg:
		m.gotAdvice(msg)
	case reconnectedMsg:
		if msg.err != nil {
			m.refused(rowKey(rowServer, msg.name), msg.err)
		}
	case editedMsg:
		if msg.err != nil {
			m.editing = false
			m.refused(msg.key, msg.err)
			return m, nil
		}
		if msg.note != "" {
			m.logs.note(msg.note)
		}
		if m.applying {
			m.reloadAfter = true
			return m, nil
		}
		return m, m.reloadConfig()
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case startedMsg:
		if msg.err != nil {
			m.proxyErr = msg.err
			m.logs.note("The proxy could not start: " + msg.err.Error())
			return m, nil
		}
		m.started = true
		cmds := []tea.Cmd{m.readMachine()} // the engine has just created the CA
		for _, p := range m.cfg.Projects {
			if p.Enabled {
				cmds = append(cmds, m.startProject(p.Name))
			}
		}
		return m, tea.Batch(cmds...)
	case eventMsg:
		m.logs.event(engine.Event(msg), m.cfg)
		m.trackUptime()
		return m, m.listen()
	case tickMsg:
		m.trackUptime()
		return m, tea.Batch(tick(), m.checkConfig(), m.readMachine())
	case machineMsg:
		m.caTrusted, m.hostsState = msg.caTrusted, msg.hostsState
		m.offerFirstRun()
	case firstRunSkippedMsg:
		m.form = nil
		m.selectRow(homeKey)
	case caTrustedMsg:
		m.form = nil
		m.selectRow(homeKey)
		m.logs.note("The local CA is now trusted.")
		return m, m.readMachine()
	case projectDoneMsg:
		// A start or stop the engine refused outright changed no state, so
		// the refusal is the only sign of it.
		if msg.err != nil {
			m.logs.project(msg.name, time.Now(), msg.err.Error())
		}
	case appliedMsg:
		m.applying, m.endApply = false, nil
		if m.reloadAfter {
			// A change was saved while that Apply ran: apply it too.
			m.reloadAfter = false
			m.setConfig(m.b.Config())
			return m, m.reloadConfig()
		}
		m.editing = false
		if msg.err != nil {
			m.logs.note("Ignored a change to " + m.b.ConfigPath() + ": " + strings.TrimRight(msg.err.Error(), ".") + ". Save the file again to apply it.")
		}
		m.setConfig(m.b.Config())
	case openedMsg:
		if msg.err != nil {
			m.logs.note("Open " + msg.url + " in your browser.")
		}
	case suspendMsg:
		if m.quitting {
			msg.done <- errQuitting // nothing needs the terminal now
			return m, nil
		}
		done := msg.done
		return m, tea.Exec(&funcCommand{fn: msg.fn}, func(err error) tea.Msg {
			done <- err
			return resumedMsg{}
		})
	case shutdownMsg:
		if msg.err != nil {
			m.logs.note("Stopping: " + msg.err.Error())
		}
		m.unsubscribe()
		return m, tea.Quit
	case connectedMsg:
		// Only the add-project form, waiting on its own, takes a
		// connection. Any other is closed: its form is gone or going.
		if pf, ok := m.form.(*projectForm); ok && pf.sub == nil && !m.quitting {
			return m, pf.update(msg)
		}
		msg.release()
	case formClosedMsg:
		if pf, ok := m.form.(*projectForm); ok && pf.sub != nil {
			return m, pf.update(msg) // the add-server form opened from it
		}
		m.form = nil
	case serverAddedMsg:
		key := rowKey(rowServer, msg.srv.Name)
		verb := "Added server "
		if msg.edited {
			verb = "Saved server "
		}
		m.logs.noteOn(key, verb+msg.srv.Name+".")
		var cmd tea.Cmd
		if pf, ok := m.form.(*projectForm); ok && pf.sub != nil {
			cmd = pf.update(msg) // back to adding the project, on this server
		} else {
			m.form = nil
			m.selectRow(key)
		}
		return m, tea.Batch(cmd, m.applyNow())
	case projectAddedMsg:
		m.form = nil
		m.selectRow(rowKey(rowProject, msg.name))
		verb := "Added "
		if msg.edited {
			verb = "Saved "
		}
		m.logs.noteOn(m.sel, verb+msg.name+".")
		return m, m.applyNow()
	case changedMsg:
		m.form = nil
		m.selectRow(msg.sel)
		m.logs.noteOn(m.sel, msg.note)
		return m, m.applyNow()
	case tea.KeyPressMsg:
		return m, m.key(msg)
	case tea.MouseClickMsg:
		return m, m.click(msg.Mouse())
	case tea.MouseWheelMsg:
		m.wheel(msg.Mouse())
	default:
		// The answers of a form's background steps.
		if m.form != nil {
			return m, m.form.update(msg)
		}
	}
	return m, nil
}

// setConfig takes in the engine's configuration after a change, keeping the
// selection when its row is still there and selecting a project that is new.
func (m *Model) setConfig(cfg *config.Config) {
	old := m.cfg
	m.cfg = cfg
	for _, p := range cfg.Projects {
		if _, existed := old.Project(p.Name); !existed {
			delete(m.collapsed, p.Server) // so the new project's row is shown
			m.sel = rowKey(rowProject, p.Name)
			m.scroll = 0
			return
		}
	}
	for _, r := range rows(cfg, m.collapsed, m.query()) {
		if r.key() == m.sel {
			return
		}
	}
	m.selectRow(homeKey)
}

// startProject starts a project. A start that fails shows as the project's
// state; the error is logged as well, since a start refused outright (the
// proxy is down, the project is being changed) changes no state.
func (m *Model) startProject(name string) tea.Cmd {
	return func() tea.Msg {
		return projectDoneMsg{name, m.b.StartProject(context.Background(), name)}
	}
}

func (m *Model) stopProject(name string) tea.Cmd {
	return func() tea.Msg {
		return projectDoneMsg{name, m.b.StopProject(context.Background(), name)}
	}
}

// readMachine reads what Home shows about the machine.
func (m *Model) readMachine() tea.Cmd {
	return func() tea.Msg {
		return machineMsg{m.b.CATrusted(), m.b.HostsState()}
	}
}

// quit stops everything, then ends the program. Asked again while stopping,
// it cuts the shutdown short.
func (m *Model) quit() tea.Cmd {
	if m.quitting {
		m.cutShort()
		return nil
	}
	m.quitting = true
	if m.endApply != nil {
		m.endApply() // the shutdown would otherwise wait for it
	}
	// The open form's connection, and those its running steps are opening,
	// go before the engine does.
	var files ServerFiles
	if pf, ok := m.form.(*projectForm); ok {
		files, pf.files = pf.files, nil
	}
	work := m.work
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	m.cutShort = cancel
	return func() tea.Msg {
		defer cancel()
		work.stop()
		if files != nil {
			files.Close()
		}
		return shutdownMsg{m.b.Shutdown(ctx)}
	}
}

// View draws the screen.
func (m *Model) View() tea.View {
	var content string
	if m.width < minWidth || m.height < minHeight {
		content = m.tooSmall()
	} else {
		if m.empty() {
			content = m.emptyScreen()
		} else {
			content = m.frame(m.columns(), m.footer())
		}
		if m.form != nil {
			content = m.formBox(content)
		}
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// main draws the main panel: the selected item's details, then its log,
// exactly height lines of width cells.
func (m *Model) main(width, height int) []string {
	var details []string
	kind, name := m.selected()
	switch kind {
	case rowServer:
		details = m.serverPanel(name, width)
	case rowProject:
		details = m.projectPanel(name, width)
	default:
		details = m.home(width)
	}
	if len(details) > height-3 {
		details = details[:height-3]
	}
	lines := make([]string, 0, height)
	for _, d := range details {
		lines = append(lines, fit(d, width))
	}
	label := "log"
	if kind == rowHome {
		label = "recent"
	}
	log := m.logs.entries(m.sel, "")
	follow := styleDim.Render("following ") + styleBeacon.Render("●")
	if m.scroll > 0 {
		follow = styleDim.Render(fmt.Sprintf("%d newer below", m.scroll))
	}
	lines = append(lines, strings.Repeat(" ", width), section(label, follow, width))
	// The newest entries that fit, counted back from the end less what is
	// scrolled.
	room := height - len(lines)
	var shown []string
	for i := len(log) - 1 - m.scroll; i >= 0 && len(shown) < room; i-- {
		shown = append(entryLines(log[i], width, kind != rowProject), shown...)
	}
	if len(shown) > room {
		shown = shown[len(shown)-room:]
	}
	lines = append(lines, shown...)
	for len(lines) < height {
		lines = append(lines, strings.Repeat(" ", width))
	}
	return lines
}

// selected is the kind and name of the selected row.
func (m *Model) selected() (rowKind, string) {
	for _, r := range m.rows() {
		if r.key() == m.sel {
			return r.kind, r.name
		}
	}
	return rowHome, ""
}

// fileStamp is what is checked to notice a change to the config file.
type fileStamp struct {
	exists bool
	mod    time.Time
	size   int64
}

func stampOf(path string) fileStamp {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{exists: true, mod: info.ModTime(), size: info.Size()}
}

// checkConfig applies the config file when it has changed and then stayed
// the same for a tick, and no Apply is running. Waiting a tick keeps a save
// caught half way from being applied. An empty file is never applied: an
// editor that truncates before it writes leaves one for a moment, and
// applying it would stop every project.
func (m *Model) checkConfig() tea.Cmd {
	if m.applying || m.quitting {
		return nil
	}
	path := m.b.ConfigPath()
	now := stampOf(path)
	if now == m.stamp {
		m.seen = now
		return nil
	}
	if now != m.seen {
		m.seen = now // changed since the last tick: wait for it to settle
		return nil
	}
	if now.exists && now.size == 0 {
		m.stamp = now
		m.logs.note("Ignored a change to " + path + ": the file is empty")
		return nil
	}
	return m.reloadConfig()
}

// applyNow applies the config file now, after a form saved to it, unless an
// Apply is running; the clock picks the change up after that one.
func (m *Model) applyNow() tea.Cmd {
	if m.applying {
		return nil
	}
	return m.reloadConfig()
}

// reloadConfig reads the config file and gives it to the engine.
func (m *Model) reloadConfig() tea.Cmd {
	path := m.b.ConfigPath()
	m.stamp = stampOf(path)
	m.applying = true
	ctx, cancel := context.WithCancel(context.Background())
	m.endApply = cancel
	return func() tea.Msg {
		defer cancel()
		cfg, err := config.Load(path)
		if err == nil {
			err = m.b.Apply(ctx, cfg)
		}
		return appliedMsg{err}
	}
}
