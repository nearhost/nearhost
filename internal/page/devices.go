package page

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/report/htmlview"
	"github.com/nearhost/nearhost/internal/safetext"
)

// deviceRow is a paired device in the list. Paired and LastSeen are ages,
// "" when the device has none; Revoke is its one form.
type deviceRow struct {
	Name, Paired, LastSeen, LastAddr string
	Revoke                           []button
}

// devicesView is what /devices shows.
type devicesView struct {
	base
	// Machine is this machine's name, "" when it is not known.
	Machine string
	Devices []deviceRow
}

// networked reports whether the core serves to the network, which is when
// devices pair with it.
func (p *page) networked() bool {
	n := p.o.Core.Status().Network
	return n != nil && n.On
}

// notNetworked answers a request for a devices view of a core that is not
// networked: it has none.
func notNetworked(w http.ResponseWriter, r *http.Request) {
	htmlview.Write(w, r, report.Errorf(report.ProxyNotFound, "Nearhost has no paired devices to show").
		Because("This core does not serve to the network. Turn on the network section of the config first."), http.StatusNotFound, pageName)
}

// devices serves /devices: the devices paired with this core, each with a
// button to revoke it.
func (p *page) devices(w http.ResponseWriter, r *http.Request) {
	if !p.networked() {
		notNetworked(w, r)
		return
	}
	list, err := p.o.Core.Devices()
	if err != nil {
		htmlview.Write(w, r, deviceFailure(report.PageAnswerFailed, "Nearhost could not list the paired devices", err), http.StatusInternalServerError, pageName)
		return
	}
	v := devicesView{base: p.base("Paired devices"), Machine: safetext.Line(p.o.Machine)}
	now := p.o.Now()
	for _, d := range list {
		action := "/devices/" + url.PathEscape(d.Name) + "/revoke"
		v.Devices = append(v.Devices, deviceRow{
			Name:     safetext.Line(d.Name),
			Paired:   age(now, d.Paired),
			LastSeen: age(now, d.LastSeen),
			LastAddr: safetext.Line(d.LastAddr),
			Revoke:   []button{{Action: action, CSRF: v.CSRF, Label: "Revoke", Quiet: true}},
		})
	}
	render(w, r, "devices", http.StatusOK, v)
}

// revoke serves POST /devices/{name}/revoke: it unpairs the device, then
// sends the browser back to the list. A name no device has is not found.
func (p *page) revoke(w http.ResponseWriter, r *http.Request) {
	if !p.networked() {
		notNetworked(w, r)
		return
	}
	name := r.PathValue("name")
	switch err := p.o.Core.RevokeDevice(name); {
	case errors.Is(err, control.ErrDeviceUnknown):
		htmlview.Write(w, r, report.Errorf(report.DeviceUnknown, "No paired device is called %s", safetext.Line(name)).
			Because("It may have been revoked already. Open the list of paired devices again."), http.StatusNotFound, pageName)
	case err != nil:
		htmlview.Write(w, r, deviceFailure(report.PageAnswerFailed, "Nearhost could not revoke that device", err), http.StatusInternalServerError, pageName)
	default:
		http.Redirect(w, r, "/devices", http.StatusSeeOther)
	}
}

// deviceFailure is err as a message: its own when it is one, otherwise
// code and title with err as the reason.
func deviceFailure(code report.Code, title string, err error) *report.Message {
	if m, ok := report.As(err); ok {
		return m
	}
	return report.Errorf(code, "%s", title).Because("%s", err)
}
