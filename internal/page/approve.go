package page

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"

	"github.com/nearhost/nearhost/internal/agentapi"
	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/documents"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/nearhostrc"
	"github.com/nearhost/nearhost/internal/safetext"
	"github.com/nearhost/nearhost/internal/tui"
)

// declineReason is what an agent is told of a proposal declined here.
const declineReason = "declined on Nearhost's page"

// answerLabels are the answers to a request, as the page names them.
var answerLabels = map[string]string{"yes": "Yes", "no": "No", "always": "Always allow"}

// changeLabels are the answers to a .nearhostrc change, as the page names
// them once given.
var changeLabels = map[string]string{"approve": "Approved", "decline": "Declined"}

// button is one form of a view, with one button: it posts Value as the
// answer, and SHA when it is set.
type button struct {
	Action, CSRF, Value, Label, SHA string
	// Quiet is set for the buttons that are not the view's first choice.
	Quiet bool
}

// approveView is what /approve/{id} shows.
type approveView struct {
	base
	// Lines say what is asked, in full.
	Lines []string
	// Advice follows the lines: what to do instead of answering here.
	Advice  string
	Buttons []button
	// Error is why the answer given failed. With Gone set, it is why the
	// answer ended the request.
	Error string
	// Answered is the answer given, once it has been, and Gone is set when
	// the request waits no longer: either replaces the request.
	Answered string
	Gone     bool
}

// pending is the approval id that waits now.
func (p *page) pending(id string) (control.Approval, bool) {
	for _, a := range p.o.Core.State().Approvals {
		if a.ID == id {
			return a, true
		}
	}
	return control.Approval{}, false
}

// approval serves /approve/{id}: the request, and the answers it takes.
// With answered set to an answer, once the request waits no longer, it says
// the answer was given.
func (p *page) approval(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a, ok := p.pending(id)
	if !ok {
		if label, given := answerLabels[r.URL.Query().Get("answered")]; given {
			render(w, r, "approve", http.StatusOK, approveView{base: p.base("Answered"), Answered: label})
			return
		}
		p.gone(w, r, http.StatusNotFound)
		return
	}
	render(w, r, "approve", http.StatusOK, p.approveView(a, true))
}

// gone answers with the view that says the request waits no longer.
func (p *page) gone(w http.ResponseWriter, r *http.Request, status int) {
	render(w, r, "approve", status, approveView{base: p.base("Nothing to answer"), Gone: true})
}

// answer serves POST /approve/{id}: it gives the core the answer in the
// form's answer field, then sends the browser to the view that says it was
// given. An answer the request does not offer is refused, and nothing is
// called. A request that no longer waits, or that another answer still
// opens, gets 409, and an answer the core
// could not give shows its error on the request's view, as does one that
// ended the request because what it asked for could not be opened.
func (p *page) answer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ans := r.PostFormValue("answer")
	a, ok := p.pending(id)
	if !ok {
		p.gone(w, r, http.StatusConflict)
		return
	}
	if !slices.Contains(offered(a), ans) {
		if a.Proposal != nil {
			http.Error(w, "A proposal can only be declined here. Run nearhost on this machine to review it.", http.StatusBadRequest)
			return
		}
		http.Error(w, "That answer is not one this request offers. Open the request again and choose one of its buttons.", http.StatusBadRequest)
		return
	}
	// The browser going away does not cancel an open midway, which would
	// decline the agent's request with "context canceled": the open has
	// the agent API's own time instead.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), agentapi.OpenTimeout)
	defer cancel()
	var err error
	switch {
	case a.Proposal != nil:
		p.o.Core.DeclineProposal(id, declineReason)
	case a.Pass != nil:
		err = p.o.Core.AnswerPassthrough(ctx, id, ans)
	case a.Doc != nil:
		err = p.o.Core.AnswerDocument(ctx, id, ans)
	}
	switch {
	case errors.Is(err, agentapi.ErrNotOpened):
		// The answer ended the request, as what it asked for could not be
		// opened: say why, with nothing left to answer.
		v := p.approveView(a, false)
		v.Gone, v.Error = true, safetext.Line(err.Error())
		render(w, r, "approve", http.StatusBadGateway, v)
		return
	case errors.Is(err, control.ErrRequestEnded), errors.Is(err, agentapi.ErrBeingAnswered):
		// Another answer won, or still opens it: this one has lost.
		p.gone(w, r, http.StatusConflict)
		return
	case err != nil:
		// The request may still wait, as one whose answer was refused does:
		// it can be answered again.
		_, still := p.pending(id)
		v := p.approveView(a, still)
		v.Error = safetext.Line(err.Error())
		render(w, r, "approve", http.StatusBadGateway, v)
		return
	}
	http.Redirect(w, r, "/approve/"+url.PathEscape(id)+"?answered="+ans, http.StatusSeeOther)
}

// offered lists the answers a takes, in the order its buttons show them.
func offered(a control.Approval) []string {
	switch {
	case a.Proposal != nil:
		return []string{"no"}
	case a.Pass != nil && a.Pass.CanAlways, a.Doc != nil && a.Doc.CanAlways:
		return []string{"yes", "no", "always"}
	case a.Pass != nil, a.Doc != nil:
		return []string{"yes", "no"}
	}
	return nil
}

// approveView is the view of a, with its buttons when withButtons is set.
func (p *page) approveView(a control.Approval, withButtons bool) approveView {
	var v approveView
	switch {
	case a.Pass != nil:
		v = approveView{base: p.base("Open a port"), Lines: passLines(a.Pass)}
	case a.Doc != nil:
		v = approveView{base: p.base("Show a document"), Lines: docLines(a.Doc)}
	case a.Proposal != nil:
		v = approveView{base: p.base("Add a project"), Lines: proposalLines(a.Proposal),
			Advice: "Run nearhost on this machine to review it."}
	}
	if !withButtons {
		return v
	}
	action := "/approve/" + url.PathEscape(a.ID)
	for i, ans := range offered(a) {
		v.Buttons = append(v.Buttons, button{Action: action, CSRF: v.CSRF, Value: ans, Label: answerLabels[ans], Quiet: i > 0})
	}
	return v
}

// summary says what a asks, in a line of the list of what waits.
func summary(a control.Approval) string {
	switch {
	case a.Pass != nil:
		return fmt.Sprintf("%s %s wants to open port %d", tui.RequesterName(a.Pass.Requester), onServer(a.Pass.Origin), a.Pass.Remote)
	case a.Doc != nil:
		return fmt.Sprintf("%s %s wants to show %s", tui.RequesterName(a.Doc.Requester), onServer(a.Doc.Origin), safetext.Line(a.Doc.Doc))
	case a.Proposal != nil:
		return fmt.Sprintf("A coding agent %s proposes adding %s", onServer(a.Proposal.Origin), safetext.Line(a.Proposal.Path))
	}
	return "A coding agent asks for something"
}

// withLabel is s with the requester's label after it in brackets, if any.
func withLabel(s, label string) string {
	if l := safetext.Line(label); l != "" {
		return s + " (" + l + ")"
	}
	return s
}

// passLines say what a passthrough request asks, as the interface's prompt
// does.
func passLines(r *agentapi.PassRequest) []string {
	who := tui.RequesterName(r.Requester) + " " + onServer(r.Origin)
	lines := []string{withLabel(fmt.Sprintf("%s wants to open port %d on this laptop", who, r.Remote), r.Label) + "."}
	if r.LocalTaken {
		lines = append(lines, fmt.Sprintf("Port %d is in use here, so it will open on another port.", r.Remote))
	} else {
		lines = append(lines, fmt.Sprintf("It opens on port %d here.", r.Remote))
	}
	return lines
}

// docLines say what a document request asks, as the interface's prompt
// does.
func docLines(r *agentapi.DocRequest) []string {
	who := tui.RequesterName(r.Requester) + " " + onServer(r.Origin)
	root := safetext.Line(r.Root)
	lines := []string{withLabel(who+" wants to show "+safetext.Line(r.Doc), r.Label) + "."}
	if documents.InHiddenFolder(r.Root) {
		lines = append(lines, "Nearhost will serve files in "+root+". It is in a hidden folder, so only documents and their assets are served from it.")
	} else {
		lines = append(lines, "Nearhost will serve files in "+root+", except dotfiles.")
	}
	if r.NewRoot && r.CanAlways {
		lines = append(lines, tui.RequesterName(r.Requester)+" has Always allow "+onServer(r.Origin)+", but not for this folder. Always allow adds it.")
	}
	return lines
}

// proposalLines say what a proposal to add a project asks.
func proposalLines(r *agentapi.Proposal) []string {
	lines := []string{"A coding agent " + onServer(r.Origin) + " proposes adding " + safetext.Line(r.Path) + "."}
	if n := safetext.Line(r.Name); n != "" {
		lines = append(lines, "It would be named "+n+".")
	} else {
		lines = append(lines, "It did not say what to name it.")
	}
	if r.Web != 0 {
		lines = append(lines, fmt.Sprintf("Its web server listens on port %d.", r.Web))
	} else {
		lines = append(lines, "It did not say which port its web server listens on.")
	}
	return lines
}

// changeView is what /change/{name} shows.
type changeView struct {
	base
	// Items are what needs approval, each with why.
	Items []changeItem
	// Moves say what is served now that the change moves or removes.
	Moves    []string
	Buttons  []button
	Error    string
	Answered string
	Gone     bool
}

// changeItem is one thing a change needs approval for, and a note on it.
type changeItem struct{ Text, Note string }

// realDomainNote follows a host that is a real domain. The page looks up
// no address, so it says what serving one does, not where it resolves.
const realDomainNote = "Serving it here hides the real site on this machine."

// change serves /change/{name}: what the project's .nearhostrc asks for
// that needs approval, and the answers it takes. With answered set to an
// answer, once no change waits, it says the answer was given. An approval
// is saved at once but applied when the config is next read, so with sha
// set too it says so while the change it approved still waits.
func (p *page) change(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	pending := p.o.Core.State().Projects[name].Pending
	q := r.URL.Query()
	label, given := changeLabels[q.Get("answered")]
	applying := pending != nil && q.Get("answered") == "approve" && q.Get("sha") != "" && q.Get("sha") == pending.SHA256
	if given && (pending == nil || applying) {
		render(w, r, "change", http.StatusOK, changeView{base: p.base("Answered"), Answered: label})
		return
	}
	if pending == nil {
		render(w, r, "change", http.StatusNotFound, changeView{base: p.base("Nothing to review"), Gone: true})
		return
	}
	render(w, r, "change", http.StatusOK, p.changeView(name, pending))
}

// changeView is the view of the change pending for the project name, in
// full as the interface's review shows it: each item with why it needs
// approval, and what the change moves or removes.
func (p *page) changeView(name string, c *engine.Change) changeView {
	v := changeView{base: p.base("Review " + nearhostrc.FileName + " for " + safetext.Line(name))}
	for _, it := range c.Items {
		item := changeItem{Text: safetext.Line(it.Name()) + " (" + it.Why + ")"}
		if it.Host != "" {
			item.Text = fmt.Sprintf("%s on port %d (%s)", safetext.Line(it.Host), it.Port, it.Why)
			if it.Why == nearhostrc.WhyRealDomain {
				item.Note = realDomainNote
			}
		}
		v.Items = append(v.Items, item)
	}
	if cfg, err := config.Parse([]byte(p.o.Core.State().Config)); err == nil {
		if old, ok := cfg.Project(name); ok {
			for _, m := range nearhostrc.Moves(*old, c.Project) {
				v.Moves = append(v.Moves, safetext.Line(m))
			}
		}
	}
	action := "/change/" + url.PathEscape(name)
	v.Buttons = []button{
		{Action: action, CSRF: v.CSRF, Value: "approve", Label: "Approve", SHA: c.SHA256},
		{Action: action, CSRF: v.CSRF, Value: "decline", Label: "Decline", SHA: c.SHA256, Quiet: true},
	}
	return v
}

// answerChange serves POST /change/{name}: it approves or declines the
// change the form's sha names. A change that is no longer the one pending
// gets 409 and the view of the one that is, if any.
func (p *page) answerChange(w http.ResponseWriter, r *http.Request) {
	name, ans, sha := r.PathValue("name"), r.PostFormValue("answer"), r.PostFormValue("sha")
	if _, ok := changeLabels[ans]; !ok || sha == "" {
		http.Error(w, "The answer must be approve or decline, for the change shown. Open the change again and choose one of its buttons.", http.StatusBadRequest)
		return
	}
	var err error
	switch pending := p.o.Core.State().Projects[name].Pending; {
	case ans == "approve":
		err = p.o.Core.AcceptChange(name, sha)
	case pending == nil || pending.SHA256 != sha:
		err = fmt.Errorf("the %s changed again; review it again", nearhostrc.FileName)
	default:
		p.o.Core.DeclineChange(name, sha)
	}
	if err != nil {
		// With no change waiting, another answer has ended it; otherwise
		// the one that waits now is shown, with why this answer failed.
		v := changeView{base: p.base("Review " + nearhostrc.FileName + " for " + safetext.Line(name)), Gone: true}
		if now := p.o.Core.State().Projects[name].Pending; now != nil {
			v = p.changeView(name, now)
			v.Error = safetext.Line(err.Error())
		}
		render(w, r, "change", http.StatusConflict, v)
		return
	}
	answered := url.Values{"answered": {ans}}
	if ans == "approve" {
		answered.Set("sha", sha)
	}
	http.Redirect(w, r, "/change/"+url.PathEscape(name)+"?"+answered.Encode(), http.StatusSeeOther)
}

// sortedNames lists m's keys in order.
func sortedNames[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	slices.Sort(names)
	return names
}
