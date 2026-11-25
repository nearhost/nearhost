package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/elevate"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/firewall"
	"github.com/nearhost/nearhost/internal/report"
)

// firewallGOOS is the OS `network firewall` acts for; tests replace it.
var firewallGOOS = runtime.GOOS

// firewallExecutable is the running program, the only one the firewall
// rule lets in; tests replace it.
var firewallExecutable = os.Executable

// netshRun runs netsh; tests replace it.
var netshRun firewall.Runner = firewall.Netsh

// firewallRecheck is how often a networked core that waits for the firewall
// rule looks for it.
const firewallRecheck = 30 * time.Second

// osName is the name people know goos by.
func osName(goos string) string {
	switch goos {
	case "darwin":
		return "macOS"
	case "linux":
		return "Linux"
	case "windows":
		return "Windows"
	}
	return goos
}

func newNetworkFirewallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "firewall",
		Short: "Let other devices reach Nearhost through Windows Firewall",
		Long: "Add a Windows Firewall rule that lets other devices on private networks reach\n" +
			"this Nearhost, so networked mode can listen on the network. Windows asks once\n" +
			"for an administrator's consent. A running Nearhost finds the rule within 30\n" +
			"seconds. Only Windows needs this.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runNetworkFirewall(cmd)
		},
	}
	var program, input, reportPath string
	apply := &cobra.Command{
		Use:    "apply",
		Hidden: true,
		Args:   cobra.NoArgs,
		Short:  "Add the firewall rule for --program, which must be this program",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFirewallApply(program, input, reportPath)
		},
	}
	apply.Flags().StringVar(&program, "program", "", "the program the rule lets in: this one")
	apply.Flags().StringVar(&input, "input", "", "unused; the Windows elevator passes it")
	apply.Flags().StringVar(&reportPath, "report", "", "when apply fails, write the error's text to this file")
	cmd.AddCommand(apply)
	return cmd
}

// runNetworkFirewall is `nearhost network firewall`: on Windows it adds the
// rule through the UAC prompt, unless it is there already.
func runNetworkFirewall(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()
	if firewallGOOS != "windows" {
		_, err := fmt.Fprintf(out, "Nothing to do on %s.\n", osName(firewallGOOS))
		return err
	}
	exe, err := firewallExecutable()
	if err != nil {
		return err
	}
	if firewall.ReadyWith(netshRun, exe) {
		_, err := fmt.Fprintln(out, "The firewall rule is already in place.")
		return err
	}
	e := elevate.System()
	if e.Needed() {
		err = e.Run(orBackground(cmd.Context()), elevate.Command{
			Path: exe, Args: []string{"network", "firewall", "apply", "--program", exe}, Interactive: true,
		})
	} else {
		err = firewall.ApplyWith(netshRun, exe)
	}
	if errors.Is(err, elevate.ErrDeclined) {
		return report.Errorf(report.NetworkFirewall, "The Windows prompt was declined, so the firewall rule wasn't added.").
			Step("Run it again and choose Yes:", "nearhost network firewall").Wrap(err)
	}
	if err != nil {
		return report.Errorf(report.NetworkFirewall, "The firewall rule couldn't be added: %s", err).Wrap(err)
	}
	_, err = fmt.Fprintln(out, "Added the firewall rule. Other devices on private networks can reach Nearhost,\n"+
		"and a running Nearhost starts listening on the network within 30 seconds.")
	return err
}

// runFirewallApply is the hidden `network firewall apply`, which runs as
// administrator. Whoever starts it chooses --program, so it adds a rule
// only for the program that is running, and refuses any other with
// elevate.HelperRefusedCode before netsh runs.
func runFirewallApply(program, input, reportPath string) error {
	var dirs heldDirs
	defer dirs.Close()
	elevated := isElevated()
	if elevated {
		if err := checkElevatedIO(&dirs, input, reportPath); err != nil {
			return report.Errorf(report.NetworkFirewallRefused, "%s", err).ExitCode(elevate.HelperRefusedCode).Wrap(err)
		}
	}
	exe, err := firewallExecutable()
	if err == nil && !firewall.SameProgram(program, exe) {
		m := report.Errorf(report.NetworkFirewallRefused, "The firewall rule is only added for this program, %s.", exe).ExitCode(elevate.HelperRefusedCode)
		err = m
	} else if err == nil {
		err = firewall.ApplyWith(netshRun, exe)
	}
	if err != nil && reportPath != "" {
		if rerr := writeReport(&dirs, reportPath, err.Error()); rerr != nil {
			return errors.Join(err, rerr)
		}
	}
	return err
}

// networkRechecker is the engine, as the firewall watch sees it.
type networkRechecker interface {
	NetworkState() engine.NetworkState
	RecheckNetwork(ctx context.Context) error
}

// firewallWatch is what a core on Windows knows of the firewall rule. Ready
// is the engine's Firewall option: the answer it holds, never netsh, which
// must not run under the engine's locks. run looks for the rule while the
// core is networked and the rule is missing.
type firewallWatch struct {
	ready atomic.Bool
	// check runs netsh: firewall.Ready.
	check func() bool
	// publish says that the rule has come; nil says nothing.
	publish func(engine.Event)
}

// newFirewallWatch is the core's watch, or nil where no rule is needed. A
// core that starts networked has it look once, at once; one that is not
// runs no netsh until networked mode is turned on.
func newFirewallWatch(networked bool) *firewallWatch {
	if !firewall.Needed {
		return nil
	}
	w := &firewallWatch{check: firewall.Ready}
	if networked {
		w.ready.Store(w.check())
	}
	return w
}

// Ready reports whether the rule was there when last looked for.
func (w *firewallWatch) Ready() bool { return w.ready.Load() }

// run looks for the rule at each tick while eng is networked, until it is
// there or ctx ends. When it comes, eng applies its network again, so it
// listens on the network, and run ends: once the rule is there it is not
// looked for again.
func (w *firewallWatch) run(ctx context.Context, eng networkRechecker, tick <-chan time.Time) {
	for !w.ready.Load() {
		select {
		case <-ctx.Done():
			return
		case <-tick:
		}
		if !eng.NetworkState().On || !w.check() {
			continue
		}
		w.ready.Store(true)
		if eng.RecheckNetwork(ctx) == nil && w.publish != nil {
			w.publish(engine.Event{Message: "The firewall rule is in place, so other devices can reach Nearhost."})
		}
	}
}
