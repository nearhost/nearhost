package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/elevate"
	"github.com/nearhost/nearhost/internal/hosts"
)

// hostsPrompt is the password prompt of the commands that update /etc/hosts.
const hostsPrompt = "nearhost needs your password to update /etc/hosts: "

// newElevatedApplier is a variable so tests can avoid real sudo. Nothing
// interrupts it while sudo waits for a password.
var newElevatedApplier = func(exe, path string) hosts.Applier {
	return hosts.ElevatedApplier(context.Background(), elevate.System(), exe, path, hostsPrompt, true)
}

func newHostsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hosts",
		Short: "Manage the nearhost block in /etc/hosts",
		Long: `Manage the nearhost block in /etc/hosts.

` + elevationNote,
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
			err := applyAliases(cmd.InOrStdin(), file, input)
			if err != nil && report != "" {
				if rerr := writeReport(report, err); rerr != nil {
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
func applyAliases(stdin io.Reader, file, input string) error {
	r := stdin
	if input != "" {
		f, err := openInput(input)
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

// refuseLinkedDir returns an error when dir is a symlink or a junction.
func refuseLinkedDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return fmt.Errorf("%s is a link", dir)
	}
	return nil
}

// writeReport writes err's text to a new file at path. The elevated process
// that calls it is handed a path in the user's temp directory, which other
// processes of that user can write to. The file is created with O_EXCL,
// which fails on an existing file or link rather than following it, and the
// directory holding it must not be a symlink or a junction when it is
// checked. That check and the create are two steps, so a directory swapped
// for a junction between them could still redirect the new file; that is
// safe enough because O_EXCL means nothing is overwritten and the content is
// only the error's text, which the caller already has.
func writeReport(path string, err error) error {
	if derr := refuseLinkedDir(filepath.Dir(path)); derr != nil {
		return fmt.Errorf("report directory: %w", derr)
	}
	f, ferr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if ferr != nil {
		return ferr
	}
	_, werr := fmt.Fprintln(f, err)
	return errors.Join(werr, f.Close())
}
