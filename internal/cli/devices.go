package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/devices"
	"github.com/nearhost/nearhost/internal/report"
)

// devicesAPI is what `nearhost devices` asks of the core.
type devicesAPI interface {
	Devices(ctx context.Context) ([]control.DeviceInfo, error)
	RevokeDevice(ctx context.Context, name string) error
}

// dialDevices dials the running core.
func dialDevices() (devicesAPI, error) {
	c, err := dialCore()
	if err != nil {
		return nil, err
	}
	return c, nil
}

func newDevicesCmd(dial func() (devicesAPI, error)) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "devices",
		Short: "List the devices paired with this Nearhost",
		Long: "List the devices paired with the running Nearhost: each one's name, when it\n" +
			"paired, when it was last seen and where from. Pair one with `nearhost pair`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dial()
			if err != nil {
				return notRunningOr(err)
			}
			list, err := c.Devices(orBackground(cmd.Context()))
			if err != nil {
				return notRunningOr(err)
			}
			out := cmd.OutOrStdout()
			if len(list) == 0 {
				_, err := fmt.Fprintln(out, "No devices are paired. Pair one with `nearhost pair`.")
				return err
			}
			rows := [][]string{{"NAME", "PAIRED", "LAST SEEN", "FROM"}}
			for _, d := range list {
				rows = append(rows, []string{printable(d.Name), deviceAgo(d.Paired), deviceAgo(d.LastSeen), printable(d.LastAddr)})
			}
			return writeTable(out, rows)
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "revoke <name>",
		Short: "Unpair a device",
		Long: "Unpair the device with this name. Its next request is refused and sent back to\n" +
			"the page to pair again. With no Nearhost running, it edits the list of devices\n" +
			"directly.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRevoke(cmd, dial, args[0])
		},
	})
	return cmd
}

// notRunningOr is err, or the not-running message when that is what it is.
func notRunningOr(err error) error {
	if errors.Is(err, control.ErrNotRunning) {
		return coreNotRunning(err)
	}
	return err
}

func runRevoke(cmd *cobra.Command, dial func() (devicesAPI, error), name string) error {
	c, err := dial()
	if err == nil {
		err = c.RevokeDevice(orBackground(cmd.Context()), name)
	}
	switch {
	case errors.Is(err, control.ErrNotRunning):
		err = revokeOffline(name)
	}
	if err != nil {
		if m, ok := report.As(err); ok && m.Code == report.DeviceUnknown {
			return report.Errorf(report.DeviceUnknown, "No paired device is called %q.", printable(name)).
				Step("List the paired devices:", "nearhost devices").Wrap(err)
		}
		if errors.Is(err, control.ErrDeviceUnknown) || errors.Is(err, devices.ErrNoDevice) {
			return report.Errorf(report.DeviceUnknown, "No paired device is called %q.", printable(name)).
				Step("List the paired devices:", "nearhost devices").Wrap(err)
		}
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Revoked %q.\n", printable(name))
	return err
}

// revokeOffline edits devices.json when no core runs to do it.
func revokeOffline(name string) error {
	store, err := offlineDevices()
	if err != nil {
		return err
	}
	return store.Revoke(name)
}

// offlineDevices is devices.json opened to list or revoke devices, not to
// sign: that needs no cookie key, so the store gets a fixed one that signs
// nothing.
func offlineDevices() (*devices.Store, error) {
	path, err := config.DevicesPath()
	if err != nil {
		return nil, err
	}
	return devices.Open(path, []byte(strings.Repeat("-", 32))), nil
}

// deviceAgo says when t was, to the day for an old time.
func deviceAgo(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h ago", int(d/time.Hour))
	}
	return t.Local().Format("2006-01-02")
}
