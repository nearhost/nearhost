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

// isElevated reports whether this process runs as root or administrator.
// Tests replace it, as CI's Windows runner is elevated.
var isElevated = elevate.Elevated

// checkElevatedFile refuses a file other than the system hosts file when
// this process is elevated. The hosts helper runs elevated through sudo or
// the UAC prompt, and whoever starts it chooses --file, so it would
// otherwise write Nearhost's block into any file as root or administrator.
func checkElevatedFile(file string) error {
	if isElevated() && !hosts.IsSystemPath(file) {
		return fmt.Errorf("an elevated run only writes %s", hosts.SystemPath())
	}
	return nil
}

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
			// An elevated run checks where --input and --report are before
			// it opens or creates either, so a refusal leaves no report.
			elevated := isElevated()
			if elevated {
				if err := checkElevatedIO(&dirs, input, report); err != nil {
					return err
				}
			}
			err := checkElevatedFile(file)
			if err == nil {
				err = applyAliases(cmd.InOrStdin(), file, input, &dirs)
			}
			if err != nil && report != "" {
				if rerr := writeReport(&dirs, report, reportText(err, elevated)); rerr != nil {
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
			if err := checkElevatedFile(file); err != nil {
				return err
			}
			apply, err := cleanApplier(file)
			if err != nil {
				return err
			}
			changed, err := hosts.Ensure(file, nil, apply)
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

// cleanApplier is how `hosts clean` writes file: through the elevated
// helper for the system hosts file, and in this process for any other file,
// which the user names with --file and so must be able to write. The
// elevated helper writes only the system hosts file.
func cleanApplier(file string) (hosts.Applier, error) {
	if !hosts.IsSystemPath(file) {
		return func(aliases []string) error { return hosts.Apply(file, aliases) }, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return newElevatedApplier(exe, file), nil
}

// maxInput is the most `hosts apply --input` reads: a hosts block never
// holds anything near it.
const maxInput = 1 << 20

// applyAliases reads aliases, one per line, from the file input, or from
// stdin when input is empty, and sets the managed block of file to them.
// openInput opens input, holding its directory in dirs where the system can.
// An error reading the aliases is an *inputError.
func applyAliases(stdin io.Reader, file, input string, dirs *heldDirs) error {
	aliases, err := readAliases(stdin, input, dirs)
	if err != nil {
		return &inputError{err}
	}
	return hosts.Apply(file, aliases)
}

// readAliases reads the non-blank lines of the file input, or of stdin when
// input is empty, each trimmed.
func readAliases(stdin io.Reader, input string, dirs *heldDirs) ([]string, error) {
	r := stdin
	if input != "" {
		f, err := openInput(dirs, input)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		// One byte more than the limit tells a file at the limit from one
		// over it.
		data, err := io.ReadAll(io.LimitReader(f, maxInput+1))
		if err != nil {
			return nil, err
		}
		if len(data) > maxInput {
			return nil, fmt.Errorf("%s is over %d bytes", input, maxInput)
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
	return aliases, sc.Err()
}

// inputError is an error reading the host list, which names the caller's
// input file or quotes its content.
type inputError struct{ err error }

// Error is the wrapped error's text.
func (e *inputError) Error() string { return e.err.Error() }

// Unwrap is the wrapped error.
func (e *inputError) Unwrap() error { return e.err }

// reportText is what the --report file says about err. Elevated, it quotes
// no text the caller chose, as whoever starts the helper chooses the input,
// and the unelevated caller has already checked the host list and says what
// is wrong with it itself: a host list that cannot be read or is not valid
// gets a fixed sentence. Every other error names only the system hosts file,
// which checkElevatedFile pins, and the system's own message.
func reportText(err error, elevated bool) string {
	if !elevated {
		return err.Error()
	}
	var ie *inputError
	switch {
	case errors.As(err, &ie):
		return "the host list could not be read"
	case errors.Is(err, hosts.ErrInvalidHost):
		return "the host list is not valid"
	}
	return err.Error()
}

// writeReport writes text and a newline to a new file at path, which
// createReport makes without following or replacing anything already there.
// When elevated, checkElevatedIO has already refused a path outside a folder
// Nearhost made for the helper; createReport's comment says what each system
// guarantees about where the file lands.
func writeReport(dirs *heldDirs, path, text string) error {
	f, ferr := createReport(dirs, path)
	if ferr != nil {
		return ferr
	}
	_, werr := fmt.Fprintln(f, text)
	return errors.Join(werr, f.Close())
}
