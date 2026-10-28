package desktop

import (
	"runtime"
	"slices"
	"testing"
)

func TestOpenCommand(t *testing.T) {
	url := "https://a.test"
	var want []string
	switch runtime.GOOS {
	case "darwin":
		want = []string{"open", url}
	case "windows":
		want = []string{"rundll32", "url.dll,FileProtocolHandler", url}
	default:
		want = []string{"xdg-open", url}
	}
	if got := openCommand(url); !slices.Equal(got, want) {
		t.Errorf("openCommand(%q) = %q, want %q", url, got, want)
	}
}
