// Package firewall keeps the Windows Firewall rule that lets other devices
// on the network reach a networked core. Binding a non-loopback address
// without one makes Windows ask, with no context, whether to let Nearhost
// through, and a core in the background has no one to answer; so the core
// stays on loopback until `nearhost network firewall`, run once from a
// terminal, adds the rule as administrator. Elsewhere there is no rule to
// add, and Ready is always true.
package firewall

import (
	"errors"
	"fmt"
	"strings"
)

// RuleName is the name of Nearhost's rule.
const RuleName = "Nearhost"

// Runner runs netsh with args and returns what it printed. An error means
// netsh failed, or exited other than 0.
type Runner func(args ...string) ([]byte, error)

// ShowCommand is netsh's arguments to show Nearhost's rule, with its
// program.
func ShowCommand() []string {
	return []string{"advfirewall", "firewall", "show", "rule", `name="` + RuleName + `"`, "verbose"}
}

// DeleteCommand is netsh's arguments to delete every rule named RuleName.
func DeleteCommand() []string {
	return []string{"advfirewall", "firewall", "delete", "rule", `name="` + RuleName + `"`}
}

// AddCommand is netsh's arguments to add the rule that lets program in on
// private and domain networks, never on a public one. They are passed to
// netsh as written, quotes and all.
func AddCommand(program string) []string {
	return []string{
		"advfirewall", "firewall", "add", "rule", `name="` + RuleName + `"`, "dir=in", "action=allow",
		`program="` + program + `"`, "enable=yes", "profile=private,domain",
	}
}

// normPath is a Windows path for comparing: forward slashes made back
// slashes, "." elements dropped, and lower case, as Windows paths are not
// case-sensitive.
func normPath(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), "/", `\`)
	parts := strings.Split(p, `\`)
	kept := parts[:0]
	for i, s := range parts {
		if s == "." || (s == "" && i > 0 && i < len(parts)-1) {
			continue
		}
		kept = append(kept, s)
	}
	return strings.ToLower(strings.Join(kept, `\`))
}

// SameProgram reports whether a and b are the same program path, compared
// as Windows compares them.
func SameProgram(a, b string) bool {
	return a != "" && b != "" && normPath(a) == normPath(b)
}

// HasProgram reports whether out, what netsh printed for ShowCommand, has a
// rule for program that lets it in. Its labels are in the system's
// language, so it looks for a line that ends in the program's path, after a
// space or a colon, whatever the label before it. Where the labels are
// English, as on most systems, the rule must also be enabled, inbound, allow
// and on a private or domain network: one turned off or changed in Windows
// Defender Firewall lets nothing in. In another language those are not
// checked.
func HasProgram(out []byte, program string) bool {
	want := normPath(program)
	if want == "" {
		return false
	}
	for _, block := range ruleBlocks(string(out)) {
		if blockHasProgram(block, want) && !blockRefuses(block) {
			return true
		}
	}
	return false
}

// ruleBlocks is out split into one block of lines per rule: netsh prints a
// blank line between rules.
func ruleBlocks(out string) [][]string {
	var blocks [][]string
	var cur []string
	for line := range strings.Lines(out) {
		line = strings.TrimRight(line, "\r\n")
		if strings.TrimSpace(line) == "" {
			if len(cur) > 0 {
				blocks = append(blocks, cur)
				cur = nil
			}
			continue
		}
		cur = append(cur, line)
	}
	if len(cur) > 0 {
		blocks = append(blocks, cur)
	}
	return blocks
}

// blockHasProgram reports whether a rule's lines name want, a normalised
// path.
func blockHasProgram(block []string, want string) bool {
	for _, line := range block {
		got := normPath(line)
		if !strings.HasSuffix(got, want) {
			continue
		}
		rest := got[:len(got)-len(want)]
		if rest == "" || strings.HasSuffix(rest, " ") || strings.HasSuffix(rest, "\t") || strings.HasSuffix(rest, ":") {
			return true
		}
	}
	return false
}

// blockRefuses reports whether a rule's English labels say it lets nothing
// in: not enabled, not inbound, not allow, or only on public networks. A
// label it doesn't find, as in another language, refuses nothing.
func blockRefuses(block []string) bool {
	for _, line := range block {
		label, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.ToLower(strings.TrimSpace(value))
		switch strings.TrimSpace(label) {
		case "Enabled":
			if value != "yes" {
				return true
			}
		case "Direction":
			if value != "in" {
				return true
			}
		case "Action":
			if value != "allow" {
				return true
			}
		case "Profiles":
			if !strings.Contains(value, "private") && !strings.Contains(value, "domain") && value != "any" {
				return true
			}
		}
	}
	return false
}

// ReadyWith reports whether netsh, through run, shows Nearhost's rule for
// program.
func ReadyWith(run Runner, program string) bool {
	out, err := run(ShowCommand()...)
	return err == nil && HasProgram(out, program)
}

// ApplyWith makes Nearhost's rule, through run, the one for program: it does
// nothing when the rule is there already, and otherwise deletes every rule
// of that name before it adds the rule, then checks that netsh shows it.
// netsh exits 1 both when there is no rule and when show itself fails, in
// the system's language, so the delete runs either way: no second rule is
// added beside one show missed. Its failure counts only when show listed a
// rule, as with none there is nothing to delete.
func ApplyWith(run Runner, program string) error {
	if program == "" || strings.ContainsAny(program, "\"\r\n") {
		return errors.New("the program's path can't be given to netsh")
	}
	out, err := run(ShowCommand()...)
	listed := err == nil
	if listed && HasProgram(out, program) {
		return nil
	}
	if out, err := run(DeleteCommand()...); err != nil && listed {
		return netshError("delete the old firewall rule", out, err)
	}
	if out, err := run(AddCommand(program)...); err != nil {
		return netshError("add the firewall rule", out, err)
	}
	out, err = run(ShowCommand()...)
	if err != nil {
		return netshError("check the firewall rule netsh added", out, err)
	}
	if !HasProgram(out, program) {
		return fmt.Errorf("netsh added the firewall rule, but doesn't show it letting %s in", program)
	}
	return nil
}

// netshError says that what failed, with netsh's own words when it printed
// any.
func netshError(what string, out []byte, err error) error {
	if text := strings.TrimSpace(string(out)); text != "" {
		return fmt.Errorf("couldn't %s: netsh said %q: %w", what, text, err)
	}
	return fmt.Errorf("couldn't %s: %w", what, err)
}
