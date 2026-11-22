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
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/nearhostrc"
)

const (
	// minWidth and minHeight are the smallest window the interface draws in.
	minWidth, minHeight = 80, 20
	// eventBuffer is how many engine events may wait for the interface; the
	// bus drops what does not fit, and the next tick redraws from Status.
	eventBuffer = 1024
	// tickEvery is how often the screen is redrawn from the engine's state,
	// and its config taken afresh.
	tickEvery = time.Second
	// shutdownTimeout bounds stopping everything on quit.
	shutdownTimeout = 30 * time.Second
)

// formStopTimeout bounds how long quitting waits for the forms' background
// steps, inside shutdownTimeout, so the engine keeps the rest of it. A step
// still running then is left behind: the process is ending.
var formStopTimeout = 5 * time.Second

// Model is the interface's state. Create it with New and run it with
// tea.NewProgram.
type Model struct {
	b   Backend
	dir string // where nearhost was started; n adds the project there

	cfg       *config.Config
	width     int
	height    int
	tallCells bool // the terminal's line height stretches cells; see cellsTall
	mouse     mouseState
	toast     toastState
	sel       string // key of the selected row
	collapsed map[string]bool
	logs      *logs
	filter    textinput.Model // the sidebar's filter by name
	filtering bool            // the filter is being typed
	showHelp  bool            // the footer lists every key
	help      help.Model      // draws the footer
	spinner   spinner.Model
	spinning  bool     // the spinner is turning
	form      form     // the open add form; nil when none is
	logView   *logView // the full-screen log; nil when it is not shown
	// password asks for the user's password over everything else; nil when
	// nothing is asking.
	password *passwordBox
	// quitBox asks whether quitting stops Nearhost; nil when it is not open.
	quitBox *quitBox
	// waiting are the agents' requests to be reviewed, oldest first;
	// proposing is the proposal whose form is open, nil when none is.
	waiting   []asked
	proposing *proposing

	routeFocus bool // the project panel's routes have the keys
	routeRow   int  // the route selected there
	// passFocus is set while the server panel's passthroughs and grants have
	// the keys, and passRow is the row selected there. passSel is the key of
	// that row, by which passRows finds it again in rows read afresh.
	passFocus bool
	passRow   int
	passSel   string
	// passTop is the first of the rows the panel shows when they do not
	// all fit.
	passTop int
	// passClosing holds the passthroughs, by ID, whose close from the
	// panel is running, so x does not close one twice.
	passClosing map[string]bool
	// grantCache is the grants last read for each server.
	grantCache map[string]*grantsEntry
	// showIgnored lists a project's ignored routes in its panel; without
	// it they fold into one line, unless the routes have the keys.
	showIgnored bool
	panelErr    string // why the last change made in the panel was refused
	// adviceWaiting is the row whose advice is read when the selection has
	// rested on it; empty when none is.
	adviceWaiting string
	advice        map[string]advice    // by project name
	since         map[string]time.Time // when each running project started running
	scroll        int                  // log lines scrolled back from the newest

	started  bool  // the engine started
	proxyErr error // why the engine could not start
	// proxyRetrying is set while r on Home starts the engine again; further
	// presses wait for the answer.
	proxyRetrying bool
	quitting      bool
	leaving       bool               // the quit leaves Nearhost running
	cutShort      context.CancelFunc // ends the shutdown's context
	// lostErr is why the core could no longer be reached, which ended the
	// program; see Err.
	lostErr error
	// leftEmpty is set once a server is connected from the first-run
	// screen: the main screen stays up for this run, with nothing to serve.
	leftEmpty bool

	events      <-chan engine.Event
	unsubscribe func()
	applying    bool               // a Reload is running
	endApply    context.CancelFunc // ends the running Reload; quitting calls it

	// What Home shows about the machine. Reading it can be slow (the trust
	// store, /etc/hosts), so it is read on the clock, not on every redraw.
	caTrusted bool
	// caTrustedAt is when the trust step last reported success.
	caTrustedAt time.Time
	caExpires   time.Time // zero when the CA is not there yet
	hostsState  string

	work *worker // runs the forms' background steps

	firstRunShown bool // the first-run screen was offered this session
	// editing holds role changes back from when one is saved until the
	// config with it is applied, so each starts from the project as it is.
	editing bool
	// reloadAfter applies the config again once the running Reload is done:
	// a change was saved after it had read the file.
	reloadAfter bool

	// paletteSettled is set once the terminal has said what its background
	// is, or backgroundWait has gone by without an answer: the palette does
	// not change after that.
	paletteSettled bool

	// monochrome is set when the terminal shows no colour (NO_COLOR, a dumb
	// terminal, a pipe): what colour alone tells apart then needs a mark.
	monochrome bool

	// guard is the request box nextAsked opened last, which ignores keys
	// for askGuard after it opens.
	guard askGuarded

	now func() time.Time // the clock; tests set their own
	// getenv reads the environment, to tell which terminal this is; tests
	// set their own.
	getenv func(string) string
	// homeDir is this machine's home folder, which a document's folder on it
	// is shown below as ~; tests set their own.
	homeDir func() (string, error)
}

// New creates the interface for b. dir is where `nearhost` was started.
func New(b Backend, dir string) *Model {
	events, unsubscribe := b.Events().Subscribe(eventBuffer)
	m := &Model{
		b: b, dir: dir,
		cfg:       b.Config(),
		sel:       homeKey,
		collapsed: map[string]bool{},
		advice:    map[string]advice{},
		since:     map[string]time.Time{},
		now:       time.Now,
		getenv:    os.Getenv,
		homeDir:   realHome,
		events:    events, unsubscribe: unsubscribe,
		caTrusted: b.CATrusted(), caExpires: b.CAExpires(), hostsState: b.HostsState(),
		work: newWorker(),
		help: newHelp(), spinner: newSpinner(), filter: newFilter(),
	}
	m.logs = newLogs(func() time.Time { return m.now() })
	return m
}

// Messages the model sends itself.
type (
	startedMsg struct{ err error }
	eventMsg   engine.Event
	tickMsg    time.Time
	appliedMsg struct{ err error }
	resumedMsg struct{}
	// shutdownMsg is the end of a quit: err is the Shutdown's or Leave's,
	// and unsaved says why the quit box's answer could not be remembered.
	shutdownMsg struct {
		err, unsaved error
	}
	projectDoneMsg struct {
		name string
		err  error
	}
	machineMsg struct {
		// readAt is when the read began; zero means it is not tracked.
		readAt     time.Time
		caTrusted  bool
		caExpires  time.Time
		hostsState string
	}
	// openedMsg is what came of opening url for the row key.
	openedMsg struct {
		key, url string
		err      error
	}
)

// Init starts the engine, listens for its events and for the core being
// lost, and starts the clock. It asks the terminal for its background
// colour, to pick the palette.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(func() tea.Msg {
		return startedMsg{m.b.Start(context.Background())}
	}, m.listen(), m.waitLost(), tick(), tea.RequestBackgroundColor, backgroundTimer())
}

// backgroundWait is how long the interface waits for the terminal to say
// what its background colour is. A terminal that has not answered by then
// keeps the dark palette, and an answer that comes later is ignored, so the
// colours never change once the user has started reading.
const backgroundWait = 2 * time.Second

// backgroundWaitedMsg is backgroundWait gone by.
type backgroundWaitedMsg struct{}

// backgroundTimer ends the wait for the terminal's background colour.
func backgroundTimer() tea.Cmd {
	return tea.Tick(backgroundWait, func(time.Time) tea.Msg { return backgroundWaitedMsg{} })
}

// gotBackground picks the palette for the terminal's background colour,
// when it is the first answer within backgroundWait.
func (m *Model) gotBackground(msg tea.BackgroundColorMsg) {
	if m.paletteSettled {
		return
	}
	m.paletteSettled = true
	if !msg.IsDark() {
		m.usePalette(lightPalette)
	}
}

// usePalette sets the palette, and gives it to the parts of the model that
// keep a copy of their styles: the footer's help and the text inputs.
func (m *Model) usePalette(p palette) {
	usePalette(p)
	brandHelp(&m.help)
	brandInput(&m.filter)
	if m.logView != nil {
		brandInput(&m.logView.search)
	}
	if m.password != nil {
		brandInput(&m.password.input)
	}
}

// requestCellSize asks the terminal how big a cell is in pixels, on the first
// window size and every one after. Terminals that answer send a
// uv.CellSizeEvent; the rest stay silent. Window operation 16 is the cell size
// request.
var requestCellSize = tea.Raw(ansi.WindowOp(16))

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
	// A proposal's form that has gone without saving declines it, and the
	// next request, a proposal or a passthrough, opens once nothing else is
	// open.
	m.endProposal()
	// A proposal that ended while its form saved closes once the save has.
	ended := m.closeEndedProposal()
	// The advice of the selected project is read when it is not known, once
	// the selection has rested on it.
	return model, tea.Batch(cmd, ended, m.nextAsked(), m.waitForAdvice(), m.fetchGrants(), m.spin())
}

func (m *Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.gotBackground(msg)
	case tea.ColorProfileMsg:
		m.monochrome = msg.Profile <= colorprofile.ASCII && msg.Profile != colorprofile.Unknown
	case backgroundWaitedMsg:
		m.paletteSettled = true
	case spinner.TickMsg:
		return m, m.turn(msg)
	case adviceWaitMsg:
		m.adviceWaiting = ""
		if msg.key == m.sel {
			return m, m.readAdvice()
		}
	case adviceMsg:
		m.gotAdvice(msg)
	case replayedMsg:
		m.replayed(msg)
	case reconnectedMsg:
		if msg.err != nil {
			m.refused(rowKey(rowServer, msg.name), msg.err)
		}
	case recheckedMsg:
		key := rowKey(rowProject, msg.name)
		if msg.err != nil {
			m.refused(key, msg.err)
			return m, nil
		}
		// The log's source says who: "nearhost re-read .nearhostrc".
		m.logs.noteOn(key, "re-read "+nearhostrc.FileName)
	case changeDeclinedMsg:
		m.form = nil
		m.selectRow(rowKey(rowProject, msg.name))
		m.logs.noteOn(m.sel, "Declined what "+nearhostrc.FileName+" asks for, until the file changes again.")
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
		m.clearSelection()
		wasSmall := m.small()
		m.width, m.height = msg.Width, msg.Height
		// A new font size resizes the window, so the cells may have changed.
		if wasSmall && !m.small() && m.form != nil && m.form == m.guard.form {
			// A request box that opened while the window was too small is
			// drawn for the first time now, so its guard starts again.
			m.guard.opened = m.now()
			return m, tea.Batch(requestCellSize, guardTimer())
		}
		return m, requestCellSize
	case uv.CellSizeEvent:
		m.tallCells = cellsTall(msg.Width, msg.Height)
	case startedMsg:
		m.proxyRetrying = false
		if msg.err != nil {
			m.proxyErr = msg.err
			m.logs.note("The proxy could not start: " + msg.err.Error())
			return m, nil
		}
		m.proxyErr = nil
		m.started = true
		// The core starts the enabled projects itself, and has by now: a
		// project stopped since stays stopped until the user starts it.
		return m, m.readMachine() // the core may have just created the CA
	case eventMsg:
		m.logs.event(engine.Event(msg), m.cfg)
		m.trackUptime()
		return m, m.listen()
	case tickMsg:
		m.trackUptime()
		// The config the backend runs changes when its file does: the core
		// applies the file. A Reload that is running takes it when it ends.
		if cfg := m.b.Config(); cfg != m.cfg && !m.applying && !m.quitting {
			m.setConfig(cfg)
		}
		return m, tea.Batch(tick(), m.readMachine())
	case machineMsg:
		// A read that began before the trust step finished may have seen the
		// old bundle; it must not undo the success.
		if !msg.caTrusted && !msg.readAt.IsZero() && msg.readAt.Before(m.caTrustedAt) {
			msg.caTrusted = true
		}
		m.caTrusted, m.caExpires, m.hostsState = msg.caTrusted, msg.caExpires, msg.hostsState
		m.offerFirstRun()
	case firstRunSkippedMsg:
		m.form = nil
		m.selectRow(homeKey)
	case caTrustedMsg:
		m.caTrusted, m.caTrustedAt = true, time.Now()
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
		if m.quitting {
			// The quit cancelled that Reload, and nothing more is applied
			// while quitting: a change saved meanwhile waits in the file.
			m.reloadAfter, m.editing = false, false
			return m, nil
		}
		if m.reloadAfter {
			// A change was saved while that Reload ran: reload again.
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
			return m, m.alertProblem(msg.key, "Open "+msg.url+" in your browser.")
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
		if msg.unsaved != nil {
			m.logs.note("Could not save the answer to " + m.b.ConfigPath() + ": " + strings.TrimRight(msg.unsaved.Error(), ".") + ". The quit box asks again next time; to stop it asking, set keep_running in that file.")
		}
		if msg.err != nil {
			verb := "Stopping: "
			if m.leaving {
				verb = "Leaving: "
			}
			m.logs.note(verb + msg.err.Error())
		}
		m.unsubscribe()
		return m, tea.Quit
	case lostMsg:
		return m, m.lose(msg.err)
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
	case serverChosenMsg:
		m.form = nil
		return m, m.connectFromEmpty(msg.name)
	case serverAddedMsg:
		key := rowKey(rowServer, msg.srv.Name)
		verb := "Added server "
		if msg.edited {
			verb = "Saved server "
		}
		cmd := m.alert(key, verb+msg.srv.Name+".")
		if pf, ok := m.form.(*projectForm); ok && pf.sub != nil {
			cmd = tea.Batch(cmd, pf.update(msg)) // back to adding the project, on this server
		} else {
			m.form = nil
			m.selectRow(key)
		}
		return m, tea.Batch(cmd, m.applyNow())
	case proposalMsg:
		return m, m.gotProposal(msg.p)
	case passMsg:
		return m, m.gotPass(msg.r)
	case passOpenedMsg:
		return m, m.passOpened(msg)
	case docMsg:
		return m, m.gotDoc(msg.r)
	case docShownMsg:
		return m, m.docShown(msg)
	case requestEndedMsg:
		return m, m.requestEnded(msg.id)
	case docAnsweredMsg:
		return m, m.docAnswered(msg)
	case grantsMsg:
		m.gotGrants(msg)
	case passClosedMsg:
		m.passClosed(msg)
	case revokedMsg:
		m.revoked(msg)
	case treeOpenedMsg:
		m.treeOpened(msg)
	case treeRemovedMsg:
		m.treeRemoved(msg)
	case passAnsweredMsg:
		return m, m.passAnswered(msg)
	case projectAddedMsg:
		if pr := m.proposing; pr != nil && m.form == pr.form {
			if pr.ended {
				// Saved after another window answered: the agent has its
				// answer already.
				m.logs.note(fmt.Sprintf("Added %s. The agent's proposal %s was answered in another window while this one saved.", msg.name, pr.p.where()))
			} else {
				m.b.AcceptProposal(pr.p.ID, msg.name)
				m.logs.note(fmt.Sprintf("Added %s, as an agent %s proposed.", msg.name, pr.p.where()))
			}
			m.proposing = nil
		}
		m.form = nil
		m.selectRow(rowKey(rowProject, msg.name))
		verb := "Added "
		if msg.edited {
			verb = "Saved "
		}
		return m, tea.Batch(m.alert(m.sel, verb+msg.name+"."), m.applyNow())
	case changedMsg:
		m.form = nil
		m.selectRow(msg.sel)
		return m, tea.Batch(m.alert(m.sel, msg.note), m.applyNow())
	case passwordMsg:
		m.askPassword(msg)
	case passwordClosedMsg:
		if m.password == msg.box {
			m.password = nil
		}
	case passwordGoneMsg:
		if m.password != nil && m.password.done == msg.done {
			m.password.answer(context.Canceled) // nobody reads it now
			m.password = nil
		}
	case passwordCheckedMsg:
		if m.password != nil {
			return m, m.password.update(msg)
		}
	case tea.PasteMsg:
		if m.small() {
			return m, nil // the box it would go in is not drawn
		}
		if m.password != nil {
			return m, m.password.update(msg)
		}
		if m.quitBox != nil {
			return m, nil // it has nothing to type into
		}
		if m.form != nil {
			if m.guarding() {
				return m, nil
			}
			return m, m.form.update(msg)
		}
		return m, m.pasteText(msg.Content)
	case tea.KeyPressMsg:
		m.clearSelection()
		return m, m.key(msg)
	case tea.MouseClickMsg:
		if m.small() {
			return m, nil
		}
		m.mouseDown(msg.Mouse())
	case tea.MouseMotionMsg:
		if m.small() {
			return m, nil
		}
		m.mouseMove(msg.Mouse())
	case tea.MouseReleaseMsg:
		if m.small() {
			return m, nil
		}
		return m, m.mouseUp(msg.Mouse())
	case openFailedMsg:
		return m, m.alertProblem(msg.key, "Could not open "+msg.url+": "+msg.err.Error())
	case askGuardEndedMsg:
		return m, nil // the redraw shows the box's keys
	case toastDoneMsg:
		m.toastDone(msg)
	case copyFailedMsg:
		return m, m.alertProblem(msg.key, "Could not copy to the clipboard: "+msg.err.Error()+". The terminal may still have copied it.")
	case tea.MouseWheelMsg:
		if m.small() {
			return m, nil
		}
		m.clearSelection() // the text under it moves
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
// The selected route stays on its port, which a change may have moved.
func (m *Model) setConfig(cfg *config.Config) {
	old := m.cfg
	kind, name := m.selected()
	port := 0
	if p, ok := old.Project(name); ok && kind == rowProject && m.routeRow < len(p.Routes) {
		port = p.Routes[m.routeRow].Port
	}
	m.cfg = cfg
	if p, ok := cfg.Project(name); ok && port != 0 {
		for i, r := range p.Routes {
			if r.Port == port {
				m.routeRow = i
			}
		}
	}
	for _, p := range cfg.Projects {
		if _, existed := old.Project(p.Name); !existed {
			delete(m.collapsed, p.Server) // so the new project's row is shown
			m.selectRow(rowKey(rowProject, p.Name))
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
		readAt := time.Now()
		return machineMsg{readAt, m.b.CATrusted(), m.b.CAExpires(), m.b.HostsState()}
	}
}

// View draws the screen, with any text selected with the mouse highlighted.
func (m *Model) View() tea.View {
	v := tea.NewView(m.withToast(m.highlight(m.content())))
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// content is the screen as drawn, without the selection.
func (m *Model) content() string {
	var content string
	if m.small() {
		content = m.tooSmall()
	} else {
		if m.logView != nil {
			content = m.logScreen()
		} else if m.empty() {
			content = m.emptyScreen()
		} else {
			content = m.frame(m.columns(), m.footer())
		}
		if m.form != nil {
			content = m.formBox(content, m.form)
		}
		if m.quitBox != nil {
			content = m.formBox(content, m.quitBox)
		}
		if m.password != nil {
			content = m.formBox(content, m.password)
		}
	}
	return content
}

// main draws the main panel: the selected item's details, then its log,
// exactly height lines of width cells, and the log's scrollbar, a cell a
// line, for the caller to draw at the right.
func (m *Model) main(width, height int) ([]string, []string) {
	var details []string
	kind, name := m.selected()
	switch kind {
	case rowServer:
		details = m.serverPanel(name, width, height-3)
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
	for range m.gap() {
		lines = append(lines, strings.Repeat(" ", width))
	}
	lines = append(lines, section(label, follow, width))
	// The newest entries that fit, counted back from the end less what is
	// scrolled.
	room := height - len(lines)
	var shown []string
	entries := 0
	for i := len(log) - 1 - m.scroll; i >= 0 && len(shown) < room; i-- {
		shown = append(entryLines(log[i], width, kind != rowProject), shown...)
		entries++
	}
	if len(shown) > room {
		shown = shown[len(shown)-room:]
	}
	// The log's scrollbar counts entries, as the wheel scrolls them.
	bar := make([]string, len(lines), height)
	for i := range bar {
		bar[i] = " "
	}
	bar = append(bar, scrollbarOf(len(log)-m.scroll-entries, entries, len(log), room)...)
	lines = append(lines, shown...)
	for len(lines) < height {
		lines = append(lines, strings.Repeat(" ", width))
	}
	for len(bar) < height {
		bar = append(bar, " ")
	}
	return lines, bar
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

// applyNow has the backend apply the config file now, after a form saved
// to it. While a Reload runs it waits for that one to end, then reloads.
func (m *Model) applyNow() tea.Cmd {
	if m.applying {
		m.reloadAfter = true
		return nil
	}
	return m.reloadConfig()
}

// reloadConfig has the backend apply the config file.
func (m *Model) reloadConfig() tea.Cmd {
	m.applying = true
	ctx, cancel := context.WithCancel(context.Background())
	m.endApply = cancel
	b := m.b
	return func() tea.Msg {
		defer cancel()
		return appliedMsg{b.Reload(ctx)}
	}
}
