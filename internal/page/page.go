// Package page is the status and approvals part of Nearhost's page at
// nearhost.localhost: what the core serves, and the requests that wait for
// the user's answer. The proxy's gate sends it only requests that carry the
// page's cookie, and POSTs only from the page itself with its form token.
package page

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/report/htmlview"
	"github.com/nearhost/nearhost/internal/safetext"
	"github.com/nearhost/nearhost/internal/tui"
	"golang.org/x/net/publicsuffix"
)

// Core is what the page asks of the core.
type Core interface {
	Status() control.Status
	State() control.State
	AnswerPassthrough(ctx context.Context, id, answer string) error
	AnswerDocument(ctx context.Context, id, answer string) error
	DeclineProposal(id, reason string)
	AcceptChange(project, sha string) error
	DeclineChange(project, sha string)
}

// Options are what New serves the page from.
type Options struct {
	Core Core
	// Watch signals each change to what the page shows: the hub's Watch.
	// nil sends no changes.
	Watch func() (<-chan struct{}, func())
	// Trees lists the documents' trees, as / lists them without the page.
	// nil lists none.
	Trees func() []proxy.HomeTree
	// Now is the clock ages are told by; nil is time.Now.
	Now func() time.Time
	// Done, once closed, ends every event stream, so a page no longer set
	// holds no request open. nil never ends them.
	Done <-chan struct{}
}

// partHeader asks for the <main> element's contents alone, which page.js
// puts in place of the shown ones.
const partHeader = "X-Nearhost-Part"

// csrfToken is the form token every form carries. Tests replace it.
var csrfToken = proxy.CSRFToken

//go:embed templates/*.html
var templateFiles embed.FS

//go:embed static/page.js
var pageJS []byte

// views are the page's views, each the layout with its own content.
var views = map[string]*template.Template{}

func init() {
	layout := template.Must(template.New("").ParseFS(templateFiles, "templates/layout.html"))
	for _, name := range []string{"status", "approve", "change"} {
		v := template.Must(template.Must(layout.Clone()).ParseFS(templateFiles, "templates/"+name+".html"))
		for _, part := range htmlview.Template().Templates() {
			template.Must(v.AddParseTree(part.Name(), part.Tree))
		}
		views[name] = v
	}
}

// page serves the page's routes.
type page struct {
	o   Options
	mux *http.ServeMux
}

// New returns the handler of the page's routes: GET and HEAD for /,
// /approve/{id}, /change/{name}, /events and /page.js, and POST for
// /approve/{id} and /change/{name}. It expects the gate in front of it.
func New(o Options) http.Handler {
	if o.Now == nil {
		o.Now = time.Now
	}
	p := &page{o: o, mux: http.NewServeMux()}
	p.mux.HandleFunc("GET /{$}", p.status)
	p.mux.HandleFunc("GET /approve/{id}", p.approval)
	p.mux.HandleFunc("POST /approve/{id}", p.answer)
	p.mux.HandleFunc("GET /change/{name}", p.change)
	p.mux.HandleFunc("POST /change/{name}", p.answerChange)
	p.mux.HandleFunc("GET /events", p.events)
	p.mux.HandleFunc("GET /page.js", script)
	return p.mux
}

// base is what every view carries: its title, the running version, and the
// form token.
type base struct {
	Title   string
	Version string
	CSRF    string
}

func (b base) title() string { return b.Title }

// titled is every view: each carries a base.
type titled interface{ title() string }

// titleHeader carries a part's title, path-escaped, for page.js to put
// on the tab: a part replaces <main> alone.
const titleHeader = "X-Nearhost-Title"

func (p *page) base(title string) base {
	token, _ := csrfToken() // the gate let the request in, so it reads
	return base{Title: title, Version: p.o.Core.Status().Info.Version, CSRF: token}
}

// render answers r with view filled from data and status: the whole page,
// or the <main> element's contents alone when r asks for them. A HEAD gets
// the headers alone.
func render(w http.ResponseWriter, r *http.Request, view string, status int, data any) {
	name := "layout"
	if r.Header.Get(partHeader) == "main" {
		name = "main"
	}
	var buf bytes.Buffer
	if err := views[view].ExecuteTemplate(&buf, name, data); err != nil {
		htmlview.Write(w, r, report.Errorf(report.PageRenderFailed, "Nearhost's page could not be shown").Because("%s", err), http.StatusInternalServerError, pageName)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(buf.Len()))
	if name == "main" {
		// The marker tells page.js that this is the page's own part, not a
		// page from something else answering at the address meanwhile.
		h.Set(partHeader, "main")
		if v, ok := data.(titled); ok {
			h.Set(titleHeader, url.PathEscape(v.title()))
		}
	}
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		w.Write(buf.Bytes())
	}
}

// script serves page.js.
func script(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/javascript")
	h.Set("Content-Length", strconv.Itoa(len(pageJS)))
	if r.Method != http.MethodHead {
		w.Write(pageJS)
	}
}

// statusView is what / shows.
type statusView struct {
	base
	Update    string // a newer release, without "v"; "" when none is known
	Started   string // an age: "3 min ago"
	StartedBy string
	WebPort   int
	// Address is where the page was asked: host and port, as the intro
	// names them.
	Address string
	// AtNetwork is whether the page was asked at its network host, where
	// projects come first.
	AtNetwork    bool
	Servers      []serverRow
	Projects     []projectRow
	Passthroughs []passRow
	Approvals    []approvalRow
	Trees        []proxy.HomeTree
}

type serverRow struct {
	Name, Health, Message string
	// Problem is Message as a message, with its fix steps; nil when the
	// core sent none.
	Problem *htmlview.Data
}

// projectRow is a project in the list. URL is the address for the host the
// page was asked at; Preview is the page's own view of it, set only when URL
// is same-site with that host.
type projectRow struct {
	Name, State, URL, Message string
	Problem                   *htmlview.Data
	Preview                   string
}

type passRow struct {
	Who, Label, Opened string
	Remote, Local      int
}

type approvalRow struct{ Href, Text string }

// status serves /: the core and what it serves, the requests that wait for
// an answer, and the documents' trees.
func (p *page) status(w http.ResponseWriter, r *http.Request) {
	st, state := p.o.Core.Status(), p.o.Core.State()
	now := p.o.Now()
	v := statusView{
		base:      p.base("Nearhost"),
		Started:   age(now, st.Info.Started),
		StartedBy: startedBy(st.Info.StartedBy),
		Update:    safetext.Line(st.Update),
		WebPort:   state.WebPort,
	}
	for _, s := range st.Servers {
		row := serverRow{Name: serverName(s.Name), Health: s.State, Message: safetext.Line(s.Message)}
		row.Problem = problemData(s.Problem)
		if h, ok := state.Servers[s.Name]; ok {
			row.Health = health(h)
		}
		v.Servers = append(v.Servers, row)
	}
	netPage := networkOf(st)
	v.AtNetwork = netPage.at(r)
	v.Address = "nearhost.localhost:" + strconv.Itoa(state.WebPort)
	if v.AtNetwork {
		v.Address = netPage.authority
	}
	for _, pr := range st.Projects {
		row := projectRow{Name: safetext.Line(pr.Name), State: pr.State, URL: netPage.addressFor(r, pr), Message: safetext.Line(pr.Message), Problem: problemData(pr.Problem)}
		if row.URL != "" && sameSite(row.URL, r.Host) {
			row.Preview = "/preview/" + url.PathEscape(pr.Name)
		}
		v.Projects = append(v.Projects, row)
	}
	for _, pt := range state.Passthroughs {
		v.Passthroughs = append(v.Passthroughs, passRow{
			Who:    tui.RequesterName(pt.Requester) + " " + onServer(pt.Server),
			Label:  safetext.Line(pt.Label),
			Opened: age(now, pt.Opened),
			Remote: pt.Remote, Local: pt.Local,
		})
	}
	for _, a := range state.Approvals {
		v.Approvals = append(v.Approvals, approvalRow{Href: "/approve/" + url.PathEscape(a.ID), Text: summary(a)})
	}
	for _, name := range sortedNames(state.Projects) {
		if state.Projects[name].Pending != nil {
			v.Approvals = append(v.Approvals, approvalRow{
				Href: "/change/" + url.PathEscape(name),
				Text: fmt.Sprintf("The .nearhostrc of %s asks for changes that need your approval", safetext.Line(name)),
			})
		}
	}
	if p.o.Trees != nil {
		v.Trees = p.o.Trees()
	}
	render(w, r, "status", http.StatusOK, v)
}

// networkPage is the page's network address, parsed once: its host and
// port. Its zero value is no network address.
type networkPage struct{ host, authority string }

// networkOf parses the page's network address from st.
func networkOf(st control.Status) networkPage {
	if st.Network == nil || st.Network.URL == "" {
		return networkPage{}
	}
	u, err := url.Parse(st.Network.URL)
	if err != nil || u.Hostname() == "" {
		return networkPage{}
	}
	return networkPage{host: strings.ToLower(u.Hostname()), authority: u.Host}
}

// at reports whether r was asked at the page's network host.
func (n networkPage) at(r *http.Request) bool {
	return n.host != "" && n.host == hostOnly(r.Host)
}

// addressFor is the address of p to show for the host r was asked at: its
// network address at the page's network host, and its local one everywhere
// else. A project the core does not serve on the network has none there.
func addressFor(r *http.Request, st control.Status, p control.ProjectInfo) string {
	return networkOf(st).addressFor(r, p)
}

func (n networkPage) addressFor(r *http.Request, p control.ProjectInfo) string {
	if n.at(r) {
		return p.NetworkURL
	}
	return p.URL
}

// hostOnly is s as a host without its port: s may be a host, a host and
// port, or an address with a scheme.
func hostOnly(s string) string {
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil {
			s = u.Host
		}
	}
	if h, _, err := net.SplitHostPort(s); err == nil {
		s = h
	}
	return strings.ToLower(strings.Trim(s, "[]"))
}

// sameSite reports whether hosts a and b, with or without ports (or a
// scheme), share a registrable domain, compared without regard to case.
// Addresses and hosts with no registrable domain are the same site only when
// they are the same host.
func sameSite(a, b string) bool {
	a, b = hostOnly(a), hostOnly(b)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	if net.ParseIP(a) != nil || net.ParseIP(b) != nil {
		return false
	}
	da, err := publicsuffix.EffectiveTLDPlusOne(a)
	if err != nil {
		return false
	}
	db, err := publicsuffix.EffectiveTLDPlusOne(b)
	return err == nil && da == db
}

// problemData is m ready for the page; nil for no message.
func problemData(m *report.Message) *htmlview.Data {
	if m == nil {
		return nil
	}
	d := htmlview.Prepare(m)
	return &d
}

// health says how a server's connection is, as the interface does.
func health(s control.ServerState) string {
	switch {
	case s.Health.Held:
		return "disconnected by you"
	case s.Health.Failures > 1:
		return fmt.Sprintf("%s, after %d failed attempts", s.Status, s.Health.Failures)
	case s.Health.Failures == 1:
		return fmt.Sprintf("%s, after a failed attempt", s.Status)
	}
	return string(s.Status)
}

// startedBy says who started the core, in a sentence.
func startedBy(s control.StartedBy) string {
	switch s {
	case control.StartedService:
		return "the login service"
	case control.StartedDetached:
		return "nearhost -d"
	case control.StartedTUI:
		return "the nearhost interface"
	case control.StartedForeground:
		return "a terminal"
	}
	return "an unknown way"
}

// serverName names a server in a list: this machine, or the server.
func serverName(s string) string {
	if s == config.LocalServer {
		return "This machine"
	}
	return safetext.Line(s)
}

// onServer names a request's machine in a sentence: "on leto", "on this
// machine".
func onServer(s string) string {
	if s == config.LocalServer {
		return "on this machine"
	}
	return "on " + safetext.Line(s)
}

// age says how long before now t was: "just now", "3 min ago", "2 hours
// ago", "4 days ago". The zero time has no age.
func age(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d/time.Minute))
	case d < 2*time.Hour:
		return "an hour ago"
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d/time.Hour))
	}
	return fmt.Sprintf("%d days ago", int(d/(24*time.Hour)))
}
