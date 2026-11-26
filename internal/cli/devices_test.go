package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/devices"
	"github.com/nearhost/nearhost/internal/report"
)

type fakeDevices struct {
	list    []control.DeviceInfo
	revoked []string
}

func (f *fakeDevices) Devices(context.Context) ([]control.DeviceInfo, error) { return f.list, nil }
func (f *fakeDevices) RevokeDevice(_ context.Context, name string) error {
	f.revoked = append(f.revoked, name)
	return nil
}

func TestDevicesTable(t *testing.T) {
	seen := time.Now().Add(-2 * time.Hour)
	f := &fakeDevices{list: []control.DeviceInfo{{Name: "iPad", Paired: seen.Add(-48 * time.Hour), LastSeen: seen, LastAddr: "192.168.77.9"}}}
	out, err := execute(t, newDevicesCmd(func() (devicesAPI, error) { return f, nil }), "devices")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"NAME", "PAIRED", "LAST SEEN", "FROM", "iPad", "192.168.77.9"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	f.list = nil
	out, err = execute(t, newDevicesCmd(func() (devicesAPI, error) { return f, nil }), "devices")
	if err != nil || !strings.Contains(out, "No devices are paired") {
		t.Errorf("no devices: %q, %v", out, err)
	}
}

func TestDevicesNotRunning(t *testing.T) {
	configHome(t)
	_, err := execute(t, newDevicesCmd(func() (devicesAPI, error) { return nil, control.ErrNotRunning }), "devices")
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("error = %v, want the not-running error", err)
	}
}

func TestDevicesRevokeThroughCore(t *testing.T) {
	f := &fakeDevices{}
	out, err := execute(t, newDevicesCmd(func() (devicesAPI, error) { return f, nil }), "devices", "revoke", "iPad")
	if err != nil || len(f.revoked) != 1 || f.revoked[0] != "iPad" || !strings.Contains(out, `Revoked "iPad".`) {
		t.Fatalf("out %q, err %v, revoked %v", out, err, f.revoked)
	}
}

func TestDevicesRevokeWithoutCore(t *testing.T) {
	configHome(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	path, err := config.DevicesPath()
	if err != nil {
		t.Fatal(err)
	}
	store := devices.Open(path, []byte(strings.Repeat("k", 32)))
	if _, err := store.Pair(store.NewPairCode().Code, "iPad", "192.168.77.9"); err != nil {
		t.Fatal(err)
	}
	dial := func() (devicesAPI, error) { return nil, control.ErrNotRunning }
	if _, err := execute(t, newDevicesCmd(dial), "devices", "revoke", "iPad"); err != nil {
		t.Fatal(err)
	}
	list, err := devices.Open(path, []byte(strings.Repeat("k", 32))).List()
	if err != nil || len(list) != 0 {
		t.Fatalf("after revoke: %v, %v, want none", list, err)
	}
	_, err = execute(t, newDevicesCmd(dial), "devices", "revoke", "iPad")
	if m, ok := report.As(err); !ok || m.Code != report.DeviceUnknown {
		t.Fatalf("revoking a device twice: %v, want code %s", err, report.DeviceUnknown)
	}
}
