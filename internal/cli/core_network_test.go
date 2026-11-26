package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/devices"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/testutil"
)

// A networked core that does not listen on the network yet refuses to give
// a pairing code, saying why, rather than a code no device could use.
func TestPairRefusesWhileNotListening(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", testutil.TempDir(t, "nh"))
	cfg := &config.Config{Network: &config.Network{Domain: "lab.example.dev", Certificates: "ca"}}
	eng := engine.New(cfg, engine.Options{WebPort: 6327, Firewall: func() bool { return false }})
	if ns := eng.NetworkState(); ns.ProblemKind != engine.NetworkNoFirewall {
		t.Fatalf("NetworkState = %+v, want waiting for the firewall rule", ns)
	}
	store, err := openDevices()
	if err != nil {
		t.Fatal(err)
	}
	o := &coreOps{eng: eng, devices: store}
	info, err := o.Pair()
	m, ok := report.As(err)
	if !ok || m.Code != report.NetworkNotListening || !strings.Contains(m.Error(), "waiting for the firewall rule") {
		t.Fatalf("Pair = %+v, %v, want %s naming the problem", info, err, report.NetworkNotListening)
	}
	if info.Code != "" {
		t.Errorf("a code was given: %+v", info)
	}
}

// Pairing goes on while the core listens on the network, whatever else is
// wrong, and is refused, naming why, while it does not.
func TestPairRefusal(t *testing.T) {
	for _, c := range []struct {
		name string
		ns   engine.NetworkState
		code report.Code
		says string
	}{
		{"listening", engine.NetworkState{On: true, Listening: true}, "", ""},
		{"no DNS token", engine.NetworkState{On: true, Listening: true, Problem: "no DNS token", ProblemKind: engine.NetworkNoToken}, "", ""},
		{"a failed change, still listening", engine.NetworkState{On: true, Listening: true, Problem: "networked mode couldn't change: boom", ProblemKind: engine.NetworkApplyFailed}, "", ""},
		{"off", engine.NetworkState{}, report.NetworkOff, ""},
		{"a failed start", engine.NetworkState{Problem: "networked mode couldn't start: boom", ProblemKind: engine.NetworkApplyFailed}, report.NetworkNotListening, "couldn't start: boom"},
		{"waiting for the firewall", engine.NetworkState{On: true, Problem: "waiting for the firewall rule", ProblemKind: engine.NetworkNoFirewall}, report.NetworkNotListening, "waiting for the firewall rule"},
		{"not listening, no problem", engine.NetworkState{On: true}, report.NetworkNotListening, "doesn't listen"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := pairRefusal(c.ns)
			if c.code == "" {
				if err != nil {
					t.Fatalf("pairRefusal = %v, want none", err)
				}
				return
			}
			if c.code == report.NetworkOff {
				if !errors.Is(err, control.ErrNetworkOff) {
					t.Fatalf("pairRefusal = %v, want ErrNetworkOff", err)
				}
				return
			}
			m, ok := report.As(err)
			if !ok || m.Code != c.code || !strings.Contains(m.Error(), c.says) {
				t.Fatalf("pairRefusal = %v, want %s saying %q", err, c.code, c.says)
			}
		})
	}
}

// A networked core whose devices store couldn't be opened, as when it
// started un-networked, says so when asked to pair, rather than that the
// page isn't served.
func TestPairSaysWhyDevicesAreUnavailable(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", testutil.TempDir(t, "nh"))
	cfg := &config.Config{Network: &config.Network{Domain: "lab.example.dev", Certificates: "ca"}}
	eng := engine.New(cfg, engine.Options{WebPort: 6327})
	o := &coreOps{eng: eng, devicesErr: errors.New("the data directory is a file")}
	_, err := o.Pair()
	m, ok := report.As(err)
	if !ok || m.Code != report.PairUnavailable || !strings.Contains(m.Error(), "the data directory is a file") {
		t.Fatalf("Pair = %v, want %s naming the store's error", err, report.PairUnavailable)
	}
}

// A core with no store open lists and revokes the devices in devices.json,
// as the CLI does with no core running.
func TestDevicesWithoutStoreReadTheFile(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", testutil.TempDir(t, "nh"))
	path, err := config.DevicesPath()
	if err != nil {
		t.Fatal(err)
	}
	store := devices.Open(path, []byte(strings.Repeat("k", 32)))
	if _, err := store.Pair(store.NewPairCode().Code, "iPad", "192.168.77.9"); err != nil {
		t.Fatal(err)
	}
	o := &coreOps{}
	list, err := o.Devices()
	if err != nil || len(list) != 1 || list[0].Name != "iPad" {
		t.Fatalf("Devices = %+v, %v, want the iPad", list, err)
	}
	if err := o.RevokeDevice("iPad"); err != nil {
		t.Fatalf("RevokeDevice: %v", err)
	}
	if list, err := o.Devices(); err != nil || len(list) != 0 {
		t.Errorf("after revoke: %+v, %v, want none", list, err)
	}
	if err := o.RevokeDevice("iPad"); !errors.Is(err, control.ErrDeviceUnknown) {
		t.Errorf("revoking again: %v, want ErrDeviceUnknown", err)
	}
}
