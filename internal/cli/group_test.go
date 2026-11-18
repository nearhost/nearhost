package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// runTree runs the real command tree with args and returns what the command
// wrote, what Main would print to stderr, and the exit code.
func runTree(t *testing.T, args ...string) (out, errText string, code int) {
	t.Helper()
	root := NewRoot()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)
	code = exitCode(root.Execute(), &stderr)
	return stdout.String(), stderr.String(), code
}

func TestGroupCommandRejectsUnknownSubcommand(t *testing.T) {
	cases := []struct {
		args    []string
		path    string
		suggest string
	}{
		{[]string{"hosts", "clear"}, "nearhost hosts", "clean"},
		{[]string{"preset", "lst"}, "nearhost preset", "list"},
		{[]string{"preset", "registry", "ad"}, "nearhost preset registry", "add"},
		{[]string{"preset", "bogus"}, "nearhost preset", ""},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			_, errText, code := runTree(t, c.args...)
			if code != 1 {
				t.Fatalf("exit code = %d, want 1; stderr %q", code, errText)
			}
			want := "nearhost: unknown command " + `"` + c.args[len(c.args)-1] + `" for "` + c.path + `"`
			if !strings.HasPrefix(errText, want) {
				t.Fatalf("stderr = %q, want prefix %q", errText, want)
			}
			if c.suggest != "" {
				if want := "\n\nDid you mean this?\n\t" + c.suggest + "\n"; !strings.Contains(errText, want) {
					t.Fatalf("stderr = %q, want suggestion %q", errText, want)
				}
			} else if strings.Contains(errText, "Did you mean") {
				t.Fatalf("stderr = %q, want no suggestion", errText)
			}
			if strings.Count(errText, "unknown command") != 1 {
				t.Fatalf("error printed more than once: %q", errText)
			}
		})
	}
}

func TestGroupCommandWithoutArgsPrintsHelp(t *testing.T) {
	for _, args := range [][]string{{"hosts"}, {"preset"}, {"preset", "registry"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, errText, code := runTree(t, args...)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr %q", code, errText)
			}
			if !strings.Contains(out, "Available Commands:") {
				t.Fatalf("stdout = %q, want the group's help", out)
			}
		})
	}
}

func TestGroupCommandHelpFlagWithBadArg(t *testing.T) {
	for _, args := range [][]string{{"hosts", "--help", "clear"}, {"hosts", "-h"}, {"preset", "registry", "-h", "x"}} {
		_, errText, code := runTree(t, args...)
		if code != 0 {
			t.Fatalf("%v: exit code = %d, want 0; stderr %q", args, code, errText)
		}
	}
}

func TestGroupCommandSubcommandsStillResolve(t *testing.T) {
	// A valid subcommand and an alias reach their own handlers, not the group's.
	root := NewRoot()
	hostsCmd, _, err := root.Find([]string{"hosts", "clean"})
	if err != nil || hostsCmd.Name() != "clean" {
		t.Fatalf("hosts clean resolved to %v, %v", hostsCmd, err)
	}
	reg, _, err := root.Find([]string{"preset", "registry", "list"})
	if err != nil || reg.Name() != "list" {
		t.Fatalf("preset registry list resolved to %v, %v", reg, err)
	}
}

func TestGroupCommandHonoursAliasesAndHidden(t *testing.T) {
	group := &cobra.Command{Use: "g", RunE: groupRun}
	group.AddCommand(
		&cobra.Command{Use: "real", Aliases: []string{"alias"}, RunE: func(*cobra.Command, []string) error { return nil }},
		&cobra.Command{Use: "secret", Hidden: true, RunE: func(*cobra.Command, []string) error { return nil }},
	)
	root := &cobra.Command{Use: "nearhost", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(group)
	for _, name := range []string{"alias", "secret", "real"} {
		root.SetArgs([]string{"g", name})
		if err := root.Execute(); err != nil {
			t.Fatalf("g %s: %v", name, err)
		}
	}
}

// TestEveryGroupCommandHandlesUnknownSubcommands walks the whole tree, so a
// group added later cannot go back to printing help and exiting 0 for a typo.
func TestEveryGroupCommandHandlesUnknownSubcommands(t *testing.T) {
	root := NewRoot()
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	var groups [][]string
	var walk func(c *cobra.Command, path []string)
	walk = func(c *cobra.Command, path []string) {
		if len(c.Commands()) > 0 && c.HasParent() && c.Name() != "help" {
			if c.RunE == nil && c.Run == nil {
				t.Errorf("%q has subcommands and no run function; give it RunE: groupRun", c.CommandPath())
			} else if c.Run == nil && c.Args == nil {
				groups = append(groups, path)
			}
		}
		for _, sub := range c.Commands() {
			walk(sub, append(append([]string{}, path...), sub.Name()))
		}
	}
	walk(root, nil)
	if len(groups) == 0 {
		t.Fatal("found no group commands")
	}
	for _, path := range groups {
		_, errText, code := runTree(t, append(append([]string{}, path...), "zzzzqq")...)
		if want := `unknown command "zzzzqq" for "nearhost ` + strings.Join(path, " ") + `"`; code != 1 || !strings.Contains(errText, want) {
			t.Errorf("%v zzzzqq: code %d, stderr %q, want %q", path, code, errText, want)
		}
	}
}
