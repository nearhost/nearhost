package elevate

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestElevatedOnWindows(t *testing.T) {
	if got, want := Elevated(), windows.GetCurrentProcessToken().IsElevated(); got != want {
		t.Errorf("Elevated() = %v, want %v", got, want)
	}
}
