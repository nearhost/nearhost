//go:build unix

package cli

// A chmod to 0o500 does not stop writes on Windows, so the CA's move to the
// data directory cannot be made to fail this way there.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
)

// failCAMove leaves a CA at the old path that cannot move, as the data
// directory's parent can't be written.
func failCAMove(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root writes to read-only directories")
	}
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(cfgHome, "nearhost", "caddy"), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := config.DataDir()
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(data)
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o700) })
	if note := migrateCANote(); !strings.Contains(note, "Warning") {
		t.Fatalf("the move did not fail: %q", note)
	}
}

// The interface hides stderr while it runs, so a CA that could not move is
// warned of in the interface's log, not on stderr.
func TestBareNearhostPutsTheMoveWarningInTheLog(t *testing.T) {
	failCAMove(t)
	ran := false
	b := fakeUI(t, true, &ran)
	var events <-chan engine.Event
	b.engineMade = func(eng *engine.Engine) {
		ch, unsubscribe := eng.Events().Subscribe(16)
		t.Cleanup(unsubscribe)
		events = ch
	}
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	out, err := runRoot(t, uiRoot(b), "--config", cfgPath, "--https-port", "8443", "--http-port", "8080")
	if err != nil || !ran {
		t.Fatalf("err %v, ran %v", err, ran)
	}
	if strings.Contains(out, "Warning") {
		t.Errorf("warned on stderr, hidden behind the interface:\n%s", out)
	}
	for logged := false; !logged; {
		select {
		case ev := <-events:
			logged = strings.Contains(ev.Message, "could not move its local CA")
		default:
			t.Fatal("the warning was not published to the interface's log")
		}
	}
}

// A run that fails before the interface exists prints the warning, which
// would otherwise be lost.
func TestBareNearhostPrintsTheMoveWarningWhenItFailsEarly(t *testing.T) {
	failCAMove(t)
	ran := false
	b := fakeUI(t, true, &ran)
	b.workDir = func() (string, error) { return "", errors.New("no working directory") }
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	out, err := runRoot(t, uiRoot(b), "--config", cfgPath, "--https-port", "8443", "--http-port", "8080")
	if err == nil || ran {
		t.Fatalf("err %v, ran %v", err, ran)
	}
	if !strings.Contains(out, "could not move its local CA") {
		t.Errorf("no warning on stderr:\n%s", out)
	}
}
