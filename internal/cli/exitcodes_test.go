package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/elevate"
	"github.com/nearhost/nearhost/internal/report"
)

// rendered is err as Main prints it on standard error, without colour.
func rendered(err error) string {
	var b strings.Builder
	exitCode(err, &b)
	return b.String()
}

// The exit codes scripts and the elevator read stay what they were, now
// that each comes from the error's message.
func TestExitCodesUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    func(t *testing.T) error
		code   int
		prefix string
	}{
		{"interrupted add", func(t *testing.T) error {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			cmd := newAddCmd(addBackend{})
			cmd.SetContext(ctx)
			return runAddInterruptible(cmd, addBackend{}, addFlags{}, []string{"/no/such/project"})
		}, interruptedCode, "Error: Interrupted, so the project was not added.\n"},
		{"interrupted start", func(*testing.T) error {
			return interruptedStart{err: context.Canceled, fate: fateNone}
		}, interruptedCode, "Error: Interrupted before Nearhost was started in the background, so it\nis not running.\n"},
		{"status with no core", func(t *testing.T) error {
			configHome(t)
			_, err := execute(t, newStatusCmd(nil), "status")
			return err
		}, statusNotRunningCode, "Error: Nearhost is not running.\n"},
		{"hosts apply refusal", func(t *testing.T) error {
			runElevated(t)
			_, err := runApply(t, "--input", filepath.Join(t.TempDir(), "input"))
			return err
		}, elevate.HelperRefusedCode, "Error: "},
		{"update --check failure", func(t *testing.T) error {
			_, err := runUpdate(t, &fakeUpdate{err: errors.New("offline")}, "0.5.0", "", "--check")
			return err
		}, updateCheckFailedCode, "Error: offline\n"},
		{"a plain error", func(*testing.T) error { return errors.New("boom") }, 1, "Error: boom\n"},
		{"already said", func(*testing.T) error { return alreadySaid{errors.New("boom")} }, 1, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.err(t)
			var stderr strings.Builder
			if code := exitCode(err, &stderr); code != tc.code {
				t.Errorf("exit code %d, want %d (err %v)", code, tc.code, err)
			}
			if got := stderr.String(); !strings.HasPrefix(got, tc.prefix) || tc.prefix == "" && got != "" {
				t.Errorf("stderr %q, want it to start %q", got, tc.prefix)
			}
			noNearhostPrefix(t, stderr.String())
		})
	}
	if rendered(errors.New("boom")) != "Error: boom\n" {
		t.Errorf("a plain error renders %q", rendered(errors.New("boom")))
	}
}

// Without a core, status says so on standard error with how to start one,
// as JSON or not, and the code is shown with --verbose.
func TestStatusNotRunningIsAMessage(t *testing.T) {
	for _, args := range [][]string{{"status"}, {"status", "--json"}, {"status", "--verbose"}} {
		configHome(t)
		out, err := execute(t, newStatusCmd(nil), args...)
		if out != "" {
			t.Errorf("%v: printed %q", args, out)
		}
		m, ok := report.As(err)
		if !ok || m.Code != report.CoreNotRunning || m.Exit != statusNotRunningCode {
			t.Fatalf("%v: err %v, message %+v", args, err, m)
		}
		var stderr strings.Builder
		exitCodeVerbose(err, &stderr, len(args) == 2 && args[1] == "--verbose")
		want := "Error: Nearhost is not running.\n\nStart it in the background:\n\n  nearhost -d\n\nOr open its interface:\n\n  nearhost\n"
		if args[len(args)-1] == "--verbose" {
			want = "Error: Nearhost is not running.\n(core.not-running)\n\nStart it in the background:\n\n  nearhost -d\n\nOr open its interface:\n\n  nearhost\n"
		}
		if !strings.HasPrefix(stderr.String(), want) {
			t.Errorf("%v: stderr %q, want %q", args, stderr.String(), want)
		}
	}
}

// Main renders a command's error, with its code under --verbose.
func TestMainRendersErrorsWithVerbose(t *testing.T) {
	noCleanOld(t)
	configHome(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "stderr")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = f
	code := Main([]string{"status", "--verbose"})
	os.Stderr = stderr
	f.Close()
	got, _ := os.ReadFile(path)
	if code != statusNotRunningCode || !strings.HasPrefix(string(got), "Error: Nearhost is not running.\n(core.not-running)\n") {
		t.Errorf("exit %d, stderr %q", code, got)
	}
}

// flat is s with each run of white space made one space, so text that a
// renderer wrapped compares with the words it holds.
func flat(s string) string { return strings.Join(strings.Fields(s), " ") }
