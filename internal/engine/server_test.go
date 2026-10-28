package engine

import (
	"errors"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
)

func TestOpenFilesNotConnected(t *testing.T) {
	cfg := &config.Config{Servers: []config.Server{{Name: "apollo", SSHHost: "apollo"}}}
	h := newHarness(t, cfg)
	f, err := h.eng.OpenFiles(bg, "apollo")
	if f != nil || !errors.Is(err, ErrNotConnected) || !strings.Contains(err.Error(), "Nearhost isn't connected to apollo") {
		t.Fatalf("OpenFiles = %v, %v; want ErrNotConnected", f, err)
	}
	if log := h.log(); log != "" {
		t.Fatalf("ssh was run:\n%s", log)
	}
}
