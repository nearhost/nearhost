package cli

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/nearhost/nearhost/internal/config"
)

// localOps is what the interface does in its own process: the terminal,
// this machine's trust store and hosts file, the user's ssh for the
// add-project wizard, and writes to the config file.
type localOps struct {
	b       uiBackend
	path    string
	flags   serveFlags
	storage string
	// root runs steps that need root.
	root asRoot
	// running is the config Nearhost serves, which the hosts state checks
	// the hosts file against.
	running func() *config.Config
	// hostsAsked is set by the first HostsState call.
	hostsAsked atomic.Bool
}

func (u *localOps) CATrusted() bool { return untrustedCA(u.b.up.trust, u.storage) == "" }

func (u *localOps) CAExpires() time.Time {
	if u.b.up.trust.rootCert == nil {
		return time.Time{}
	}
	cert, err := u.b.up.trust.rootCert(u.storage)
	if err != nil {
		return time.Time{}
	}
	return cert.NotAfter
}

func (u *localOps) HostsState() string {
	if u.flags.noHosts {
		return "not managed (--no-hosts)"
	}
	// The interface asks once while it is built, before anything is drawn.
	// That call must not wait on DNS; the refresh that follows it does the
	// lookup and replaces this answer.
	if !u.hostsAsked.Swap(true) {
		return "checking"
	}
	// The same names doctor checks, filtered the same way, so the interface
	// and `nearhost doctor` never disagree about what the file should hold.
	c, _ := checkHosts(context.Background(), doctorBackend{env: doctorEnv{readHosts: u.b.readHosts, lookup: u.b.lookup}}, u.running())
	return c.detail
}

func (u *localOps) Open(url string) error { return u.b.open(url) }
