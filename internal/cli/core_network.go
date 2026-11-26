package cli

import (
	"errors"
	"net/url"
	"strings"

	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/devices"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/report"
)

// Pair gives a new pairing code, and the address on the network where a
// device types it.
func (o *coreOps) Pair() (control.PairInfo, error) {
	ns := o.eng.NetworkState()
	if !ns.On {
		return control.PairInfo{}, control.ErrNetworkOff
	}
	if ns.URL == "" || o.devices == nil {
		return control.PairInfo{}, report.Errorf(report.PairUnavailable, "Nearhost's page isn't served, so no device can pair.")
	}
	pc := o.devices.NewPairCode()
	return control.PairInfo{Code: pc.Code, URL: ns.URL + "/pair/" + pc.Code, Expires: pc.Expires}, nil
}

// PairStatus says what became of a pairing code.
func (o *coreOps) PairStatus(code string) (control.PairState, error) {
	if o.devices == nil {
		return control.PairState{}, control.ErrNetworkOff
	}
	device, paired, expired := o.devices.PairStatus(code)
	return control.PairState{Device: device, Paired: paired, Expired: expired}, nil
}

// Devices lists the paired devices.
func (o *coreOps) Devices() ([]control.DeviceInfo, error) {
	if o.devices == nil {
		return nil, nil
	}
	list, err := o.devices.List()
	if err != nil {
		return nil, err
	}
	return deviceInfos(list), nil
}

// deviceInfos is list as the control API gives it.
func deviceInfos(list []devices.Device) []control.DeviceInfo {
	out := make([]control.DeviceInfo, 0, len(list))
	for _, d := range list {
		out = append(out, control.DeviceInfo{Name: d.Name, Paired: d.Paired, LastSeen: d.LastSeen, LastAddr: d.LastAddr})
	}
	return out
}

// RevokeDevice unpairs a device.
func (o *coreOps) RevokeDevice(name string) error {
	if o.devices == nil {
		return control.ErrDeviceUnknown
	}
	err := o.devices.Revoke(name)
	if errors.Is(err, devices.ErrNoDevice) {
		return control.ErrDeviceUnknown
	}
	return err
}

// networkInfo is how the core serves to the network, for status; nil when
// it does not.
func (o *coreOps) networkInfo() *control.NetworkInfo {
	if o.eng == nil {
		return nil
	}
	ns := o.eng.NetworkState()
	if !ns.On && ns.Problem == "" {
		return nil
	}
	return &control.NetworkInfo{On: ns.On, URL: ns.URL, Certificates: ns.Certificates, Problem: ns.Problem}
}

// networkProjectURL is the address of a project on the network.
func (o *coreOps) networkProjectURL(project string) string {
	ns := o.eng.NetworkState()
	return networkSiteURL(ns, o.eng.Sites(), project, o.httpsPort)
}

// networkSiteURL is the address of a project on the network: the first of
// its sites' hosts under the network domain, on the https port, where sites
// are served (the page's own port is the web port). It is "" when the core
// does not serve to the network or the project has no host there.
func networkSiteURL(ns engine.NetworkState, sites []proxy.Site, project string, httpsPort int) string {
	if !ns.On || ns.URL == "" {
		return ""
	}
	page, err := url.Parse(ns.URL)
	if err != nil {
		return ""
	}
	domain := strings.TrimPrefix(page.Hostname(), "nearhost.")
	for _, s := range sites {
		if s.Project != project {
			continue
		}
		for _, h := range s.Hosts {
			h = strings.ToLower(h)
			if h == domain || strings.HasSuffix(h, "."+domain) {
				return siteURL(h, httpsPort)
			}
		}
	}
	return ""
}
