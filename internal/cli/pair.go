package cli

import (
	"context"
	"errors"
	"fmt"
	"os/signal"
	"time"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/report"
)

// pairPoll is how often `nearhost pair` asks whether the code was used.
var pairPoll = time.Second

// pairAPI is what `nearhost pair` asks of the core.
type pairAPI interface {
	Pair(ctx context.Context) (control.PairInfo, error)
	PairStatus(ctx context.Context, code string) (control.PairState, error)
}

// dialPair dials the running core.
func dialPair() (pairAPI, error) {
	c, err := dialCore()
	if err != nil {
		return nil, err
	}
	return c, nil
}

// dialCore dials the running core.
func dialCore() (*control.Client, error) {
	runDir, err := config.RunDir()
	if err != nil {
		return nil, err
	}
	return control.Dial(runDir)
}

// coreNotRunning is the error of a command that needs the core, and none
// answers.
func coreNotRunning(err error) *report.Message {
	return report.Errorf(report.CoreNotRunning, "Nearhost is not running.").
		Step("Start it in the background:", "nearhost -d").
		Step("Or open its interface:", "nearhost").Wrap(err)
}

func newPairCmd(dial func() (pairAPI, error)) *cobra.Command {
	var showQR bool
	cmd := &cobra.Command{
		Use:   "pair",
		Short: "Pair a device, such as an iPad, with this Nearhost",
		Long: "Pair a device with the running Nearhost, which serves to other devices on the\n" +
			"network. It prints an address and a code, and a QR code on a terminal (or\n" +
			"with --qr), then waits until a device uses them. The code works once and\n" +
			"expires after 10 minutes, when pair exits 1. Networked mode must be on in\n" +
			"the config.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPair(cmd, dial, showQR)
		},
	}
	cmd.Flags().BoolVar(&showQR, "qr", false, "print a QR code even when the output is not a terminal")
	return cmd
}

func runPair(cmd *cobra.Command, dial func() (pairAPI, error), showQR bool) error {
	ctx, stop := signal.NotifyContext(orBackground(cmd.Context()), stopSignals()...)
	defer stop()
	c, err := dial()
	if errors.Is(err, control.ErrNotRunning) {
		return coreNotRunning(err)
	}
	if err != nil {
		return err
	}
	info, err := c.Pair(ctx)
	if errors.Is(err, control.ErrNotRunning) {
		return coreNotRunning(err)
	}
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Open this on the device:\n  %s\n\nOr go to the page and type the code: %s\n", printable(info.URL), printable(info.Code))
	if showQR || stdoutIsTerminal() {
		fmt.Fprintln(out)
		if err := renderQR(out, info.URL); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "\nWaiting for the device. The code works once and expires in %s.\n", expiresIn(info.Expires))
	tick := time.NewTicker(pairPoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return report.Errorf(report.Interrupted, "Interrupted, so the code was not used.").ExitCode(interruptedCode).Wrap(ctx.Err())
		case <-tick.C:
		}
		st, err := c.PairStatus(ctx, info.Code)
		if err != nil {
			if ctx.Err() != nil {
				continue
			}
			return err
		}
		switch {
		case st.Paired:
			_, err := fmt.Fprintf(out, "Paired %q.\n", printable(st.Device))
			return err
		case st.Expired || !time.Now().Before(info.Expires):
			// The core forgets a code a while after it expires, and says
			// nothing of it then: the code's own deadline ends the wait too.
			return report.Errorf(report.PairCodeInvalid, "The pairing code expired before a device used it.")
		}
	}
}

// expiresIn says how long until t, rounded to the minute, at least one.
func expiresIn(t time.Time) string {
	m := int(time.Until(t).Round(time.Minute) / time.Minute)
	if m <= 1 {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", m)
}

func orBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
