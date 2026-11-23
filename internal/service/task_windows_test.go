//go:build windows

package service

import (
	"reflect"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

// TestEscapeArgAgrees checks our escaping and splitting against the system's:
// both escapers must give lines that both splitters read back as the arguments. The escaped text itself may differ, as syscall.EscapeArg leaves a quote without a space unwrapped.
func TestEscapeArgAgrees(t *testing.T) {
	cases := [][]string{
		{`C:\Users\Ed Smith\AppData\Local\nearhost\nearhost.exe`, "daemon", "--started-by", "service"},
		{`C:\Users\Ed "Q" Smith\near&host<1>.exe`, "--web-port", "0", "", `a b\`, `c\\"d`, `\\server\share dir\`},
	}
	for _, argv := range cases {
		var ours, sys []string
		for _, a := range argv {
			ours = append(ours, escapeArg(a))
			sys = append(sys, syscall.EscapeArg(a))
		}
		for _, line := range []string{strings.Join(ours, " "), strings.Join(sys, " ")} {
			if got := splitCommandLine(line); !reflect.DeepEqual(got, argv) {
				t.Errorf("splitCommandLine(%q) = %q, want %q", line, got, argv)
			}
			// The system splitter reads the first word as a program name,
			// with no backslash escapes, so put one ahead of the arguments.
			got, err := windows.DecomposeCommandLine("prog " + line)
			if err == nil && len(got) > 0 {
				got = got[1:]
			}
			if err != nil || !reflect.DeepEqual(got, argv) {
				t.Errorf("DecomposeCommandLine(%q) = %q, %v, want %q", line, got, err, argv)
			}
		}
	}
}
