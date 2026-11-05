package config

import (
	"regexp"
	"testing"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func TestMachineIDOnWindows(t *testing.T) {
	if id := machineID(); !uuidPattern.MatchString(id) {
		t.Fatalf("machineID() = %q, want a UUID", id)
	}
}
