package elevate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sys/windows"

	"github.com/nearhost/nearhost/internal/winsys"
)

// System is this OS's Elevator.
func System() Elevator { return uac{} }

// uac runs privileged steps through the UAC prompt. The elevated child gets
// no standard streams back to Nearhost, so its input and its report travel
// in files that Run names with --input and --report.
type uac struct{}

// Needed reports whether this process is not elevated.
func (uac) Needed() bool { return !Elevated() }

// TakesPassword is false: Windows asks for consent itself.
func (uac) TakesPassword() bool { return false }

// Cached is false: every Run asks for consent.
func (uac) Cached(context.Context) bool { return false }

// Validate is unsupported: there is no password to check.
func (uac) Validate(context.Context, string) error { return errors.ErrUnsupported }

// Run runs c through the UAC prompt and waits for it to finish. c.Input is
// written to a file named by --input, and when the child fails, the last
// line of the file named by --report is the error. When ctx ends Run stops
// waiting and removes both files, without killing the child: one that has
// not read its input yet then fails, so an approval given after a stop
// changes nothing. c.Prompt and c.Interactive are ignored, as the prompt is
// Windows' own.
func (uac) Run(ctx context.Context, c Command) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "nearhost-elevate-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	input := filepath.Join(dir, "input")
	if err := os.WriteFile(input, []byte(c.Input), 0o600); err != nil {
		return err
	}
	report := filepath.Join(dir, "report")
	args := append(slices.Clip(c.Args), "--input", input, "--report", report)
	h, err := winsys.RunAs(c.Path, args)
	if errors.Is(err, windows.ERROR_CANCELLED) {
		return ErrDeclined
	}
	if err != nil {
		return fmt.Errorf("run %s as administrator: %w", c.Path, err)
	}
	defer windows.CloseHandle(h)
	for {
		ev, err := windows.WaitForSingleObject(h, 100)
		if err != nil {
			return fmt.Errorf("wait for %s: %w", c.Path, err)
		}
		if ev == windows.WAIT_OBJECT_0 {
			break
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return fmt.Errorf("exit code of %s: %w", c.Path, err)
	}
	if code == 0 {
		return nil
	}
	if line := lastLine(report); line != "" {
		return errors.New(line)
	}
	return fmt.Errorf("exited with code %d", code)
}

// lastLine is the last non-blank line of the file at path, or "" when there
// is none or the file cannot be read.
func lastLine(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
