package testutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTestdataKeepsItsLineEndings pins the checkout rules that keep the skill
// script byte-identical on Windows: Git for Windows would otherwise check text
// files out with CRLF, and the script must run under plain sh.
func TestTestdataKeepsItsLineEndings(t *testing.T) {
	script := filepath.Join("..", "..", "agents", "nearhost", "skills", "nearhost", "scripts", "nearhost.sh")
	body, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "\r\n") {
		t.Errorf("%s contains CRLF line endings; it must be checked out as committed", script)
	}

	attrs, err := os.ReadFile(filepath.Join("..", "..", ".gitattributes"))
	if err != nil {
		t.Fatalf(".gitattributes: %v", err)
	}
	found := false
	for _, line := range strings.Split(string(attrs), "\n") {
		if strings.TrimRight(line, "\r") == "* -text" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf(".gitattributes must contain the line %q", "* -text")
	}
}
