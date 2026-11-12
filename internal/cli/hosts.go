package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/elevate"
	"github.com/nearhost/nearhost/internal/hosts"
)

// hostsPrompt is the password prompt of the commands that update the hosts file.
var hostsPrompt = "nearhost needs your password to update " + hosts.DefaultPath + ": "

// newElevatedApplier is a variable so tests can avoid real sudo. Nothing
// interrupts it while sudo waits for a password.
var newElevatedApplier = func(exe, path string) hosts.Applier {
	return hosts.ElevatedApplier(context.Background(), elevate.System(), exe, path, hostsPrompt, true)
}

func newHostsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hosts",
		Short: "Manage the nearhost block in " + hosts.DefaultPath,
		Long:  "Manage the nearhost block in " + hosts.DefaultPath + ".\n\n" + elevationNote,
	}
	var file string
	cmd.PersistentFlags().StringVar(&file, "file", hosts.DefaultPath, "hosts file to edit")

	var input, report string
	apply := &cobra.Command{
		Use:    "apply",
		Hidden: true,
		Args:   cobra.NoArgs,
		Short:  "Set the managed block to the aliases read from stdin or --input (one per line)",
		RunE: func(cmd *cobra.Command, args []string) error {
			// The directories of --input and --report stay held until
			// apply and the report are done.
			var dirs heldDirs
			defer dirs.Close()
			err := applyAliases(cmd.InOrStdin(), file, input, &dirs)
			if err != nil && report != "" {
				if rerr := writeReport(&dirs, report, err); rerr != nil {
					return errors.Join(err, rerr)
				}
			}
			return err
		},
	}
	apply.Flags().StringVar(&input, "input", "", "read the aliases from this file instead of stdin (at most 1 MiB)")
	apply.Flags().StringVar(&report, "report", "", "when apply fails, write the error's text to this file")

	clean := &cobra.Command{
		Use:   "clean",
		Args:  cobra.NoArgs,
		Short: cleanShort,
		RunE: func(cmd *cobra.Command, args []string) error {
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			changed, err := hosts.Ensure(file, nil, newElevatedApplier(exe, file))
			if err != nil {
				return err
			}
			if changed {
				fmt.Fprintln(cmd.OutOrStdout(), "Removed the nearhost block from", file)
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "No nearhost block in", file)
			}
			return nil
		},
	}

	cmd.AddCommand(apply, clean)
	return cmd
}

// maxInput is the most `hosts apply --input` reads: a hosts block never
// holds anything near it.
const maxInput = 1 << 20

// applyAliases reads aliases, one per line, from the file input, or from
// stdin when input is empty, and sets the managed block of file to them.
// openInput opens input, holding its directory in dirs where the system can.
func applyAliases(stdin io.Reader, file, input string, dirs *heldDirs) error {
	r := stdin
	if input != "" {
		f, err := openInput(dirs, input)
		if err != nil {
			return err
		}
		defer f.Close()
		// One byte more than the limit tells a file at the limit from one
		// over it.
		data, err := io.ReadAll(io.LimitReader(f, maxInput+1))
		if err != nil {
			return err
		}
		if len(data) > maxInput {
			return fmt.Errorf("%s is over %d bytes", input, maxInput)
		}
		r = bytes.NewReader(data)
	}
	var aliases []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if a := strings.TrimSpace(sc.Text()); a != "" {
			aliases = append(aliases, a)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return hosts.Apply(file, aliases)
}

// writeReport writes err's text to a new file at path, which createReport
// makes without following or replacing anything already there. The elevated
// process that calls it is handed a path in the user's temp directory, which
// other processes of that user can write to; createReport's comment says
// what each system guarantees about where the file lands. The content is
// only the error's text, which the caller already has.
func writeReport(dirs *heldDirs, path string, err error) error {
	f, ferr := createReport(dirs, path)
	if ferr != nil {
		return ferr
	}
	_, werr := fmt.Fprintln(f, err)
	return errors.Join(werr, f.Close())
}
