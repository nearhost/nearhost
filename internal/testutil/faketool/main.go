// Command faketool stands in for a small command-line tool in tests, such as
// a clipboard program. It does what the file beside its own executable says:
// the executable's path without any ".exe", plus ".does". That file holds one
// instruction. "exit N" exits with code N, and "copy PATH" copies standard
// input to the file PATH and exits 0.
package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// main reads the instruction and carries it out.
func main() {
	exe, err := os.Executable()
	if err != nil {
		fail(err)
	}
	b, err := os.ReadFile(strings.TrimSuffix(exe, ".exe") + ".does")
	if err != nil {
		fail(err)
	}
	verb, arg, _ := strings.Cut(strings.TrimSpace(string(b)), " ")
	switch verb {
	case "exit":
		code, err := strconv.Atoi(arg)
		if err != nil {
			fail(err)
		}
		os.Exit(code)
	case "copy":
		f, err := os.Create(arg)
		if err != nil {
			fail(err)
		}
		if _, err := io.Copy(f, os.Stdin); err != nil {
			fail(err)
		}
		if err := f.Close(); err != nil {
			fail(err)
		}
	default:
		fail(fmt.Errorf("unknown instruction %q", verb))
	}
}

// fail reports err on standard error and exits with code 2.
func fail(err error) {
	fmt.Fprintln(os.Stderr, "faketool:", err)
	os.Exit(2)
}
